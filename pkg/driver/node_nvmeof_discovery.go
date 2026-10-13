package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	exec "github.com/nasty-project/nasty-csi/pkg/cmdexec"

	"k8s.io/klog/v2"
)

// getSubsystemState returns the connection state of an NVMe subsystem ("live", "connecting", etc.)
// Returns empty string if subsystem not found or state cannot be determined.
func getSubsystemState(ctx context.Context, nqn string) string {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v", err)
		return ""
	}

	// Parse the JSON to find the subsystem and its state
	// Look for the NQN and then find the State field in the same subsystem block
	lines := strings.Split(string(output), "\n")
	foundNQN := false
	for _, line := range lines {
		if strings.Contains(line, nqn) {
			foundNQN = true
		}
		// Once we found the NQN, look for the State field
		if foundNQN && strings.Contains(line, "\"State\"") {
			// Extract state value: "State" : "live"
			parts := strings.Split(line, "\"")
			for i, part := range parts {
				if part == "State" && i+2 < len(parts) {
					state := strings.TrimSpace(parts[i+2])
					klog.V(4).Infof("Subsystem %s state: %s", nqn, state)
					return state
				}
			}
		}
		// Stop if we hit the next subsystem (next NQN)
		if foundNQN && strings.Contains(line, "\"NQN\"") && !strings.Contains(line, nqn) {
			break
		}
	}

	if foundNQN {
		klog.V(4).Infof("Found NQN %s but could not extract state", nqn)
	}
	return ""
}

// waitForSubsystemLive waits for the NVMe subsystem to reach "live" state.
// This is critical because even after nvme connect succeeds, the subsystem may not
// be immediately ready for device operations. Democratic-csi uses this pattern.
func waitForSubsystemLive(ctx context.Context, nqn string, timeout time.Duration) error {
	const (
		pollInterval = 2 * time.Second
		maxAttempts  = 30 // 30 × 2s = 60s max
	)

	klog.V(4).Infof("Waiting for NVMe subsystem %s to reach 'live' state (timeout: %v)", nqn, timeout)

	deadline := time.Now().Add(timeout)
	attempt := 0

	for time.Now().Before(deadline) && attempt < maxAttempts {
		attempt++

		state := getSubsystemState(ctx, nqn)
		if state == nvmeSubsystemStateLive {
			klog.V(4).Infof("NVMe subsystem %s is now live after %d attempts", nqn, attempt)
			return nil
		}

		if state != "" {
			klog.V(4).Infof("NVMe subsystem %s state is '%s', waiting for 'live' (attempt %d/%d)", nqn, state, attempt, maxAttempts)
		} else {
			klog.V(4).Infof("NVMe subsystem %s not yet visible in nvme list-subsys (attempt %d/%d)", nqn, attempt, maxAttempts)
		}

		// Trigger udev periodically to help device enumeration
		if attempt%5 == 0 {
			triggerUdevForNVMeSubsystem(ctx)
		}

		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for subsystem %s to become live: %w", nqn, ctx.Err())
		}
	}

	// Final state check
	finalState := getSubsystemState(ctx, nqn)
	if finalState == nvmeSubsystemStateLive {
		return nil
	}

	return fmt.Errorf("%w: NQN=%s, last state=%q, attempts=%d", ErrNVMeSubsystemTimeout, nqn, finalState, attempt)
}

