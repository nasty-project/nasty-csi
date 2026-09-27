//go:build linux

// Package mount provides Linux-specific mount utilities for CSI driver operations.
package mount

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

var errMalformedMountInfo = errors.New("malformed host mount information")

// IsMounted checks if a path is mounted.
func IsMounted(ctx context.Context, targetPath string) (bool, error) {
	// Use findmnt to check if path is mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "TARGET", "-n", "-l", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero exit code if path is not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// IsDeviceMounted checks if a device path is mounted (for block devices).
func IsDeviceMounted(ctx context.Context, targetPath string) (bool, error) {
	// For block devices, check if it's bind mounted with timeout
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "findmnt", "-o", "SOURCE", "-n", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero if not found
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check mount: %w", err)
	}

	// If we got output, the path is mounted
	return len(output) > 0, nil
}

// IsSourceMounted checks the host mount namespace by device number, so aliases
// such as /dev/disk/by-path and /dev/nvme0n1 refer to the same mounted source.
// The node DaemonSet uses hostPID, making /proc/1/mountinfo the host's table.
// Any unreadable or malformed mount table fails closed before filesystem repair.
func IsSourceMounted(ctx context.Context, sourcePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return false, fmt.Errorf("failed to stat source device: %w", err)
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return false, fmt.Errorf("%w: %s is not a block device", errMalformedMountInfo, sourcePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%w: missing device number for %s", errMalformedMountInfo, sourcePath)
	}
	file, err := os.Open("/proc/1/mountinfo")
	if err != nil {
		return false, fmt.Errorf("failed to read host mount information: %w", err)
	}
	mounted, parseErr := isDeviceInMountInfo(unix.Major(stat.Rdev), unix.Minor(stat.Rdev), file)
	closeErr := file.Close()
	if parseErr != nil {
		return false, parseErr
	}
	if closeErr != nil {
		return false, fmt.Errorf("failed to close host mount information: %w", closeErr)
	}
	return mounted, nil
}

func isDeviceInMountInfo(deviceMajor, deviceMinor uint32, reader io.Reader) (bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.Index(line, " - ")
		if separator < 0 {
			return false, fmt.Errorf("%w: missing separator", errMalformedMountInfo)
		}
		fields := strings.Fields(line[:separator])
		if len(fields) < 6 {
			return false, fmt.Errorf("%w: missing mount fields", errMalformedMountInfo)
		}
		deviceNumbers := strings.Split(fields[2], ":")
		if len(deviceNumbers) != 2 {
			return false, fmt.Errorf("%w: invalid device number %q", errMalformedMountInfo, fields[2])
		}
		major, majorErr := strconv.ParseUint(deviceNumbers[0], 10, 32)
		minor, minorErr := strconv.ParseUint(deviceNumbers[1], 10, 32)
		if majorErr != nil || minorErr != nil {
			return false, fmt.Errorf("%w: invalid device number %q", errMalformedMountInfo, fields[2])
		}
		if major == uint64(deviceMajor) && minor == uint64(deviceMinor) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("failed to scan host mount information: %w", err)
	}
	return false, nil
}

// Unmount unmounts a path.
func Unmount(ctx context.Context, targetPath string) error {
	// Try normal unmount first (10s — enough for healthy devices)
	umountCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(umountCtx, "umount", targetPath)
	output, normalErr := cmd.CombinedOutput()
	if normalErr == nil {
		return nil
	}

	klog.Warningf("Normal unmount failed for %s: %v (output: %s) — trying lazy unmount",
		targetPath, normalErr, strings.TrimSpace(string(output)))

	// Lazy unmount (-l) detaches the filesystem immediately and cleans up
	// references in the background. This prevents umount from blocking
	// indefinitely on transport-offline iSCSI/NVMe-oF devices.
	lazyCtx, lazyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer lazyCancel()
	lazyCmd := exec.CommandContext(lazyCtx, "umount", "-l", targetPath)
	lazyOutput, lazyErr := lazyCmd.CombinedOutput()
	if lazyErr != nil {
		return fmt.Errorf("failed to unmount: %w (normal unmount: %s, output: %s)", lazyErr, normalErr.Error(), string(lazyOutput))
	}

	klog.V(4).Infof("Lazy unmount succeeded for %s", targetPath)
	return nil
}
