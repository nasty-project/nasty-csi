package driver

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nasty-project/nasty-csi/pkg/cmdexec"
)

func TestISCSIAdmNeedsDBLock(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"-m", "discovery", "-t", "sendtargets", "-p", "a:3260"}, true},
		{[]string{"--mode", "discoverydb", "-t", "st"}, true},
		{[]string{"--mode=node", "-P", "0"}, true},
		// A login holds the idbm lock only while it reads the record, then waits on the
		// network; gating it would let one slow portal block every stage on the node.
		{[]string{"-m", "node", "-T", "iqn", "-p", "a", "--login"}, false},
		{[]string{"-m", "node", "-T", "iqn", "-p", "a", "-l"}, false},
		{[]string{"-m", "iface"}, true},
		{[]string{"-m", "session"}, false},
		{[]string{"-m", "session", "-P", "3"}, false},
		{[]string{"-m", "session", "-r", "4", "--logout"}, false},
		{[]string{"-m", "node", "-T", "iqn", "--logout"}, false},
		{[]string{"--version"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := iscsiadmNeedsDBLock(tt.args); got != tt.want {
			t.Errorf("iscsiadmNeedsDBLock(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// Concurrent NodeStage calls must not run iscsiadm against the node database in parallel:
// they only queue on open-iscsi's lock file and get killed for waiting (#336).
func TestRunISCSIAdmSerializesDatabaseOperations(t *testing.T) {
	runner := &iscsiadmRunner{}
	var active, maxActive atomic.Int32
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return nil, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := runner.run(context.Background(), time.Second, "-m", "node", "-P", "0"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent database iscsiadm calls = %d, want 1", got)
	}
}

func TestRunISCSIAdmSessionQueriesBypassTheGate(t *testing.T) {
	runner := &iscsiadmRunner{}
	release := make(chan struct{})
	entered := make(chan struct{})
	runner.runFn = func(_ context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "discovery") {
			close(entered)
			<-release
		}
		return nil, nil
	}
	go func() {
		_, _ = runner.run(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	}()
	<-entered
	done := make(chan error, 1)
	go func() {
		_, err := runner.run(context.Background(), time.Second, "-m", "session", "-P", "3")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session query waited behind a database operation")
	}
	close(release)
}

// The per-call budget must start once the caller owns the gate, not while it queues.
func TestRunISCSIAdmTimeoutStartsAfterAcquiringGate(t *testing.T) {
	runner := &iscsiadmRunner{}
	release := make(chan struct{})
	entered := make(chan struct{})
	var remaining time.Duration
	runner.runFn = func(ctx context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "discovery") {
			close(entered)
			<-release
			return nil, nil
		}
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)
		return nil, nil
	}
	go func() {
		_, _ = runner.run(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	}()
	<-entered
	go func() { time.Sleep(300 * time.Millisecond); close(release) }()
	if _, err := runner.run(context.Background(), 200*time.Millisecond, "-m", "node", "-P", "0"); err != nil {
		t.Fatal(err)
	}
	if remaining < 100*time.Millisecond {
		t.Fatalf("budget left when the command started = %v; queueing consumed the per-call timeout", remaining)
	}
}

// A command left running past its deadline still holds open-iscsi's lock: the next caller
// must wait for it to exit instead of racing it.
func TestRunISCSIAdmHoldsGateUntilLeftRunningCommandExits(t *testing.T) {
	runner := &iscsiadmRunner{}
	exited := make(chan struct{})
	calls := 0
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, &cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
		}
		return []byte("ok"), nil
	}
	_, err := runner.run(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	if !errors.Is(err, ErrISCSIAdmStillRunning) || !cmdexec.IsStillRunning(err) {
		t.Fatalf("first call error = %v, want ErrISCSIAdmStillRunning", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runner.run(ctx, time.Second, "-m", "node", "-P", "0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second call while the first still runs = %v, want to time out waiting for the gate", err)
	}

	close(exited)
	if out, err := runner.run(context.Background(), time.Second, "-m", "node", "-P", "0"); err != nil || string(out) != "ok" {
		t.Fatalf("call after the first exited = %q, %v", out, err)
	}
}

func TestRunISCSIAdmRetriesOnceAfterClearingStaleLock(t *testing.T) {
	runner := &iscsiadmRunner{}
	var calls, clears int
	runner.clearStaleFn = func() bool {
		clears++
		return clears >= 2 // the pre-check finds nothing; the post-failure check clears it
	}
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), errors.New("exit status 6")
		}
		return []byte("records"), nil
	}
	out, err := runner.run(context.Background(), time.Second, "-m", "node", "-P", "0")
	if err != nil || string(out) != "records" || calls != 2 {
		t.Fatalf("runISCSIAdm() = %q, %v after %d calls; want a single retry after the lock was cleared", out, err, calls)
	}
}