// triggerUdevForNVMeSubsystem triggers udev to process new NVMe devices after a connection.
// This helps ensure the kernel and udev properly enumerate newly connected NVMe-oF devices.
func triggerUdevForNVMeSubsystem(ctx context.Context) {
	klog.V(4).Infof("Triggering udev to process new NVMe devices")

	// Trigger udev to process any new NVMe devices
	triggerCtx, triggerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer triggerCancel()
	triggerCmd := exec.CommandContext(triggerCtx, "udevadm", "trigger", "--action=add", "--subsystem-match=nvme")
	if output, err := triggerCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger for NVMe subsystem failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev add events for NVMe subsystem")
	}

	// Also trigger block subsystem in case block devices need processing
	blockTriggerCtx, blockTriggerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer blockTriggerCancel()
	blockTriggerCmd := exec.CommandContext(blockTriggerCtx, "udevadm", "trigger", "--action=add", "--subsystem-match=block")
	if output, err := blockTriggerCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm trigger for block subsystem failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("Triggered udev add events for block subsystem")
	}

	// Wait for udev to settle (process the events)
	settleCtx, settleCancel := context.WithTimeout(ctx, 15*time.Second)
	defer settleCancel()
	settleCmd := exec.CommandContext(settleCtx, "udevadm", "settle", "--timeout=10")
	if output, err := settleCmd.CombinedOutput(); err != nil {
		klog.V(4).Infof("udevadm settle failed: %v, output: %s (continuing anyway)", err, string(output))
	} else {
		klog.V(4).Infof("udevadm settle completed after NVMe connection")
	}
}

// findNVMeDeviceByNQN finds the device path for a given NQN.
// Each visible namespace must match the requested NQN in sysfs, including when
// native multipath gives it a different instance number than its controller.
func (s *NodeService) findNVMeDeviceByNQN(ctx context.Context, nqn string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Resolve identity first so ambiguity is not hidden by CLI parsing/fallbacks.
	if _, err := nvmeNamespaceDeviceForNQN(sysBlockPath, nqn); errors.Is(err, errNVMeAmbiguousNamespace) {
		return "", err
	}
	klog.V(4).Infof("Searching for NVMe device: NQN=%s (NSID=1)", nqn)

	// Use nvme list-subsys which shows NQN
	subsysOutput, err := s.runNVMeListSubsys(ctx)
	if err != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, falling back to sysfs", err)
		return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
	}

	// Try to parse the output and find the device
	devicePath := s.parseNVMeListSubsysOutputForNQN(subsysOutput, nqn)
	if devicePath != "" {
		return devicePath, nil
	}

	// Fall back to checking /sys/class/nvme if parsing failed
	return s.findNVMeDeviceByNQNFromSys(ctx, nqn)
}

// runNVMeListSubsys executes nvme list-subsys and returns the output.
func (s *NodeService) runNVMeListSubsys(ctx context.Context) ([]byte, error) {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	subsysCmd := exec.CommandContext(listCtx, "nvme", "list-subsys", "-o", "json")
	return subsysCmd.CombinedOutput()
}

// parseNVMeListSubsysOutputForNQN parses nvme list-subsys JSON output to find device path.
// Namespace identity comes from sysfs, not the CLI controller name.
func (s *NodeService) parseNVMeListSubsysOutputForNQN(output []byte, nqn string) string {
	lines := strings.Split(string(output), "\n")
	foundNQN := false

	for i, line := range lines {
		if !strings.Contains(line, nqn) {
			continue
		}

		foundNQN = true
		devicePath := s.extractDevicePathFromLinesForNQN(lines, i, nqn)
		if devicePath != "" {
			return devicePath
		}
	}

	if foundNQN {
		klog.Warningf("Found NQN but could not extract device name, falling back to sysfs")
	}
	return ""
}

// extractDevicePathFromLinesForNQN searches for controller name in lines after the NQN line.
// With independent subsystems, NSID is always 1.
func (s *NodeService) extractDevicePathFromLinesForNQN(lines []string, startIdx int, nqn string) string {
	// Look ahead for the "Name" field in the Paths section (up to 20 lines)
	endIdx := startIdx + 20
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	for j := startIdx; j < endIdx; j++ {
		if !strings.Contains(lines[j], "\"Name\"") || !strings.Contains(lines[j], "nvme") {
			continue
		}

		// Extract controller name - format: "Name" : "nvme0"
		parts := strings.Split(lines[j], "\"")
		controllerName := s.extractControllerFromParts(parts)
		if controllerName == "" {
			continue
		}

		// The CLI controller number is not a namespace device number.
		devicePath, err := nvmeNamespaceDeviceForNQN(sysBlockPath, nqn)
		if err != nil {
			return ""
		}
		klog.V(4).Infof("Found NVMe device from list-subsys: %s (controller: %s, NQN: %s)",
			devicePath, controllerName, nqn)
		return devicePath
	}
	return ""
}

