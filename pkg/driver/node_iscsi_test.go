package driver

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestParseISCSISessionInfoUsesAttachedDevice(t *testing.T) {
	output := `
Target: iqn.2137-04.storage.nasty:other (non-flash)
    Current Portal: 10.0.0.10:3260,1
    Attached scsi disk sda State: running
Target: iqn.2137-04.storage.nasty:restored (non-flash)
    Current Portal: 10.0.0.20:3261,1
    Attached scsi disk sdz State: running
`
	iqn, portal := parseISCSISessionInfo(output, "sdz")
	if iqn != "iqn.2137-04.storage.nasty:restored" {
		t.Fatalf("IQN = %q, want restored target", iqn)
	}
	if portal != "10.0.0.20:3261" {
		t.Fatalf("portal = %q, want 10.0.0.20:3261", portal)
	}
}

func TestUnstageISCSIVolumeUsesDiscoveredIQN(t *testing.T) {
	binDir := t.TempDir()
	commandLog := filepath.Join(t.TempDir(), "iscsi-commands")
	commandBody := `
case "$*" in
  *"-m session -P 3"*)
    printf '%s\n' 'Target: iqn.2137-04.storage.nasty:actual' \
      '    Current Portal: 10.0.0.20:3260,1' \
      '    Attached scsi disk sdtest State: running'
    ;;
  *) printf '%s\n' "$*" >> "$ISCSI_COMMAND_LOG" ;;
esac
`
	writeProbeCommand(t, binDir, "iscsiadm", commandBody)
	writeProbeCommand(t, binDir, "nsenter", commandBody)
	writeProbeCommand(t, binDir, "findmnt", "exit 1")
	writeProbeCommand(t, binDir, "mount", "exit 0")
	t.Setenv("PATH", binDir)
	t.Setenv("ISCSI_COMMAND_LOG", commandLog)

	stagingPath := filepath.Join(t.TempDir(), "staging")
	if err := os.Symlink("/dev/sdtest", stagingPath); err != nil {
		t.Fatalf("create staging symlink: %v", err)
	}
	req := &csi.NodeUnstageVolumeRequest{
		VolumeId:          "first/pvc-does-not-identify-the-target",
		StagingTargetPath: stagingPath,
	}

	service := &NodeService{}
	if _, err := service.NodeUnstageVolume(context.Background(), req); err != nil {
		t.Fatalf("NodeUnstageVolume() error = %v", err)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("read iSCSI command log: %v", err)
	}
	got := string(commands)
	if !strings.Contains(got, "-T iqn.2137-04.storage.nasty:actual --logout") {
		t.Fatalf("logout did not use discovered IQN: %s", got)
	}
	if strings.Contains(got, req.VolumeId) {
		t.Fatalf("logout synthesized identity from volume ID: %s", got)
	}
}
