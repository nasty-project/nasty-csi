package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// Static errors for iSCSI operations.
var (
	ErrISCSIAdmNotFound     = errors.New("iscsiadm command not found - please install open-iscsi")
	ErrISCSIDeviceNotFound  = errors.New("iSCSI device not found")
	ErrISCSIDeviceTimeout   = errors.New("timeout waiting for iSCSI device to appear")
	ErrISCSILoginFailed     = errors.New("failed to login to iSCSI target")
	ErrISCSIDiscoveryFailed = errors.New("iSCSI discovery failed - iscsid may not be running or accessible")
	ErrISCSITargetNotInDB   = errors.New("iSCSI target not found in node database after discovery")
)

// defaultISCSIMountOptions are sensible defaults for iSCSI filesystem mounts.
// "errors=continue" prevents ext4 from remounting read-only on transient I/O
// errors (e.g., iSCSI session timeout during NAS downtime). The default
// "errors=remount-ro" is designed for local disks where I/O errors indicate
// hardware failure; for network block devices the errors are transient and the
// session will recover, so keeping the filesystem read-write is correct.
var defaultISCSIMountOptions = []string{"noatime", "_netdev", "errors=continue"}

// iscsiadmCmd builds a command to run iscsiadm, using nsenter to execute
// in the host's namespaces when running in a container. This allows the
// container to use the host's iscsid daemon.
func iscsiadmCmd(ctx context.Context, args ...string) *exec.Cmd {
	// Check if we're in a container by looking for /proc/1/ns/mnt
	// If accessible and we have hostPID, use nsenter to run in host namespace
	if _, err := os.Stat("/proc/1/ns/mnt"); err == nil {
		// Use nsenter to enter host's mount namespace (for /etc/iscsi, /run)
		// and IPC namespace (for iscsid communication)
		nsenterArgs := make([]string, 0, 4+len(args))
		nsenterArgs = append(nsenterArgs, "--mount=/proc/1/ns/mnt", "--ipc=/proc/1/ns/ipc", "--", "iscsiadm")
		nsenterArgs = append(nsenterArgs, args...)
		klog.V(5).Infof("Running iscsiadm via nsenter: nsenter %v", nsenterArgs)
		return exec.CommandContext(ctx, "nsenter", nsenterArgs...)
	}

	// Not in container or no access to host namespaces - run directly
	klog.V(5).Infof("Running iscsiadm directly: iscsiadm %v", args)
	return exec.CommandContext(ctx, "iscsiadm", args...)
}

// iscsiConnectionParams holds validated iSCSI connection parameters.
type iscsiConnectionParams struct {
	iqn    string
	server string
	port   string
	lun    int
}

