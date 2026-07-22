package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

// publishBlockVolume publishes a block volume by bind mounting the device file from staging to target.
func (s *NodeService) publishBlockVolume(ctx context.Context, stagingTargetPath, targetPath string, readonly bool) (*csi.NodePublishVolumeResponse, error) {
	klog.Infof("Publishing block device from %s to %s", stagingTargetPath, targetPath)

	// Verify staging path exists and is a device or symlink
	stagingInfo, err := os.Stat(stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Staging path %s not found: %v", stagingTargetPath, err)
	}

	// For block volumes, staging path should be a file (device node or symlink), not a directory
	if stagingInfo.IsDir() {
		return nil, status.Errorf(codes.Internal, "Staging path %s is a directory, expected block device or symlink", stagingTargetPath)
	}

	// For block volumes, CSI driver must create the parent directory and target file.
	// Create parent directory if it doesn't exist
	targetDir := filepath.Dir(targetPath)
	if mkdirErr := os.MkdirAll(targetDir, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create target directory %s: %v", targetDir, mkdirErr)
	}

	// Create target file atomically (O_EXCL ensures no race condition)
	//nolint:gosec // Target path from CSI request is validated by Kubernetes CSI framework
	file, fileErr := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL, 0o600)
	if fileErr != nil {
		if !os.IsExist(fileErr) {
			return nil, status.Errorf(codes.Internal, "Failed to create target file %s: %v", targetPath, fileErr)
		}
		// File already exists, which is fine
		klog.V(4).Infof("Target file %s already exists", targetPath)
	} else {
		klog.V(4).Infof("Created target file: %s", targetPath)
		if closeErr := file.Close(); closeErr != nil {
			klog.Warningf("Failed to close target file %s: %v", targetPath, closeErr)
		}
	}

	// Check if already mounted
	mounted, err := mount.IsDeviceMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if device is mounted: %v", err)
	}
	if mounted {
		klog.V(4).Infof("Target path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Bind mount the device from staging to target
	mountOptions := []string{mountOptBind}
	if readonly {
		mountOptions = append(mountOptions, "ro")
	}
	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Cleanup: remove target file on failure
		if removeErr := os.Remove(targetPath); removeErr != nil && !os.IsNotExist(removeErr) {
			klog.Warningf("Failed to remove target file %s during cleanup: %v", targetPath, removeErr)
		}
		return nil, status.Errorf(codes.Internal, "Failed to bind mount block device: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully bind mounted block device to %s", targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

// publishFilesystemVolume publishes a filesystem volume by bind mounting the staged directory to target.
func (s *NodeService) publishFilesystemVolume(ctx context.Context, stagingTargetPath, targetPath string, readonly bool) (*csi.NodePublishVolumeResponse, error) {
	klog.Infof("Publishing filesystem volume from %s to %s", stagingTargetPath, targetPath)

	// Verify staging path exists
	stagingInfo, err := os.Stat(stagingTargetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Staging path %s not found: %v", stagingTargetPath, err)
	}

	if !stagingInfo.IsDir() {
		return nil, status.Errorf(codes.Internal, "Staging path %s is not a directory", stagingTargetPath)
	}

	if mkdirErr := os.MkdirAll(targetPath, 0o750); mkdirErr != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create target directory %s: %v", targetPath, mkdirErr)
	}

	mounted, err := mount.IsMounted(ctx, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check if target path is mounted: %v", err)
	}
	if mounted {
		klog.V(4).Infof("Target path %s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	mountOptions := []string{mountOptBind}
	if readonly {
		mountOptions = append(mountOptions, "ro")
	}
	args := []string{"-o", mount.JoinMountOptions(mountOptions), stagingTargetPath, targetPath}

	klog.V(4).Infof("Executing bind mount command: mount %v", args)
	mountCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(mountCtx, "mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to bind mount filesystem: %v, output: %s", err, string(output))
	}

	klog.Infof("Successfully bind mounted filesystem to %s", targetPath)
	return &csi.NodePublishVolumeResponse{}, nil
}

// stageBlockDevice stages a raw block device by creating a symlink at staging path.
func (s *NodeService) stageBlockDevice(devicePath, stagingTargetPath string) (*csi.NodeStageVolumeResponse, error) {
	klog.Infof("Staging block device %s to %s", devicePath, stagingTargetPath)

	if _, err := os.Stat(devicePath); err != nil {
		return nil, status.Errorf(codes.Internal, "Device path %s not found: %v", devicePath, err)
	}

	if _, err := os.Stat(stagingTargetPath); err == nil {
		klog.V(4).Infof("Staging path %s already exists", stagingTargetPath)
		targetDevice, evalErr := filepath.EvalSymlinks(stagingTargetPath)
		if evalErr == nil && targetDevice == devicePath {
			klog.V(4).Infof("Staging path already points to correct device")
			return &csi.NodeStageVolumeResponse{}, nil
		}
		klog.Warningf("Removing incorrect staging path: %s", stagingTargetPath)
		if removeErr := os.Remove(stagingTargetPath); removeErr != nil {
			return nil, status.Errorf(codes.Internal, "Failed to remove incorrect staging path: %v", removeErr)
		}
	}

	stagingDir := filepath.Dir(stagingTargetPath)
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create staging directory: %v", err)
	}

	if err := os.Symlink(devicePath, stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to create symlink from %s to %s: %v", stagingTargetPath, devicePath, err)
	}

	klog.Infof("Successfully staged block device at %s -> %s", stagingTargetPath, devicePath)
	return &csi.NodeStageVolumeResponse{}, nil
}
