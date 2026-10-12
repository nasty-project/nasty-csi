package mount

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPathInMountInfo(t *testing.T) {
	const root = "36 25 0:1 / / rw - rootfs none rw\n"
	for _, tt := range []struct {
		name, target, table string
		want, wantErr       bool
	}{
		{"root", "/", root, true, false},
		{"not exact mountpoint", "/volume", root, false, false},
		{"missing path", "/does/not/exist", root, false, false},
		{"escaped space", "/staging/my volume", root + "37 36 0:2 / /staging/my\\040volume rw - nfs server:/share rw\n", true, false},
		{"escaped backslash", "/staging/a\\b", root + "37 36 0:2 / /staging/a\\134b rw - cifs //server/share rw\n", true, false},
		{"stacked mounts", "/", root + "37 36 0:2 / / rw - nfs server:/share rw\n", true, false},
		{"clean target", "/staging/../", root, true, false},
		{"empty table", "/", "", false, true},
		{"empty target", "", root, false, true},
		{"malformed after match", "/", root + "broken\n", false, true},
		{"missing fields", "/", "36 25 - nfs server:/share rw\n", false, true},
		{"bad escape", "/", "36 25 0:1 / /bad\\999 rw - nfs server:/share rw\n", false, true},
		{"truncated escape", "/", "36 25 0:1 / /bad\\04 rw - nfs server:/share rw\n", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isPathInMountInfo(tt.target, strings.NewReader(tt.table))
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %v, %v; want %v, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

type mountReadFailure struct{}

func (mountReadFailure) Read([]byte) (int, error) { return 0, errors.New("mount table unavailable") }

func TestPathInMountInfoReadFailure(t *testing.T) {
	reader := io.MultiReader(strings.NewReader("36 25 0:1 / / rw - rootfs none rw\n"), mountReadFailure{})
	if mounted, err := isPathInMountInfo("/", reader); mounted || err == nil {
		t.Fatalf("read failure after match must fail closed: %v, %v", mounted, err)
	}
}