// stageISCSIVolume stages an iSCSI volume by logging into the target.
// It uses a retry mechanism to handle transient device stability issues.
func (s *NodeService) stageISCSIVolume(ctx context.Context, req *csi.NodeStageVolumeRequest, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Validate and extract connection parameters
	params, err := s.validateISCSIParams(volumeContext)
	if err != nil {
		return nil, err
	}

	isBlockVolume := volumeCapability.GetBlock() != nil
	datasetName := volumeContext["datasetName"]
	klog.V(4).Infof("Staging iSCSI volume %s (block mode: %v): server=%s:%s, IQN=%s, LUN=%d, dataset=%s",
		volumeID, isBlockVolume, params.server, params.port, params.iqn, params.lun, datasetName)

	// Try to reuse existing connection (idempotency), or clean up orphaned sessions.
	// NodeStageVolume is only called for new pods, so any existing session for this
	// IQN belongs to a deleted pod and is safe to force-kill.
	if devicePath, findErr := s.findISCSIDevice(ctx, params); findErr == nil && devicePath != "" {
		// Verify the iSCSI session is healthy before reusing.
		sessionState, stateErr := getISCSISessionState(ctx, devicePath)
		switch {
		case stateErr != nil:
			klog.Warningf("Failed to check iSCSI session state for %s: %v - forcing re-login", devicePath, stateErr)
		case sessionState == iscsiSessionStateLoggedIn:
			klog.V(4).Infof("iSCSI device already connected at %s (session healthy) - reusing existing connection", devicePath)
			return s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
		default:
			klog.Warningf("iSCSI session for %s is %q (not LOGGED_IN) - forcing cleanup", devicePath, sessionState)
		}

		// Session is stale (orphaned from a deleted pod). Force-kill it by
		// setting replacement_timeout=0 so the kernel gives up immediately,
		// then logout and delete the node record.
		s.forceKillISCSISession(params) //nolint:contextcheck // intentionally uses Background context
	} else {
		// No device found via session -P 3, but there might still be an orphaned
		// session in recovery state (e.g., after a force-deleted pod where the
		// device has already disappeared from sysfs). Check by IQN and kill it.
		s.forceKillISCSISessionByIQN(params) //nolint:contextcheck // intentionally uses Background context
	}

	// Check if iscsiadm is installed
	if checkErr := s.checkISCSIAdm(ctx); checkErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "open-iscsi not available: %v", checkErr)
	}

	const (
		maxRetries = 2
		retryDelay = 5 * time.Second
	)

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			klog.Infof("iSCSI staging attempt %d/%d for volume %s", attempt, maxRetries, volumeID)
		}

		// Discover and login to iSCSI target
		if loginErr := s.loginISCSITarget(ctx, params); loginErr != nil {
			lastErr = loginErr
			klog.Warningf("iSCSI login attempt %d failed: %v", attempt, loginErr)
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		// Wait for device to appear
		devicePath, err := s.waitForISCSIDevice(ctx, params, 30*time.Second)
		if err != nil {
			lastErr = err
			klog.Warningf("iSCSI device wait failed on attempt %d: %v", attempt, err)
			// Cleanup: logout before retry
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after device wait failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		klog.V(4).Infof("iSCSI device connected at %s (IQN: %s, LUN: %d, dataset: %s) on attempt %d",
			devicePath, params.iqn, params.lun, datasetName, attempt)

		// Try staging - if device becomes unavailable during staging, retry the whole connection
		stageResp, stageErr := s.stageISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, isBlockVolume, volumeContext)
		if stageErr == nil {
			return stageResp, nil
		}

		// Check if this is a retryable error (device disappeared during staging)
		if status.Code(stageErr) == codes.Unavailable {
			lastErr = stageErr
			klog.Warningf("iSCSI staging failed on attempt %d (device unstable): %v", attempt, stageErr)
			// Logout and retry - the device may have become stale
			if logoutErr := s.logoutISCSITarget(ctx, params); logoutErr != nil {
				klog.Warningf("Failed to logout from iSCSI target after staging failure: %v", logoutErr)
			}
			if attempt < maxRetries {
				time.Sleep(retryDelay)
			}
			continue
		}

		// Non-retryable error - fail immediately
		return nil, stageErr
	}

	// All retries exhausted
	return nil, status.Errorf(codes.Internal, "Failed to stage iSCSI volume after %d attempts: %v", maxRetries, lastErr)
}

// validateISCSIParams validates and extracts iSCSI connection parameters from volume context.
func (s *NodeService) validateISCSIParams(volumeContext map[string]string) (*iscsiConnectionParams, error) {
	params := &iscsiConnectionParams{
		iqn:    volumeContext[VolumeContextKeyISCSIIQN],
		server: volumeContext["server"],
		port:   volumeContext["port"],
		lun:    0, // Always LUN 0 with dedicated targets
	}

	// Log all volume context keys for debugging
	klog.Infof("iSCSI validateISCSIParams - volume context keys: %v", volumeContext)
	klog.Infof("iSCSI validateISCSIParams - extracted IQN: '%s', server: '%s', port: '%s'",
		params.iqn, params.server, params.port)

	if params.iqn == "" || params.server == "" {
		return nil, status.Error(codes.InvalidArgument, "iSCSI IQN and server must be provided in volume context")
	}

	// Default port
	if params.port == "" {
		params.port = "3260"
	}

	return params, nil
}

