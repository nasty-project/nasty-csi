package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// Static errors for recovery operations.
var errControllerRecoveryTimeout = errors.New("NVMe controller did not reach live state within timeout")

// defaultHealthMonitorInterval is how often the background health monitor
// checks storage sessions. 30 seconds is a good balance between fast
// detection and not spamming sysfs reads.
const defaultHealthMonitorInterval = 30 * time.Second

// StartHealthMonitor launches a background goroutine that periodically checks
// all iSCSI sessions and NVMe-oF controllers, recovering any that are stale.
// This catches data-plane failures even when the WebSocket control plane is fine
// (e.g., target service restart on the NAS, network partition affecting only
// the storage data path).
func (s *NodeService) StartHealthMonitor() {
	if s.testMode {
		return
	}
	go s.healthMonitorLoop()
}

// StopHealthMonitor signals the background health monitor to stop.
func (s *NodeService) StopHealthMonitor() {
	select {
	case <-s.stopCh:
		// Already closed
	default:
		close(s.stopCh)
	}
}

func (s *NodeService) healthMonitorLoop() {
	klog.Info("Starting background volume health monitor")
	ticker := time.NewTicker(defaultHealthMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			klog.Info("Volume health monitor stopped")
			return
		case <-ticker.C:
			s.runHealthCheck()
		}
	}
}

// runHealthCheck performs a single pass of health checking and recovery.
// It first tries to recover storage sessions (iSCSI re-login, NVMe controller reset).
// If devices are completely gone and unrecoverable, it force-unmounts the staging
// paths so kubelet detects the broken state and triggers re-staging.
func (s *NodeService) runHealthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Phase 1: Try to recover transport-level sessions
	iscsiRecovered := s.recoverISCSISessions(ctx)
	nvmeRecovered := s.recoverNVMeOFConnections(ctx)

	if iscsiRecovered > 0 || nvmeRecovered > 0 {
		klog.Infof("Health monitor recovered: iSCSI=%d, NVMe-oF=%d", iscsiRecovered, nvmeRecovered)
	}

	// Phase 2: Force-unmount staging paths for volumes whose devices are completely gone.
	// This allows kubelet to detect the broken mount and trigger NodeUnstageVolume +
	// NodeStageVolume, which will establish a fresh connection.
	s.unmountBrokenStagingPaths(ctx)
}

// unmountCooldown is how long we suppress repeated unmount attempts for the
// same staging path. This prevents log spam when kubelet is slow to re-stage.
const unmountCooldown = 5 * time.Minute

// unmountBrokenStagingPaths finds staging mounts where the source block device
// no longer exists and force-unmounts them so kubelet can re-stage.
func (s *NodeService) unmountBrokenStagingPaths(ctx context.Context) {
	// Use findmnt to discover all mounts under our CSI staging directory
	findmntCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(findmntCtx, "findmnt", "-n", "-o", "SOURCE,TARGET", "-t", "ext4,xfs", "--raw")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("findmnt for staging path check: %v", err)
		return
	}

	// Expire old entries from the cooldown map
	now := time.Now()
	for path, ts := range s.recentUnmounts {
		if now.Sub(ts) > unmountCooldown {
			delete(s.recentUnmounts, path)
		}
	}

	// Filter for our CSI driver's staging paths
	const stagingPathMarker = "/plugins/kubernetes.io/csi/nasty.csi.io/"
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, stagingPathMarker) {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		source := fields[0]
		target := fields[1]

		// Skip if we recently unmounted this path (avoid log spam)
		if _, cooldown := s.recentUnmounts[target]; cooldown {
			continue
		}

		// Check if the source device still exists
		if _, statErr := os.Stat(source); statErr == nil {
			// Device exists — check if it's actually accessible
			if isDeviceAccessible(ctx, source) {
				continue // Healthy
			}
			klog.Warningf("Device %s exists but is inaccessible (staging: %s)", source, target)
		} else {
			klog.Warningf("Device %s no longer exists (staging: %s)", source, target)
		}

		// Device is gone or inaccessible — force lazy unmount the staging path.
		// This makes kubelet's mount check fail, triggering the full re-stage flow.
		klog.Infof("Force-unmounting broken staging path %s (source device: %s)", target, source)
		umountCtx, umountCancel := context.WithTimeout(ctx, 15*time.Second)
		umountCmd := exec.CommandContext(umountCtx, "umount", "-l", target)
		if umountOutput, umountErr := umountCmd.CombinedOutput(); umountErr != nil {
			klog.Errorf("Failed to unmount broken staging path %s: %v (%s)", target, umountErr, strings.TrimSpace(string(umountOutput)))
		} else {
			klog.Infof("Successfully unmounted broken staging path %s — kubelet will re-stage", target)
		}
		umountCancel()

		// Record this unmount to suppress repeated attempts
		s.recentUnmounts[target] = now
	}
}