// extractControllerFromParts extracts controller name from parsed JSON parts.
func (s *NodeService) extractControllerFromParts(parts []string) string {
	for k := range len(parts) - 1 {
		if parts[k] == "Name" && k+2 < len(parts) {
			return strings.TrimSpace(parts[k+2])
		}
	}
	return ""
}

// findNVMeDeviceByNQNFromSys finds NVMe device by checking /sys/class/nvme.
// With independent subsystems, NSID is always 1.
func (s *NodeService) findNVMeDeviceByNQNFromSys(ctx context.Context, nqn string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := nvmeNamespaceDeviceForNQN(sysBlockPath, nqn); errors.Is(err, errNVMeAmbiguousNamespace) {
		return "", err
	}
	klog.V(4).Infof("Searching for NVMe device via sysfs: NQN=%s (NSID=1)", nqn)

	// Read /sys/class/nvme/nvmeX/subsysnqn for each device
	nvmeDir := "/sys/class/nvme"
	entries, err := os.ReadDir(nvmeDir)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", nvmeDir, err)
	}

	klog.V(2).Infof("Searching %d NVMe controller(s) in sysfs for NQN: %s", len(entries), nqn)

	for _, entry := range entries {
		deviceName := entry.Name()
		// Skip non-controller entries (controllers are named nvme0, nvme1, etc.)
		// Note: Don't check entry.IsDir() because sysfs entries are symlinks
		if !strings.HasPrefix(deviceName, "nvme") || strings.Contains(deviceName, "-") {
			continue
		}
		// Skip namespace entries (like nvme0n1)
		if strings.Contains(deviceName[4:], "n") {
			continue
		}

		nqnPath := filepath.Join(nvmeDir, deviceName, "subsysnqn")

		//nolint:gosec // Reading NVMe subsystem info from standard sysfs path
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(5).Infof("Cannot read NQN for %s: %v", deviceName, err)
			continue
		}

		deviceNQN := strings.TrimSpace(string(data))
		// Log all NQN comparisons at V(2) for debugging device discovery issues
		klog.V(2).Infof("Controller %s sysfs NQN: %q (looking for: %q, match: %v)",
			deviceName, deviceNQN, nqn, deviceNQN == nqn)

		if deviceNQN == nqn {
			// Resolve the actual visible namespace owned by this NQN.
			devicePath, identityErr := nvmeNamespaceDeviceForNQN(sysBlockPath, nqn)
			if errors.Is(identityErr, errNVMeAmbiguousNamespace) {
				return "", identityErr
			}
			if identityErr != nil {
				return "", fmt.Errorf("%w: namespace for %s not ready: %w", ErrNVMeDeviceUnhealthy, nqn, identityErr)
			}
			// Check if device exists AND is healthy (non-zero size block device)
			if _, err := os.Stat(devicePath); err == nil {
				if s.isDeviceHealthy(ctx, devicePath) {
					klog.V(4).Infof("Found healthy NVMe device from sysfs: %s (controller: %s, NQN: %s)",
						devicePath, deviceName, nqn)
					return devicePath, nil
				}
				klog.V(2).Infof("Device %s exists but is not healthy (zero size or not a block device), trying ns-rescan", devicePath)
			}
			// Controller exists but namespace device doesn't exist or isn't healthy - try ns-rescan
			controllerPath := "/dev/" + deviceName
			klog.V(4).Infof("Found matching NQN on %s but device path %s not ready, trying ns-rescan", deviceName, devicePath)
			s.forceNamespaceRescan(ctx, controllerPath)
			// Check again after rescan - device must exist AND be healthy
			if _, err := os.Stat(devicePath); err == nil && s.isDeviceHealthy(ctx, devicePath) {
				klog.V(4).Infof("Found healthy NVMe device after ns-rescan: %s (controller: %s, NQN: %s)",
					devicePath, deviceName, nqn)
				return devicePath, nil
			}
			// NQN matches but device is unhealthy after ns-rescan
			// Return ErrNVMeDeviceUnhealthy - let the caller decide whether to:
			// - Disconnect (if this is a stale connection from previous run)
			// - Wait (if this is a freshly connected device still initializing)
			// NOTE: We do NOT disconnect here because this function is also called
			// during waitForNVMeDevice after a fresh connect, and disconnecting
			// would break the freshly connected controller.
			klog.V(2).Infof("Device path %s still not ready after ns-rescan (controller: %s) - returning unhealthy status", devicePath, deviceName)
			return devicePath, fmt.Errorf("%w: %s (controller: %s)", ErrNVMeDeviceUnhealthy, devicePath, deviceName)
		}
	}

	klog.Warningf("NVMe device not found in sysfs for NQN=%s", nqn)
	return "", fmt.Errorf("%w for NQN: %s", ErrNVMeDeviceNotFound, nqn)
}