// checkISCSIAdm checks if iscsiadm is available (either directly or via nsenter).
func (s *NodeService) checkISCSIAdm(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := iscsiadmCmd(checkCtx, "--version")
	if err := cmd.Run(); err != nil {
		return ErrISCSIAdmNotFound
	}
	return nil
}

// loginISCSITarget discovers and logs into an iSCSI target.
func (s *NodeService) loginISCSITarget(ctx context.Context, params *iscsiConnectionParams) error {
	portal := params.server + ":" + params.port

	// Step 1: Discovery
	klog.Infof("iSCSI: Discovering targets at portal %s for IQN %s", portal, params.iqn)
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 30*time.Second)
	defer discoverCancel()

	discoverCmd := iscsiadmCmd(discoverCtx, "-m", "discovery", "-t", "sendtargets", "-p", portal)
	output, err := discoverCmd.CombinedOutput()
	if err != nil {
		// Log the discovery error - this is critical for debugging
		klog.Errorf("iSCSI discovery failed at %s: %v, output: %s", portal, err, string(output))
		// Check if it's a connection error to iscsid
		if strings.Contains(string(output), "connect") || strings.Contains(string(output), "Connection refused") {
			return fmt.Errorf("%w: %s", ErrISCSIDiscoveryFailed, string(output))
		}
		// Continue anyway - target might already be known from previous discovery
		klog.Warningf("Continuing despite discovery failure - target may already be known")
	} else {
		klog.Infof("iSCSI discovery successful at %s, discovered targets:\n%s", portal, string(output))
	}

	// Step 2: Fix portal address for NAT/DNAT environments.
	// Discovery stores the target's self-reported IP (e.g., 10.0.0.22) in the node database,
	// but we may only be able to reach the public IP (e.g., 152.70.42.159).
	// iscsiadm won't let us update node.conn[0].address (it's a lookup key), so we delete
	// the discovered entry and create a new one with the reachable portal.
	discoveredPortal := findDiscoveredPortal(string(output), params.iqn)
	if discoveredPortal != "" && discoveredPortal != portal {
		klog.Infof("iSCSI: Target reported portal %s, replacing with reachable address %s", discoveredPortal, portal)

		// Delete the entry with the unreachable portal
		delCtx, delCancel := context.WithTimeout(ctx, 5*time.Second)
		defer delCancel()
		delCmd := iscsiadmCmd(delCtx, "-m", "node", "-T", params.iqn, "-p", discoveredPortal, "--op", "delete")
		if delOutput, delErr := delCmd.CombinedOutput(); delErr != nil {
			klog.V(4).Infof("Failed to delete old node entry (may not exist): %v, output: %s", delErr, string(delOutput))
		}

		// Create a new entry with the reachable portal
		newCtx, newCancel := context.WithTimeout(ctx, 5*time.Second)
		defer newCancel()
		newCmd := iscsiadmCmd(newCtx, "-m", "node", "--op", "new", "-T", params.iqn, "-p", portal)
		if newOutput, newErr := newCmd.CombinedOutput(); newErr != nil {
			klog.Warningf("Failed to create node entry with reachable portal: %v, output: %s", newErr, string(newOutput))
		}

		// Disable authentication on the new entry (same as targetcli default)
		authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
		defer authCancel()
		authCmd := iscsiadmCmd(authCtx, "-m", "node", "-T", params.iqn, "-p", portal,
			"--op", "update", "-n", "node.session.auth.authmethod", "-v", "None")
		if authOutput, authErr := authCmd.CombinedOutput(); authErr != nil {
			klog.V(4).Infof("Failed to set auth method (may be default): %v, output: %s", authErr, string(authOutput))
		}
	}

	// Step 3: Login using the reachable portal
	klog.Infof("Logging into iSCSI target: %s (portal: %s)", params.iqn, portal)
	loginCtx, loginCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loginCancel()

	loginCmd := iscsiadmCmd(loginCtx, "-m", "node", "-T", params.iqn, "-p", portal, "--login")
	output, err = loginCmd.CombinedOutput()
	if err != nil {
		// Check if already logged in
		alreadyLoggedIn := strings.Contains(string(output), "already present") ||
			strings.Contains(string(output), "session already exists") ||
			strings.Contains(string(output), "session exists")
		if alreadyLoggedIn {
			klog.V(4).Infof("iSCSI target already logged in: %s", params.iqn)
			return nil
		}
		klog.Errorf("iSCSI login failed for target %s: %v, output: %s", params.iqn, err, string(output))
		return fmt.Errorf("%w: %s", ErrISCSILoginFailed, string(output))
	}

	klog.Infof("Successfully logged into iSCSI target: %s, output: %s", params.iqn, string(output))
	return nil
}

