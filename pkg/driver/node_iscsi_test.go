package driver

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

func TestFindDiscoveredPortal(t *testing.T) {
	tests := []struct {
		name   string
		output string
		iqn    string
		want   string
	}{
		{
			name:   "single portal v4",
			output: "10.0.0.22:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:    "iqn.2137-04.storage.nasty:vol1",
			want:   "10.0.0.22:3260",
		},
		{
			name: "multi-portal returns first match",
			// NASty dual-stack default: v4 wildcard listed first.
			output: "0.0.0.0:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"[::]:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: "0.0.0.0:3260",
		},
		{
			name: "ignores other IQNs",
			output: "10.0.0.1:3260,1 iqn.2137-04.storage.nasty:other\n" +
				"10.0.0.22:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: "10.0.0.22:3260",
		},
		{
			name:   "no match returns empty",
			output: "10.0.0.1:3260,1 iqn.2137-04.storage.nasty:other",
			iqn:    "iqn.2137-04.storage.nasty:missing",
			want:   "",
		},
		{
			name:   "empty output returns empty",
			output: "",
			iqn:    "iqn.2137-04.storage.nasty:vol1",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findDiscoveredPortal(tt.output, tt.iqn)
			if got != tt.want {
				t.Errorf("findDiscoveredPortal(%q, %q) = %q, want %q", tt.output, tt.iqn, got, tt.want)
			}
		})
	}
}

func TestFindAllDiscoveredPortals(t *testing.T) {
	tests := []struct {
		name   string
		output string
		iqn    string
		want   []string
	}{
		{
			name:   "single portal",
			output: "10.0.0.22:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:    "iqn.2137-04.storage.nasty:vol1",
			want:   []string{"10.0.0.22:3260"},
		},
		{
			name: "dual-stack: v4 and v6 wildcards",
			// The exact output shape NASty produces today after the
			// dual-stack default landed — the pruning logic must see
			// both so it can delete the unreachable one.
			output: "0.0.0.0:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"[::]:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: []string{"0.0.0.0:3260", "[::]:3260"},
		},
		{
			name: "three portals (v4 + v6 + Tailscale)",
			// What operators get when they manually add a Tailscale
			// portal alongside the dual-stack defaults.
			output: "0.0.0.0:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"[::]:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"100.64.0.5:3260,1 iqn.2137-04.storage.nasty:vol1",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: []string{"0.0.0.0:3260", "[::]:3260", "100.64.0.5:3260"},
		},
		{
			name: "skips lines for other IQNs",
			output: "10.0.0.1:3260,1 iqn.2137-04.storage.nasty:other\n" +
				"10.0.0.22:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"[::]:3260,1 iqn.2137-04.storage.nasty:other",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: []string{"10.0.0.22:3260"},
		},
		{
			name:   "no match returns nil",
			output: "10.0.0.1:3260,1 iqn.2137-04.storage.nasty:other",
			iqn:    "iqn.2137-04.storage.nasty:missing",
			want:   nil,
		},
		{
			name:   "empty output returns nil",
			output: "",
			iqn:    "iqn.2137-04.storage.nasty:vol1",
			want:   nil,
		},
		{
			name: "tolerates blank lines and whitespace",
			output: "\n" +
				"  10.0.0.22:3260,1 iqn.2137-04.storage.nasty:vol1\n" +
				"\n" +
				"  [::]:3260,1 iqn.2137-04.storage.nasty:vol1\n",
			iqn:  "iqn.2137-04.storage.nasty:vol1",
			want: []string{"10.0.0.22:3260", "[::]:3260"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findAllDiscoveredPortals(tt.output, tt.iqn)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findAllDiscoveredPortals(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStageISCSIDeviceDoesNotRescanFreshLUN(t *testing.T) {
	binDir := t.TempDir()
	rescanSentinel := filepath.Join(t.TempDir(), "rescan-called")
	writeProbeCommand(t, binDir, "blockdev", `
if [ "$1" = "--flushbufs" ]; then
	printf called > "$RESCAN_SENTINEL"
	exit 0
fi
printf '1073741824\n'
`)
	writeProbeCommand(t, binDir, "udevadm", `printf called > "$RESCAN_SENTINEL"`)
	writeProbeCommand(t, binDir, "blkid", `printf 'ext4\n'`)
	writeProbeCommand(t, binDir, "e2fsck", "exit 0")
	writeProbeCommand(t, binDir, "findmnt", "exit 1")
	writeProbeCommand(t, binDir, "mount", "exit 0")
	t.Setenv("PATH", binDir)
	t.Setenv("RESCAN_SENTINEL", rescanSentinel)

	devicePath := filepath.Join(t.TempDir(), "device")
	if err := os.WriteFile(devicePath, nil, 0o600); err != nil {
		t.Fatalf("create fake device: %v", err)
	}
	capability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsTypeExt4}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}

	service := &NodeService{}
	if _, err := service.stageISCSIDevice(context.Background(), "volume", devicePath, filepath.Join(t.TempDir(), "stage"), capability, false, map[string]string{
		"expectedCapacity": "1073741824",
	}); err != nil {
		t.Fatalf("stageISCSIDevice() error = %v", err)
	}
	if _, err := os.Stat(rescanSentinel); !os.IsNotExist(err) {
		t.Fatalf("stageISCSIDevice() rescanned fresh LUN; stat error = %v", err)
	}
}