// forceNamespaceRescan forces the kernel to rescan namespaces on an NVMe controller.
// This is a lightweight version that just does ns-rescan without full udev processing.
func (s *NodeService) forceNamespaceRescan(ctx context.Context, controllerPath string) {
	rescanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	klog.V(4).Infof("Forcing namespace rescan on controller %s", controllerPath)

	cmd := exec.CommandContext(rescanCtx, "nvme", "ns-rescan", controllerPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.V(4).Infof("nvme ns-rescan failed for %s: %v, output: %s (continuing anyway)", controllerPath, err, string(output))
	} else {
		klog.V(4).Infof("nvme ns-rescan completed for %s", controllerPath)
	}
	// Note: Don't call triggerUdevForNVMeSubsystem here - it's too slow (10s+ settle)
	// udev trigger is done periodically in waitForNVMeDevice instead
}

// waitForNVMeDevice waits for the NVMe device to appear after connection.
// With independent subsystems, NSID is always 1.
// Note: This should be called AFTER waitForSubsystemLive() has confirmed the subsystem is "live".
func (s *NodeService) waitForNVMeDevice(ctx context.Context, nqn string, timeout time.Duration) (string, error) {
	const pollInterval = 2 * time.Second // Match democratic-csi polling interval

	deadline := time.Now().Add(timeout)
	attempt := 0
	lastControllerFound := ""

	klog.V(4).Infof("Waiting for NVMe device for NQN %s (timeout: %v)", nqn, timeout)

	for time.Now().Before(deadline) {
		attempt++

		devicePath, controllerName, err := s.findNVMeDeviceByNQNWithController(ctx, nqn)
		switch {
		case err == nil && devicePath != "":
			// Verify device is accessible AND healthy (non-zero size)
			// This prevents returning a device that exists but isn't functional yet
			if _, statErr := os.Stat(devicePath); statErr == nil {
				if s.isDeviceHealthy(ctx, devicePath) {
					klog.Infof("NVMe device found and healthy at %s after %d attempts", devicePath, attempt)
					return devicePath, nil
				}
				klog.V(4).Infof("Device %s exists but reports zero size, waiting for initialization (attempt %d)", devicePath, attempt)
				// Force rescan periodically to help with initialization (every 5 attempts)
				if controllerName != "" && attempt%5 == 0 {
					s.forceNamespaceRescan(ctx, "/dev/"+controllerName)
				}
			} else if controllerName != "" {
				// Device path doesn't exist but we found the controller - try ns-rescan
				if controllerName != lastControllerFound {
					klog.V(4).Infof("Found controller %s for NQN %s but device %s doesn't exist, forcing ns-rescan", controllerName, nqn, devicePath)
					lastControllerFound = controllerName
					// First time seeing this controller - do immediate rescan
					s.forceNamespaceRescan(ctx, "/dev/"+controllerName)
				} else if attempt%5 == 0 {
					// Periodic rescan every 5 attempts
					s.forceNamespaceRescan(ctx, "/dev/"+controllerName)
				}
			}
		case errors.Is(err, ErrNVMeDeviceUnhealthy):
			// Device found but unhealthy - keep waiting, periodic rescan
			klog.V(4).Infof("NVMe device found but still initializing (unhealthy), waiting... (attempt %d, path: %s)", attempt, devicePath)
			if attempt%5 == 0 {
				if ctrl := extractNVMeController(devicePath); ctrl != "" {
					s.forceNamespaceRescan(ctx, ctrl)
				}
			}
		case errors.Is(err, ErrNVMeDeviceNotFound) && controllerName != "":
			// A live controller can precede namespace enumeration. Rescan that
			// controller, but never invent a namespace path from its number.
			if controllerName != lastControllerFound || attempt%5 == 0 {
				lastControllerFound = controllerName
				s.forceNamespaceRescan(ctx, "/dev/"+controllerName)
			}
		case errors.Is(err, errNVMeAmbiguousNamespace):
			return "", err
		default:
			// Can't find device - do diagnostic dump every 10 attempts
			if attempt%10 == 0 {
				s.logNVMeDiscoveryDiagnostics(ctx, nqn)
			}
		}

		// Trigger full udev processing periodically (every 10 attempts) to help enumeration
		if attempt%10 == 0 {
			triggerUdevForNVMeSubsystem(ctx)
		}

		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return "", fmt.Errorf("context canceled while waiting for NVMe device: %w", ctx.Err())
		}
	}

	// Final diagnostic dump before failing
	s.logNVMeDiscoveryDiagnostics(ctx, nqn)

	return "", fmt.Errorf("%w after %d attempts (NQN: %s, timeout: %v)", ErrNVMeDeviceTimeout, attempt, nqn, timeout)
}

