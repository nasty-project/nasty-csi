//go:build linux

package mount

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBindMountRefs(t *testing.T) {
	const mounts = "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"2 1 0:5 / /dev rw - devtmpfs udev rw\n" +
		"3 1 0:42 /export/claim /staging rw - nfs server:/export/claim rw\n" +
		"4 1 0:42 /export/claim /target rw - nfs server:/export/claim rw\n" +
		"5 1 0:42 /export/other /unrelated rw - nfs server:/export/other rw\n" +
		"6 1 0:5 /sdb /block-target rw - devtmpfs udev rw\n" +
		"7 1 0:5 /sdc /other-block rw - devtmpfs udev rw\n" +
		"8 1 8:2 / /fs-staging rw - ext4 /dev/sdd rw\n" +
		"9 1 8:2 / /fs-target rw - ext4 /dev/sdd rw\n" +
		`10 1 0:43 /with\040space /space\040staging rw - nfs server:/export rw` + "\n" +
		`11 1 0:43 /with\040space /space\040target rw - nfs server:/export rw` + "\n"
	for _, tt := range []struct {
		name, source string
		want         []string
	}{
		{"NFS exact subdirectory", "/staging", []string{"/target"}},
		{"raw block", "/dev/sdb", []string{"/block-target"}},
		{"block filesystem", "/fs-staging", []string{"/fs-target"}},
		{"escaped paths", "/space staging", []string{"/space target"}},
		{"no publications", "/dev/sde", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindMountRefs(tt.source, strings.NewReader(mounts))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("refs = %v, error = %v; want %v", got, err, tt.want)
			}
		})
	}
}

// Run explicitly in a privileged Linux test container, never mount on the
// developer host or require privileges for the ordinary unit suite.
func TestGetBindMountRefsLive(t *testing.T) {
	if os.Getenv("NASTY_TEST_BIND_MOUNTS") != "1" {
		t.Skip("requires explicit privileged Linux mount test environment")
	}
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "filesystem", true: "file-symlink"}[block], func(t *testing.T) {
			dir := t.TempDir()
			staging := filepath.Join(dir, "stage with space")
			target := filepath.Join(dir, "target with space")
			for _, path := range []string{staging, target} {
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mount("tmpfs", staging, "tmpfs", 0, ""); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(staging, 0) })
			source := staging
			if block {
				source = filepath.Join(staging, "device")
				if err := os.WriteFile(source, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(target, "device")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(target, 0) })
			if block {
				alias := filepath.Join(dir, "staged-device")
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				source = alias
			}
			refs, err := GetBindMountRefs(context.Background(), source)
			if err != nil || !reflect.DeepEqual(refs, []string{target}) {
				t.Fatalf("refs = %v, error = %v", refs, err)
			}
			if unmountErr := unix.Unmount(target, 0); unmountErr != nil {
				t.Fatal(unmountErr)
			}
			refs, err = GetBindMountRefs(context.Background(), source)
			if err != nil || len(refs) != 0 {
				t.Fatalf("refs after unpublish = %v, error = %v", refs, err)
			}
		})
	}
}

func TestBindMountRefsFailsClosed(t *testing.T) {
	for _, table := range []string{
		"", "malformed\n", "1 0 invalid / / rw - ext4 /dev/sda rw\n",
		"1 0 8:1 relative / rw - ext4 /dev/sda rw\n",
		"1 0 8:1 / /elsewhere rw - ext4 /dev/sda rw\n",
	} {
		if _, err := bindMountRefs("/staging", strings.NewReader(table)); err == nil {
			t.Errorf("accepted unusable mount table %q", table)
		}
	}
}
