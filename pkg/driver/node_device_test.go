package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrepareFilesystemForMountFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		blkid     string
		fsck      string
		fsType    string
		wantError bool
		wantCode  codes.Code
	}{
		{name: "matching filesystem is repaired", blkid: "printf 'ext4\\n'", fsck: "exit 0", fsType: fsTypeExt4},
		{name: "failed repair prevents mount", blkid: "printf 'ext4\\n'", fsck: "exit 4", fsType: fsTypeExt4, wantError: true, wantCode: codes.FailedPrecondition},
		{name: "missing signature is rejected", blkid: "exit 2", fsck: "exit 99", fsType: fsTypeExt4, wantError: true, wantCode: codes.FailedPrecondition},
		{name: "probe failure is retryable", blkid: "printf 'I/O error\\n' >&2; exit 4", fsck: "exit 99", fsType: fsTypeExt4, wantError: true, wantCode: codes.Unavailable},
		{name: "conflicting filesystem is rejected", blkid: "printf 'xfs\\n'", fsck: "exit 99", fsType: fsTypeExt4, wantError: true, wantCode: codes.FailedPrecondition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			writeProbeCommand(t, binDir, "blkid", tt.blkid)
			writeProbeCommand(t, binDir, "e2fsck", tt.fsck)
			t.Setenv("PATH", binDir)

			err := prepareFilesystemForMount(context.Background(), "/dev/test", tt.fsType)
			if (err != nil) != tt.wantError {
				t.Errorf("prepareFilesystemForMount() error = %v, wantError %v", err, tt.wantError)
			}
			if status.Code(err) != tt.wantCode {
				t.Errorf("prepareFilesystemForMount() code = %v, want %v", status.Code(err), tt.wantCode)
			}
		})
	}
}

func writeProbeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s probe: %v", name, err)
	}
}