// isDeviceAccessible checks if a block device can be opened (not just stat'd).
// A device that exists in /dev but has a dead transport will fail to open.
func isDeviceAccessible(ctx context.Context, devicePath string) bool {
	// Use blockdev --getsize64 as a quick accessibility check
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "blockdev", "--getsize64", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	// If size is 0, device is likely broken
	size := strings.TrimSpace(string(output))
	return size != "" && size != "0"
}

// recoverVolumes is called asynchronously after the WebSocket connection to
// the NAS is re-established. It probes all active iSCSI sessions and NVMe-oF
// connections, and attempts to recover any that are stale/offline.
//
// This proactive recovery reduces the window between NAS availability and
// pod recovery — instead of waiting for kubelet to call NodeStageVolume
// (which may take minutes due to CrashLoopBackOff backoff), we fix the
// data plane immediately.
func (s *NodeService) recoverVolumes() {
	klog.Info("NAS reconnection detected — starting proactive volume recovery")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	iscsiRecovered := s.recoverISCSISessions(ctx)
	nvmeRecovered := s.recoverNVMeOFConnections(ctx)

	klog.Infof("Volume recovery complete: iSCSI sessions recovered=%d, NVMe-oF connections recovered=%d",
		iscsiRecovered, nvmeRecovered)
}

// recoverISCSISessions scans for iSCSI sessions in non-LOGGED_IN state
// and forces a logout/re-login to recover them.
func (s *NodeService) recoverISCSISessions(ctx context.Context) int {
	// Find all iSCSI block devices by scanning /sys/block/sd*
	matches, err := filepath.Glob("/sys/block/sd*")
	if err != nil || len(matches) == 0 {
		klog.V(4).Info("No iSCSI block devices found to check")
		return 0
	}

	recovered := 0
	for _, sysPath := range matches {
		devName := filepath.Base(sysPath)
		devicePath := "/dev/" + devName

		// Only check devices that are actually iSCSI (have a session symlink)
		sessionLink := sysPath + "/device/../../iscsi_session"
		if _, err := os.Stat(sessionLink); err != nil {
			// Not an iSCSI device
			continue
		}

		state, stateErr := getISCSISessionState(ctx, devicePath)
		if stateErr != nil {
			klog.V(4).Infof("Could not check session state for %s: %v", devicePath, stateErr)
			continue
		}

		if state == iscsiSessionStateLoggedIn {
			klog.V(4).Infof("iSCSI device %s session is healthy", devicePath)
			continue
		}

		klog.Warningf("iSCSI device %s session is %q — attempting recovery", devicePath, state)

		// Extract target IQN and portal from the session for re-login
		iqn, portal := getISCSISessionInfo(ctx, sysPath)
		if iqn == "" {
			klog.Warningf("Could not determine IQN for %s — skipping recovery", devicePath)
			continue
		}

		// Force session recovery via iscsiadm rescan/relogin
		if err := recoverISCSISession(ctx, iqn, portal); err != nil {
			klog.Errorf("Failed to recover iSCSI session for %s (IQN: %s): %v", devicePath, iqn, err)
			continue
		}

		recovered++
		klog.Infof("Successfully recovered iSCSI session for %s (IQN: %s)", devicePath, iqn)
	}

	return recovered
}