func TestRunISCSIAdmReportsHeldDatabaseLock(t *testing.T) {
	runner := &iscsiadmRunner{}
	calls := 0
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), errors.New("exit status 6")
	}
	_, err := runner.run(context.Background(), time.Second, "-m", "node", "-P", "0")
	if !errors.Is(err, ErrISCSINodeDBLocked) {
		t.Fatalf("runISCSIAdm() error = %v, want ErrISCSINodeDBLocked", err)
	}
	if calls != 1 {
		t.Fatalf("iscsiadm calls = %d; nothing was cleared, so there must be no retry", calls)
	}
}

func TestRunISCSIAdmGateWaitHonoursContext(t *testing.T) {
	runner := &iscsiadmRunner{}
	release := make(chan struct{})
	entered := make(chan struct{})
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		close(entered)
		<-release
		return nil, nil
	}
	go func() { _, _ = runner.run(context.Background(), time.Second, "-m", "node", "-P", "0") }()
	<-entered
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.run(ctx, time.Second, "-m", "node", "-P", "0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runISCSIAdm() with canceled ctx = %v, want context.Canceled", err)
	}
}

// A command left running can already have printed open-iscsi's lock timeout. Starting a
// second iscsiadm then would run two at once and lose track of the first (review finding).
func TestRunISCSIAdmDoesNotRetryWhileFirstAttemptStillRuns(t *testing.T) {
	runner := &iscsiadmRunner{clearStaleFn: func() bool { return true }}
	exited := make(chan struct{})
	calls := 0
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		calls++
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"),
			&cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
	}
	_, err := runner.run(context.Background(), time.Second, "-m", "discovery", "-t", "st", "-p", "a")
	if !errors.Is(err, ErrISCSIAdmStillRunning) {
		t.Fatalf("runISCSIAdm() = %v, want ErrISCSIAdmStillRunning", err)
	}
	if calls != 1 {
		t.Fatalf("iscsiadm started %d times while the first was still running", calls)
	}
	close(exited)
}

// One slow or unreachable portal must not stop every other volume on the node from staging.
func TestRunISCSIAdmLoginDoesNotHoldTheGate(t *testing.T) {
	runner := &iscsiadmRunner{}
	release := make(chan struct{})
	entered := make(chan struct{})
	runner.runFn = func(_ context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "--login") {
			close(entered)
			<-release
		}
		return nil, nil
	}
	go func() {
		_, _ = runner.run(context.Background(), time.Second, "-m", "node", "-T", "iqn", "-p", "a", "--login")
	}()
	<-entered
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := runner.run(ctx, time.Second, "-m", "node", "-P", "0"); err != nil {
		t.Fatalf("node listing waited behind a slow login: %v", err)
	}
}

// A login outside the gate can still meet a held lock; it must be named as such.
func TestRunISCSIAdmLoginReportsHeldLock(t *testing.T) {
	runner := &iscsiadmRunner{}
	runner.runFn = func(context.Context, ...string) ([]byte, error) {
		return []byte("iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"), errors.New("exit status 6")
	}
	_, err := runner.run(context.Background(), time.Second, "-m", "node", "-T", "iqn", "-p", "a", "--login")
	if !errors.Is(err, ErrISCSINodeDBLocked) {
		t.Fatalf("runISCSIAdm(login) = %v, want ErrISCSINodeDBLocked", err)
	}
}