// findDiscoveredPortal parses iscsiadm discovery output to find the portal for a given IQN.
// Discovery output format: "10.0.0.22:3260,1 iqn.2137-04.storage.nasty:volume-name".
func findDiscoveredPortal(discoveryOutput, iqn string) string {
	for _, line := range strings.Split(discoveryOutput, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, iqn) {
			// Format: "ip:port,tpg iqn"
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// Strip the ",tpg" suffix from "ip:port,1"
				portalWithTPG := parts[0]
				if idx := strings.LastIndex(portalWithTPG, ","); idx != -1 {
					return portalWithTPG[:idx]
				}
				return portalWithTPG
			}
		}
	}
	return ""
}

// logoutISCSITarget logs out from an iSCSI target and removes the node record
// to prevent iscsid from trying to reconnect to deleted targets.
//
//nolint:contextcheck // intentionally uses Background context to survive kubelet gRPC retries
func (s *NodeService) logoutISCSITarget(_ context.Context, params *iscsiConnectionParams) error {
	klog.V(4).Infof("Logging out from iSCSI target: %s", params.iqn)

	// Use Background context — kubelet retry cancellation must not kill a logout in progress,
	// as that leaves zombie sessions stuck in REOPEN state.
	logoutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Logout from target on all portals
	cmd := iscsiadmCmd(logoutCtx, "-m", "node", "-T", params.iqn, "--logout")
	output, err := cmd.CombinedOutput()
	if err != nil {
		alreadyLoggedOut := strings.Contains(string(output), "No matching sessions") ||
			strings.Contains(string(output), "not found")
		if !alreadyLoggedOut {
			klog.Warningf("iSCSI logout failed for %s: %v (output: %s)", params.iqn, err, strings.TrimSpace(string(output)))
		}
	} else {
		klog.V(4).Infof("Successfully logged out from iSCSI target: %s", params.iqn)
	}

	// Delete the node record so iscsid won't try to reconnect
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer deleteCancel()
	deleteCmd := iscsiadmCmd(deleteCtx, "-m", "node", "-T", params.iqn, "-o", "delete")
	if deleteOutput, deleteErr := deleteCmd.CombinedOutput(); deleteErr != nil {
		if !strings.Contains(string(deleteOutput), "not found") {
			klog.V(4).Infof("Failed to delete iSCSI node record for %s (non-fatal): %v", params.iqn, deleteErr)
		}
	} else {
		klog.V(4).Infof("Deleted iSCSI node record for %s", params.iqn)
	}

	return nil
}

// forceKillISCSISession forces an immediate teardown of an orphaned iSCSI session.
// It sets replacement_timeout=0 so the kernel stops recovery immediately, then
// logs out and deletes the node record. This is only safe for sessions belonging
// to deleted pods — never for sessions serving running workloads.
func (s *NodeService) forceKillISCSISession(params *iscsiConnectionParams) {
	klog.Infof("Force-killing orphaned iSCSI session for %s", params.iqn)

	// Set replacement_timeout=0 to make the kernel give up recovery immediately.
	// Without this, logout hangs until the original replacement_timeout (120s+) expires.
	toCtx, toCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer toCancel()
	toCmd := iscsiadmCmd(toCtx, "-m", "node", "-T", params.iqn,
		"--op", "update", "-n", "node.session.timeo.replacement_timeout", "-v", "0")
	if out, err := toCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("Failed to set replacement_timeout=0 for %s (may not exist): %v, output: %s", params.iqn, err, strings.TrimSpace(string(out)))
	}

	// Brief pause for the kernel to process the timeout change and fail the session
	time.Sleep(1 * time.Second)

	// Now logout — should complete immediately since the session is no longer recovering
	if logoutErr := s.logoutISCSITarget(context.Background(), params); logoutErr != nil {
		klog.Warningf("Failed to logout orphaned iSCSI session for %s: %v (continuing with fresh login)", params.iqn, logoutErr)
	}

	// Wait for kernel to clean up the SCSI device
	time.Sleep(2 * time.Second)
}