// recoverNVMeOFConnections scans for NVMe controllers with non-live state
// and triggers a controller reset to recover them.
func (s *NodeService) recoverNVMeOFConnections(ctx context.Context) int {
	// Find all NVMe controllers
	matches, err := filepath.Glob("/sys/class/nvme/nvme*")
	if err != nil || len(matches) == 0 {
		klog.V(4).Info("No NVMe controllers found to check")
		return 0
	}

	recovered := 0
	for _, ctrlPath := range matches {
		ctrlName := filepath.Base(ctrlPath)

		// Only check fabrics (NVMe-oF) controllers — skip local NVMe
		transportPath := ctrlPath + "/transport"
		transport, err := os.ReadFile(transportPath) //nolint:gosec // path constructed from sysfs glob
		if err != nil || strings.TrimSpace(string(transport)) == "" {
			continue // Not a fabrics controller
		}

		// Check controller state
		statePath := ctrlPath + "/state"
		stateData, err := os.ReadFile(statePath) //nolint:gosec // path constructed from sysfs glob
		if err != nil {
			klog.V(4).Infof("Could not read state for %s: %v", ctrlName, err)
			continue
		}

		state := strings.TrimSpace(string(stateData))
		if state == nvmeSubsystemStateLive {
			klog.V(4).Infof("NVMe controller %s is healthy (state: live)", ctrlName)
			continue
		}

		klog.Warningf("NVMe controller %s state is %q — attempting recovery via reset", ctrlName, state)

		// Attempt recovery by writing "1" to the reset_controller sysfs attribute
		resetPath := ctrlPath + "/reset_controller"
		if err := os.WriteFile(resetPath, []byte("1"), 0o200); err != nil {
			klog.Warningf("Controller reset via sysfs failed for %s: %v — trying nvme reset", ctrlName, err)
			// Fallback: use nvme-cli reset
			if resetErr := resetNVMeController(ctx, ctrlName); resetErr != nil {
				klog.Errorf("Failed to recover NVMe controller %s: %v", ctrlName, resetErr)
				continue
			}
		}

		// Wait for controller to come back to live state
		if err := waitForNVMeControllerRecovery(ctx, ctrlPath, 30*time.Second); err != nil {
			klog.Errorf("NVMe controller %s did not recover after reset: %v", ctrlName, err)
			continue
		}

		recovered++
		klog.Infof("Successfully recovered NVMe controller %s", ctrlName)
	}

	return recovered
}

// getISCSISessionInfo extracts the IQN and portal from sysfs for a given iSCSI device.
func getISCSISessionInfo(ctx context.Context, sysBlockPath string) (iqn, portal string) {
	// The target IQN is in /sys/block/sdX/device/../../iscsi_session/session*/targetname
	sessionGlob := sysBlockPath + "/device/../../iscsi_session/session*"
	sessions, err := filepath.Glob(sessionGlob)
	if err != nil || len(sessions) == 0 {
		// Alternative path structure
		sessions, _ = filepath.Glob(sysBlockPath + "/device/../../../iscsi_session/session*") //nolint:errcheck // best-effort fallback
	}

	for _, sess := range sessions {
		// Read targetname
		targetData, readErr := os.ReadFile(filepath.Join(sess, "targetname")) //nolint:gosec // path from sysfs glob
		if readErr == nil {
			iqn = strings.TrimSpace(string(targetData))
		}

		// Read connection info for portal
		connGlob := strings.Replace(sess, "iscsi_session", "iscsi_connection", 1)
		connGlob = strings.Replace(connGlob, "session", "connection", 1) + "*/persistent_address"
		conns, globErr := filepath.Glob(connGlob)
		if globErr == nil && len(conns) > 0 {
			addrData, addrErr := os.ReadFile(conns[0])
			if addrErr == nil {
				portal = strings.TrimSpace(string(addrData)) + ":3260"
			}
		}

		if iqn != "" {
			return iqn, portal
		}
	}

	// Fallback: parse iscsiadm session output
	cmd := exec.CommandContext(ctx, "iscsiadm", "-m", "session")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", ""
	}

	// Parse lines like: "tcp: [3] 10.10.20.100:3260,1 iqn.2137-04.storage.nasty:vol-name (non-flash)"
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			candidateIQN := strings.TrimSpace(fields[3])
			if strings.HasPrefix(candidateIQN, "iqn.") {
				// Return first match — caller will need to correlate with device
				return candidateIQN, strings.TrimSuffix(fields[2], ",1")
			}
		}
	}

	return "", ""
}

