//go:build linux

package mount

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestIsDeviceInMountInfo(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    bool
		wantErr bool
	}{
		{name: "same device under different source name", data: "36 25 259:2 / /mnt/volume rw - ext4 /dev/disk/by-id/alias rw\n", want: true},
		{name: "another device", data: "36 25 8:1 / /mnt/other rw - ext4 /dev/sda1 rw\n"},
		{name: "missing separator", data: "36 25 259:2 / /mnt/volume rw\n", wantErr: true},
		{name: "missing fields", data: "36 25 259:2 - ext4 /dev/test rw\n", wantErr: true},
		{name: "invalid major", data: "36 25 bad:2 / /mnt/volume rw - ext4 /dev/test rw\n", wantErr: true},
		{name: "invalid minor", data: "36 25 259:bad / /mnt/volume rw - ext4 /dev/test rw\n", wantErr: true},
		{name: "invalid device pair", data: "36 25 259 / /mnt/volume rw - ext4 /dev/test rw\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mounted, err := isDeviceInMountInfo(259, 2, strings.NewReader(tt.data))
			if mounted != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("isDeviceInMountInfo() = %v, %v; want %v, error=%v", mounted, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestIsDeviceInMountInfoReadFailure(t *testing.T) {
	if _, err := isDeviceInMountInfo(259, 2, failedReader{}); err == nil {
		t.Fatal("mount-table read failure must not report unmounted")
	}
}

func TestIsSourceMountedRefusesInvalidDevice(t *testing.T) {
	if _, err := IsSourceMounted(context.Background(), "/dev/null"); err == nil {
		t.Fatal("character device must not be treated as an unmounted block device")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := IsSourceMounted(ctx, "/dev/null"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mount check = %v, want context canceled", err)
	}
}
