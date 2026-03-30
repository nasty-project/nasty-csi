//go:build linux

// Package mount provides Linux-specific mount utilities for CSI driver operations.
package mount

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

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
		return fmt.Errorf("failed to unmount (normal: %v, lazy: %w, output: %s)", normalErr, lazyErr, string(lazyOutput))
	}

	klog.V(4).Infof("Lazy unmount succeeded for %s", targetPath)
	return nil
}