// recoverISCSISession forces recovery of a stale iSCSI session by issuing
// a session-level rescan/relogin via iscsiadm.
func recoverISCSISession(ctx context.Context, iqn, portal string) error {
	// First try: session-level rescan (non-destructive, triggers kernel reconnect)
	rescanCtx, rescanCancel := context.WithTimeout(ctx, 15*time.Second)
	defer rescanCancel()

	var rescanCmd *exec.Cmd
	if portal != "" {
		rescanCmd = exec.CommandContext(rescanCtx, "iscsiadm", "-m", "session", "-r", iqn, "--rescan")
	} else {
		rescanCmd = exec.CommandContext(rescanCtx, "iscsiadm", "-m", "session", "--rescan")
	}
	if output, err := rescanCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("Session rescan for %s: %v (%s)", iqn, err, strings.TrimSpace(string(output)))
	}

	// Wait briefly and check if rescan recovered the session
	time.Sleep(3 * time.Second)

	// Check session state after rescan using iscsiadm
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	checkCmd := exec.CommandContext(checkCtx, "iscsiadm", "-m", "session", "-P", "1")
	checkOutput, checkErr := checkCmd.CombinedOutput()
	if checkErr == nil && strings.Contains(string(checkOutput), iscsiSessionStateLoggedIn) {
		return nil // Recovered via rescan
	}

	// Second try: force logout and re-login
	klog.Infof("Rescan did not recover session for %s — forcing logout/re-login", iqn)

	logoutCtx, logoutCancel := context.WithTimeout(ctx, 15*time.Second)
	defer logoutCancel()
	logoutCmd := exec.CommandContext(logoutCtx, "iscsiadm", "-m", "node", "-T", iqn, "--logout")
	if output, err := logoutCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("Logout for recovery of %s: %v (%s)", iqn, err, strings.TrimSpace(string(output)))
	}

	// Small delay for cleanup
	time.Sleep(2 * time.Second)

	// Re-login
	loginCtx, loginCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loginCancel()

	var loginCmd *exec.Cmd
	if portal != "" {
		loginCmd = exec.CommandContext(loginCtx, "iscsiadm", "-m", "node", "-T", iqn, "-p", portal, "--login")
	} else {
		loginCmd = exec.CommandContext(loginCtx, "iscsiadm", "-m", "node", "-T", iqn, "--login")
	}
	output, err := loginCmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "already present") || strings.Contains(string(output), "session exists") {
			return nil // Session recovered somehow
		}
		return fmt.Errorf("re-login failed: %w: %s", err, string(output))
	}

	return nil
}

// resetNVMeController resets an NVMe controller using nvme-cli.
func resetNVMeController(ctx context.Context, ctrlName string) error {
	resetCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(resetCtx, "nvme", "reset", "/dev/"+ctrlName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nvme reset failed: %w: %s", err, string(output))
	}
	return nil
}

// waitForNVMeControllerRecovery polls the controller state until it becomes live
// or the timeout is reached.
func waitForNVMeControllerRecovery(ctx context.Context, ctrlPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	statePath := ctrlPath + "/state"

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		data, err := os.ReadFile(statePath) //nolint:gosec // path constructed from sysfs controller path
		if err == nil && strings.TrimSpace(string(data)) == nvmeSubsystemStateLive {
			return nil
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("%w: %v", errControllerRecoveryTimeout, timeout)
}
