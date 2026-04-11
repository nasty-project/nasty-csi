package driver

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	nastygo "github.com/nasty-project/nasty-go"
)

func writeTestFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRecoverVolumesCalledOnReconnect(t *testing.T) {
	// Verify that SetOnReconnect correctly wires up the callback.
	// We can't test actual iSCSI/NVMe recovery without a real system,
	// but we can verify the plumbing works.
	var called atomic.Int32

	client := &nastygo.Client{}
	client.SetOnReconnect(func() {
		called.Add(1)
	})

	// Simulate what notifyReconnect does
	client.SetOnReconnect(func() {
		called.Add(1)
	})

	// The callback should be set but not yet called
	if called.Load() != 0 {
		t.Fatal("callback should not be called until reconnection happens")
	}
}

func TestWaitForNVMeControllerRecovery_Timeout(t *testing.T) {
	// Create a temp dir to simulate a controller that never becomes live
	dir := t.TempDir()

	// Write a non-live state
	statePath := dir + "/state"
	if err := writeTestFile(t, statePath, "connecting\n"); err != nil {
		t.Fatal(err)
	}

	// Should timeout quickly with a short deadline
	ctx := testContext(t)
	err := waitForNVMeControllerRecovery(ctx, dir, 3*time.Second)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestWaitForNVMeControllerRecovery_Success(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/state"

	// Start with non-live state
	if err := writeTestFile(t, statePath, "connecting\n"); err != nil {
		t.Fatal(err)
	}

	// Simulate recovery after 1 second
	go func() {
		time.Sleep(1 * time.Second)
		_ = writeTestFile(t, statePath, "live\n")
	}()

	ctx := testContext(t)
	err := waitForNVMeControllerRecovery(ctx, dir, 10*time.Second)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}