// forceKillISCSISessionByIQN checks if any iSCSI session exists for the given IQN
// and force-kills it. This handles the case where findISCSIDevice returns nothing
// (device already gone from sysfs) but a session is still lingering in recovery state.
func (s *NodeService) forceKillISCSISessionByIQN(params *iscsiConnectionParams) {
	// Check if any session exists for this IQN
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	cmd := iscsiadmCmd(checkCtx, "-m", "session")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return // No sessions at all
	}

	if !strings.Contains(string(output), params.iqn) {
		return // No session for this IQN
	}

	klog.Infof("Found orphaned iSCSI session for %s (device already gone) — force-killing", params.iqn)
	s.forceKillISCSISession(params)
}

// findISCSIDevice finds the device path for an iSCSI LUN.
func (s *NodeService) findISCSIDevice(ctx context.Context, params *iscsiConnectionParams) (string, error) {
	// Query active sessions with detail level 3 to see attached devices
	sessionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := iscsiadmCmd(sessionCtx, "-m", "session", "-P", "3")
	output, err := cmd.CombinedOutput()

	// Always log the output for debugging
	klog.Infof("iscsiadm -m session -P 3: err=%v, output:\n%s", err, string(output))

	if err != nil {
		return "", ErrISCSIDeviceNotFound
	}

	deviceName := parseISCSISessionDevice(string(output), params.iqn)
	if deviceName == "" {
		klog.Infof("parseISCSISessionDevice found no device for IQN: %s", params.iqn)
		return "", ErrISCSIDeviceNotFound
	}

	devicePath := "/dev/" + deviceName
	klog.Infof("Found iSCSI device: %s", devicePath)
	return devicePath, nil
}

// parseISCSISessionDevice parses iscsiadm -m session -P 3 output to find
// the attached disk for a specific IQN.
func parseISCSISessionDevice(output, targetIQN string) string {
	lines := strings.Split(output, "\n")
	inTargetSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check if we're entering a target section
		// Format: "Target: iqn.2005-10.org.freenas.ctl:pvc-xxx (non-flash)"
		// The IQN might be followed by extra text like "(non-flash)"
		if strings.HasPrefix(line, "Target:") {
			targetLine := strings.TrimPrefix(line, "Target:")
			targetLine = strings.TrimSpace(targetLine)
			// Check if this line contains our target IQN (use Contains/HasPrefix
			// because there might be extra text after the IQN)
			inTargetSection = strings.HasPrefix(targetLine, targetIQN)
			continue
		}

		// If we're in the right target section, look for attached disk
		if inTargetSection && strings.Contains(line, "Attached scsi disk") {
			// Line format: "Attached scsi disk sda	State: running"
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "disk" && i+1 < len(parts) {
					return parts[i+1] // Return device name like "sda"
				}
			}
		}
	}

	return ""
}

// waitForISCSIDevice waits for the iSCSI device to appear after login.
func (s *NodeService) waitForISCSIDevice(ctx context.Context, params *iscsiConnectionParams, timeout time.Duration) (string, error) {
	klog.Infof("Waiting for iSCSI device for IQN %s (timeout: %v)", params.iqn, timeout)

	deadline := time.Now().Add(timeout)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		devicePath, err := s.findISCSIDevice(ctx, params)
		if err == nil && devicePath != "" {
			if _, statErr := os.Stat(devicePath); statErr == nil {
				klog.Infof("iSCSI device ready: %s (attempt %d)", devicePath, attempt)
				return devicePath, nil
			}
		}
		time.Sleep(2 * time.Second)
	}

	return "", ErrISCSIDeviceTimeout
}

