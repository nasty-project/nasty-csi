package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestForceDeviceRescanDoesNotSyncUnrelatedDevices(t *testing.T) {
	binDir := t.TempDir()
	syncSentinel := filepath.Join(t.TempDir(), "sync-called")
	writeProbeCommand(t, binDir, "sync", `printf called > "$SYNC_SENTINEL"`)
	writeProbeCommand(t, binDir, "blockdev", "exit 0")
	writeProbeCommand(t, binDir, "udevadm", "exit 0")
	t.Setenv("PATH", binDir)
	t.Setenv("SYNC_SENTINEL", syncSentinel)

	if err := forceDeviceRescan(context.Background(), "/dev/test"); err != nil {
		t.Fatalf("forceDeviceRescan() error = %v", err)
	}
	if _, err := os.Stat(syncSentinel); !os.IsNotExist(err) {
		t.Fatalf("forceDeviceRescan() ran global sync; stat error = %v", err)
	}
}

func TestLogDeviceInfoUsesLowLevelBlkidProbe(t *testing.T) {
	binDir := t.TempDir()
	blkidArgs := filepath.Join(t.TempDir(), "blkid-args")
	writeProbeCommand(t, binDir, "blockdev", `printf '1073741824\n'`)
	writeProbeCommand(t, binDir, "blkid", `printf '%s\n' "$*" >> "$BLKID_ARGS"`)
	t.Setenv("PATH", binDir)
	t.Setenv("BLKID_ARGS", blkidArgs)

	devicePath := filepath.Join(t.TempDir(), "device")
	if err := os.WriteFile(devicePath, nil, 0o600); err != nil {
		t.Fatalf("create fake device: %v", err)
	}

	service := &NodeService{}
	service.logDeviceInfo(context.Background(), devicePath)

	args, err := os.ReadFile(blkidArgs)
	if err != nil {
		t.Fatalf("read blkid arguments: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 2 {
		t.Fatalf("blkid calls = %d, want 2: %q", len(lines), string(args))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "-p ") {
			t.Errorf("blkid call did not bypass the global cache: %q", line)
		}
	}
}

func writeProbeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write %s probe: %v", name, err)
	}
}
