package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func singleWriterRequest(volumeID, target string, mode csi.VolumeCapability_AccessMode_Mode) *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId: volumeID, TargetPath: target,
		VolumeContext: map[string]string{"protocol": ProtocolNFS},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode},
		},
	}
}

func TestNodePublishSingleWriterLifecycle(t *testing.T) {
	ctx := context.Background()
	service := NewNodeService("node", nil, true, nil, false, 5)
	dir := t.TempDir()
	first := singleWriterRequest("volume", filepath.Join(dir, "first"), csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
	second := singleWriterRequest("volume", filepath.Join(dir, "second"), csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
	for _, req := range []*csi.NodePublishVolumeRequest{first, first} {
		if _, err := service.NodePublishVolume(ctx, req); err != nil {
			t.Fatalf("initial publish or retry: %v", err)
		}
	}
	second.Readonly = true
	if _, err := service.NodePublishVolume(ctx, second); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second target: %v", err)
	}
	if _, err := os.Stat(second.TargetPath); !os.IsNotExist(err) {
		t.Fatalf("rejected publish must not create its target: %v", err)
	}
	other := singleWriterRequest("other-volume", second.TargetPath, csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
	if _, err := service.NodePublishVolume(ctx, other); err != nil {
		t.Fatalf("independent volume: %v", err)
	}
	// Unpublishing an unrelated target must not release the first publication.
	if _, err := service.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "volume", TargetPath: filepath.Join(dir, "absent")}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NodePublishVolume(ctx, second); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unrelated unpublish released the volume: %v", err)
	}
	for range 2 {
		if _, err := service.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "volume", TargetPath: first.TargetPath}); err != nil {
			t.Fatal(err)
		}
	}
	second.TargetPath = filepath.Join(dir, "replacement")
	if _, err := service.NodePublishVolume(ctx, second); err != nil {
		t.Fatalf("publish after unpublish: %v", err)
	}
}

func TestNodePublishMultipleWriterTargets(t *testing.T) {
	for _, mode := range []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			service := NewNodeService("node", nil, true, nil, false, 5)
			dir := t.TempDir()
			for _, target := range []string{"first", "second"} {
				if _, err := service.NodePublishVolume(context.Background(), singleWriterRequest("volume", filepath.Join(dir, target), mode)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNodePublishSingleWriterConcurrent(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	dir := t.TempDir()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, target := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.NodePublishVolume(context.Background(), singleWriterRequest("volume", filepath.Join(dir, target), csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[codes.Code]int{}
	for err := range results {
		counts[status.Code(err)]++
	}
	if counts[codes.OK] != 1 || counts[codes.FailedPrecondition] != 1 {
		t.Fatalf("concurrent publish results: %v", counts)
	}
}

func TestNodePublishFailureDoesNotReserveSingleWriter(t *testing.T) {
	service := NewNodeService("node", nil, true, nil, false, 5)
	dir := t.TempDir()
	req := singleWriterRequest("volume", filepath.Join(dir, "target"), csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
	req.VolumeContext["protocol"] = ProtocolNVMeOF
	if _, err := service.NodePublishVolume(context.Background(), req); err == nil {
		t.Fatal("expected missing staging path failure")
	}
	req.VolumeContext["protocol"] = ProtocolNFS
	req.TargetPath = filepath.Join(dir, "valid")
	if _, err := service.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("failed publish reserved the volume: %v", err)
	}
}

func TestSingleWriterHostMountCheck(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		for _, block := range []bool{false, true} {
			if block && (protocol == ProtocolNFS || protocol == ProtocolSMB) {
				continue
			}
			t.Run(protocol+"/"+map[bool]string{false: "filesystem", true: "block"}[block], func(t *testing.T) {
				// A fresh service has no publication state: mount refs still reject
				// a second target after restart, before any mount is attempted.
				service := NewNodeService("node", nil, false, nil, false, 5)
				req := singleWriterRequest("volume", "/target", csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER)
				req.StagingTargetPath = "/staging"
				req.VolumeContext["protocol"] = protocol
				if block {
					req.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
				}
				service.getMountRefsFn = func(_ context.Context, source string) ([]string, error) {
					if source != req.StagingTargetPath {
						t.Fatalf("source = %q", source)
					}
					return []string{"/existing-target"}, nil
				}
				if _, err := service.NodePublishVolume(context.Background(), req); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("second target: %v", err)
				}
				service.getMountRefsFn = func(context.Context, string) ([]string, error) { return []string{req.TargetPath}, nil }
				if err := service.checkSingleWriterPublication(context.Background(), req); err != nil {
					t.Fatalf("same-target retry: %v", err)
				}
				service.getMountRefsFn = func(context.Context, string) ([]string, error) { return nil, errors.New("unreadable mount table") }
				if _, err := service.NodePublishVolume(context.Background(), req); status.Code(err) != codes.Internal {
					t.Fatalf("mount check must fail closed: %v", err)
				}
			})
		}
	}
}
