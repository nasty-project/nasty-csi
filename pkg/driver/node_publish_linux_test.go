//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodePublishSingleWriterLive(t *testing.T) {
	if os.Getenv("NASTY_TEST_BIND_MOUNTS") != "1" {
		t.Skip("requires explicit privileged Linux mount test environment")
	}
	// Reproduce the CNI namespace mounts found on Kubernetes hosts.
	namespace := filepath.Join(t.TempDir(), "netns")
	if err := os.WriteFile(namespace, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("/proc/self/ns/net", namespace, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(namespace, 0) })
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "filesystem", true: "block-file"}[block], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			staging := filepath.Join(dir, "staging")
			if err := os.Mkdir(staging, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mount("tmpfs", staging, "tmpfs", 0, ""); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(staging, 0) })
			req := singleWriterRequest("volume", filepath.Join(dir, "first"), csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
			req.StagingTargetPath = staging
			if block {
				device := filepath.Join(staging, "device")
				if err := os.WriteFile(device, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				req.StagingTargetPath = filepath.Join(dir, "staged-device")
				if err := os.Symlink(device, req.StagingTargetPath); err != nil {
					t.Fatal(err)
				}
				req.VolumeContext["protocol"] = ProtocolISCSI
				req.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
			}
			service := NewNodeService("node", nil, false, nil, false, 5)
			first := req.TargetPath
			t.Cleanup(func() { _ = unix.Unmount(first, 0) })
			if _, err := service.NodePublishVolume(ctx, req); err != nil {
				t.Fatal(err)
			}
			service = NewNodeService("node", nil, false, nil, false, 5)
			if _, err := service.NodePublishVolume(ctx, req); err != nil {
				t.Fatalf("same-target retry after restart: %v", err)
			}
			req.TargetPath = filepath.Join(dir, "second")
			if _, err := service.NodePublishVolume(ctx, req); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("second target after restart: %v", err)
			}
			if _, err := service.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "volume", TargetPath: first}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(req.TargetPath, 0) })
			if _, err := service.NodePublishVolume(ctx, req); err != nil {
				t.Fatalf("publish after unpublish: %v", err)
			}
		})
	}
}