// findNVMeDeviceByNQNWithController finds NVMe device and returns both device path and controller name.
func (s *NodeService) findNVMeDeviceByNQNWithController(ctx context.Context, nqn string) (devicePath, controllerName string, err error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return "", "", contextErr
	}
	if _, identityErr := nvmeNamespaceDeviceForNQN(sysBlockPath, nqn); errors.Is(identityErr, errNVMeAmbiguousNamespace) {
		return "", "", identityErr
	}
	// Use nvme list-subsys which shows NQN and controller mapping
	subsysOutput, listErr := s.runNVMeListSubsys(ctx)
	if listErr != nil {
		klog.V(4).Infof("nvme list-subsys failed: %v, falling back to sysfs", listErr)
		devicePath, err = s.findNVMeDeviceByNQNFromSys(ctx, nqn)
		return devicePath, "", err
	}

	// Parse the output to find controller name for this NQN
	controllerName = s.findControllerForNQN(string(subsysOutput), nqn)
	if controllerName != "" {
		devicePath, err = nvmeNamespaceDeviceForNQN(sysBlockPath, nqn)
		if err == nil {
			controller, _, ownerErr := nvmeControllerForNamespace(sysBlockPath, devicePath)
			if ownerErr != nil {
				return "", "", ownerErr
			}
			controllerName = filepath.Base(controller)
		}
		return devicePath, controllerName, err
	}

	// Fall back to sysfs
	devicePath, err = s.findNVMeDeviceByNQNFromSys(ctx, nqn)
	return devicePath, "", err
}