// stageISCSIDevice stages an iSCSI device as either block or filesystem volume.
func (s *NodeService) stageISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, isBlockVolume bool, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	// Verify device still exists before proceeding (it may have disappeared due to race conditions
	// with previous volume cleanup or iSCSI session issues)
	if _, err := os.Stat(devicePath); err != nil {
		klog.Warningf("iSCSI device %s disappeared before staging could complete: %v", devicePath, err)
		return nil, status.Errorf(codes.Unavailable,
			"iSCSI device %s became unavailable: %v", devicePath, err)
	}

	// For filesystem volumes, wait for device to be fully initialized
	if !isBlockVolume {
		if err := waitForDeviceInitialization(ctx, devicePath); err != nil {
			// Check if device disappeared during initialization
			if _, statErr := os.Stat(devicePath); statErr != nil {
				return nil, status.Errorf(codes.Unavailable,
					"iSCSI device %s became unavailable during initialization: %v", devicePath, err)
			}
			return nil, status.Errorf(codes.Internal, "Device initialization timeout: %v", err)
		}

		// Force device rescan
		if err := forceDeviceRescan(ctx, devicePath); err != nil {
			klog.Warningf("Device rescan warning for %s: %v (continuing anyway)", devicePath, err)
		}

		// Stabilization delay
		const deviceMetadataDelay = 2 * time.Second
		klog.V(4).Infof("Waiting %v for device %s metadata to stabilize", deviceMetadataDelay, devicePath)
		time.Sleep(deviceMetadataDelay)

		// Verify device still exists after stabilization
		if _, err := os.Stat(devicePath); err != nil {
			klog.Warningf("iSCSI device %s disappeared after stabilization: %v", devicePath, err)
			return nil, status.Errorf(codes.Unavailable,
				"iSCSI device %s became unavailable after stabilization: %v", devicePath, err)
		}
	}

	if isBlockVolume {
		return s.stageBlockDevice(devicePath, stagingTargetPath)
	}
	return s.formatAndMountISCSIDevice(ctx, volumeID, devicePath, stagingTargetPath, volumeCapability, volumeContext)
}

