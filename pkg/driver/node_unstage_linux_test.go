//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Use ordinary temporary directories, never real mounts or live shares.
func TestUnstagePreservesUnexpectedContents(t *testing.T) {
	s := &NodeService{}
	for _, protocol := range []string{"nfs", "smb"} {
		t.Run(protocol, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "must-survive")
			if err := os.WriteFile(file, []byte("live data"), 0600); err != nil {
				t.Fatal(err)
			}
			req := &csi.NodeUnstageVolumeRequest{VolumeId: "test", StagingTargetPath: dir}
			var err error
			if protocol == "nfs" {
				_, err = s.unstageNFSVolume(context.Background(), req)
			} else {
				_, err = s.unstageSMBVolume(context.Background(), req)
			}
			if status.Code(err) != codes.Internal {
				t.Fatalf("nonempty staging directory must trigger retry: %v", err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "live data" {
				t.Fatalf("unstage removed or changed data: %q, %v", data, err)
			}
		})
	}
}

func TestUnstageEmptyAndMissingDirectories(t *testing.T) {
	s := &NodeService{}
	for _, protocol := range []string{"nfs", "smb"} {
		t.Run(protocol, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "stage")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			req := &csi.NodeUnstageVolumeRequest{VolumeId: "test", StagingTargetPath: dir}
			for range 2 {
				var err error
				if protocol == "nfs" {
					_, err = s.unstageNFSVolume(context.Background(), req)
				} else {
					_, err = s.unstageSMBVolume(context.Background(), req)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("empty staging directory not removed: %v", err)
			}
		})
	}
}