// findControllerForNQN parses nvme list-subsys output to find the controller name for a given NQN.
func (s *NodeService) findControllerForNQN(output, nqn string) string {
	lines := strings.Split(output, "\n")
	foundNQN := false

	for i, line := range lines {
		if strings.Contains(line, nqn) {
			foundNQN = true
		}
		if foundNQN && strings.Contains(line, "\"Name\"") && strings.Contains(line, "nvme") {
			// Extract controller name from "Name" : "nvme0"
			parts := strings.Split(line, "\"")
			for k := range len(parts) - 1 {
				if parts[k] == "Name" && k+2 < len(parts) {
					name := strings.TrimSpace(parts[k+2])
					if nvmeControllerName.MatchString(name) {
						return name
					}
				}
			}
		}
		// Reset if we've moved past this subsystem's section
		if foundNQN && i > 0 && strings.Contains(line, "NQN") && !strings.Contains(line, nqn) {
			foundNQN = false
		}
	}
	return ""
}

// logNVMeDiscoveryDiagnostics logs diagnostic information to help debug device discovery issues.
func (s *NodeService) logNVMeDiscoveryDiagnostics(ctx context.Context, nqn string) {
	klog.V(2).Infof("=== NVMe Device Discovery Diagnostics for NQN: %s ===", nqn)

	// Run nvme list-subsys
	subsysCtx, subsysCancel := context.WithTimeout(ctx, 5*time.Second)
	defer subsysCancel()
	subsysCmd := exec.CommandContext(subsysCtx, "nvme", "list-subsys")
	if output, err := subsysCmd.CombinedOutput(); err == nil {
		klog.V(2).Infof("nvme list-subsys output:\n%s", string(output))
	} else {
		klog.V(2).Infof("nvme list-subsys failed: %v", err)
	}

	// Run nvme list to show actual namespace devices
	listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
	defer listCancel()
	listCmd := exec.CommandContext(listCtx, "nvme", "list")
	if output, err := listCmd.CombinedOutput(); err == nil {
		klog.V(2).Infof("nvme list output:\n%s", string(output))
	} else {
		klog.V(2).Infof("nvme list failed: %v", err)
	}

	// List /sys/class/nvme contents and their NQNs
	if entries, err := os.ReadDir("/sys/class/nvme"); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		klog.V(2).Infof("/sys/class/nvme contents: %v", names)

		// Read subsysnqn for each controller
		nvmeSysDir := "/sys/class/nvme"
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "nvme") || strings.Contains(e.Name(), "-") {
				continue
			}
			if len(e.Name()) > 4 && strings.Contains(e.Name()[4:], "n") {
				continue // Skip namespace entries
			}
			nqnPath := nvmeSysDir + "/" + e.Name() + "/subsysnqn"
			//nolint:gosec // Reading NVMe subsystem info from standard sysfs path for diagnostics
			if data, readErr := os.ReadFile(nqnPath); readErr == nil {
				klog.V(2).Infof("  %s/subsysnqn = %q", e.Name(), strings.TrimSpace(string(data)))
			} else {
				klog.V(2).Infof("  %s/subsysnqn: error reading: %v", e.Name(), readErr)
			}
		}
	}

	// List /dev/nvme* devices
	devCtx, devCancel := context.WithTimeout(ctx, 3*time.Second)
	defer devCancel()
	devCmd := exec.CommandContext(devCtx, "ls", "-la", "/dev/nvme*")
	if output, err := devCmd.CombinedOutput(); err == nil {
		klog.V(2).Infof("/dev/nvme* devices:\n%s", string(output))
	}

	klog.V(2).Infof("=== End NVMe Diagnostics ===")
}

// isDeviceHealthy does a quick check if a device is functional (non-zero size).
// This is a single check, not a retry loop like verifyDeviceHealthy.
func (s *NodeService) isDeviceHealthy(ctx context.Context, devicePath string) bool {
	sizeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(sizeCtx, "blockdev", "--getsize64", devicePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	sizeStr := strings.TrimSpace(string(output))
	size, parseErr := strconv.ParseInt(sizeStr, 10, 64)
	return parseErr == nil && size > 0
}