// formatAndMountISCSIDevice formats (if needed) and mounts an iSCSI device.
func (s *NodeService) formatAndMountISCSIDevice(ctx context.Context, volumeID, devicePath, stagingTargetPath string, volumeCapability *csi.VolumeCapability, volumeContext map[string]string) (*csi.NodeStageVolumeResponse, error) {
	datasetName := volumeContext["datasetName"]
	iqn := volumeContext[VolumeContextKeyISCSIIQN]
	klog.V(4).Infof("Formatting and mounting iSCSI device: device=%s, path=%s, volume=%s, dataset=%s, IQN=%s",
		devicePath, stagingTargetPath, volumeID, datasetName, iqn)

	// Log device information
	s.logDeviceInfo(ctx, devicePath)

	// Verify device size
	if err := s.verifyDeviceSize(ctx, devicePath, volumeContext); err != nil {
		klog.Errorf("Device size verification FAILED for %s: %v", devicePath, err)
		return nil, status.Errorf(codes.FailedPrecondition,
			"Device size mismatch detected - refusing to mount: %v", err)
	}

	// Determine filesystem type
	fsType := fsTypeExt4
	if mnt := volumeCapability.GetMount(); mnt != nil && mnt.FsType != "" {
		fsType = mnt.FsType
	}

	// Check if device is cloned from snapshot
	isClone := false
	if cloned, exists := volumeContext[VolumeContextKeyClonedFromSnap]; exists && cloned == VolumeContextValueTrue {
		isClone = true
		klog.V(4).Infof("Volume %s was cloned from snapshot - adding stabilization delay", volumeID)
		const cloneStabilizationDelay = 5 * time.Second
		time.Sleep(cloneStabilizationDelay)
	}

	// Handle formatting
	if err := s.handleDeviceFormatting(ctx, volumeID, devicePath, fsType, datasetName, iqn, isClone); err != nil {
		return nil, err
	}

	// Create staging target path
	if err := os.MkdirAll(stagingTargetPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging target path: %v", err)
	}

	// Check if already mounted
	mounted, err := mount.IsMounted(ctx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}
	if mounted {
		klog.V(4).Infof("Staging path %s is already mounted", stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Mount the device
	klog.V(4).Infof("Mounting device %s to %s", devicePath, stagingTargetPath)

	var userMountOptions []string
	if mnt := volumeCapability.GetMount(); mnt != nil {
		userMountOptions = mnt.MountFlags
	}
	mountOptions := getISCSIMountOptions(userMountOptions)

	klog.V(4).Infof("iSCSI mount options: user=%v, final=%v", userMountOptions, mountOptions)

	args := []string{devicePath, stagingTargetPath}
	if len(mountOptions) > 0 {
		args = []string{"-o", mount.JoinMountOptions(mountOptions), devicePath, stagingTargetPath}
	}

	mountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to mount device: %v, output: %s", err, string(output))
	}

	klog.V(4).Infof("Mounted iSCSI device to staging path")
	return &csi.NodeStageVolumeResponse{}, nil
}

// unstageISCSIVolume unstages an iSCSI volume by logging out from the target.
//
//nolint:contextcheck // intentionally uses Background context to survive kubelet gRPC retries
func (s *NodeService) unstageISCSIVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest, volumeContext map[string]string) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	klog.V(4).Infof("Unstaging iSCSI volume %s from %s", volumeID, stagingTargetPath)

	// Get IQN from volume context
	iqn := volumeContext[VolumeContextKeyISCSIIQN]

	// Use Background context for cleanup — kubelet retry cancellation must not
	// leave half-torn-down iSCSI sessions (zombie REOPEN state).
	//nolint:contextcheck // intentionally decoupled from kubelet retry context
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cleanupCancel()

	// Check if mounted and unmount if necessary
	mounted, err := mount.IsMounted(cleanupCtx, stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if staging path is mounted: %v", err)
	}

	if mounted {
		klog.V(4).Infof("Unmounting staging path: %s", stagingTargetPath)
		if err := mount.Unmount(cleanupCtx, stagingTargetPath); err != nil {
			// Don't abort — proceed with logout to clean up the session.
			// The kernel will finish the unmount in the background.
			klog.Warningf("Unmount failed for %s (proceeding with logout): %v", stagingTargetPath, err)
		}
	}

	// If we don't have IQN, we can't logout
	if iqn == "" {
		klog.Warningf("Cannot determine IQN for volume %s - skipping iSCSI logout", volumeID)
		return &csi.NodeUnstageVolumeResponse{}, nil
	}

	// Logout from the iSCSI target
	server := volumeContext["server"]
	port := volumeContext["port"]
	if port == "" {
		port = "3260"
	}

	params := &iscsiConnectionParams{
		iqn:    iqn,
		server: server,
		port:   port,
	}

	klog.V(4).Infof("Logging out from iSCSI target for volume %s: IQN=%s", volumeID, iqn)
	if err := s.logoutISCSITarget(cleanupCtx, params); err != nil {
		klog.Warningf("Failed to logout from iSCSI target (continuing anyway): %v", err)
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// getISCSIMountOptions merges user-provided mount options with sensible defaults.
func getISCSIMountOptions(userOptions []string) []string {
	if len(userOptions) == 0 {
		return defaultISCSIMountOptions
	}

	// Build a map of user-specified option keys
	userOptionKeys := make(map[string]bool)
	for _, opt := range userOptions {
		key := extractISCSIOptionKey(opt)
		userOptionKeys[key] = true
	}

	// Start with user options, then add defaults that don't conflict
	result := make([]string, 0, len(userOptions)+len(defaultISCSIMountOptions))
	result = append(result, userOptions...)

	for _, defaultOpt := range defaultISCSIMountOptions {
		key := extractISCSIOptionKey(defaultOpt)
		if !userOptionKeys[key] {
			result = append(result, defaultOpt)
		}
	}

	return result
}

// extractISCSIOptionKey extracts the key from a mount option.
func extractISCSIOptionKey(option string) string {
	for i, c := range option {
		if c == '=' {
			return option[:i]
		}
	}
	return option
}
