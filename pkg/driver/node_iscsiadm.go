package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nasty-project/nasty-csi/pkg/cmdexec"
	"github.com/nasty-project/nasty-csi/pkg/metrics"
	"k8s.io/klog/v2"
)

// All iscsiadm calls go through runISCSIAdm. Two rules come from open-iscsi's node-database
// lock, a link()-based lock file that survives a killed holder (see node_iscsi_dblock.go):
//
//  1. Never kill iscsiadm on a deadline. A per-call timeout bounds how long the RPC waits;
//     the process keeps running (cmdexec.RunProtected) and only a hung one is killed, at
//     its hard ceiling.
//  2. Run database operations one at a time per node. The database is single-writer anyway,
//     so parallel calls only queue on the lock file and burn their timeouts doing so.
//     Logins and logouts are the exception: they hold the lock only while reading records,
//     then wait on the network and iscsid, so one slow portal must not block every other
//     volume's stage on the node. Outside the gate they can still meet the lock; the
//     janitor treats any live iscsiadm older than the lock as its possible owner.
const (
	// iscsiadmHardCeiling kills an iscsiadm that is truly hung. It is far above open-iscsi's
	// 30 s lock wait plus a login's own retries.
	iscsiadmHardCeiling = 3 * time.Minute
	// iscsiadmDBTimeout is the wait budget for a database operation. It exceeds open-iscsi's
	// 30 s lock wait, so a call queued behind a foreign lock holder reports open-iscsi's
	// error rather than our deadline.
	iscsiadmDBTimeout = 45 * time.Second
	// iscsiadmQueryTimeout is the wait budget for session queries, which read sysfs.
	iscsiadmQueryTimeout = 10 * time.Second
	// iscsiadmQueryHardCeiling bounds session queries so a hung sysfs cannot pile up
	// processes from polling loops. It is still safe for the lock: an iscsiadm waiting on
	// it gives up by itself after 30 s, and none holds it for anything close to a minute.
	iscsiadmQueryHardCeiling = time.Minute

	iscsiDBLockTimeoutMessage = "Timeout on acquiring lock on DB"

	// iscsiErrNoObjsFound is iscsiadm's exit code for "No records found" / "No active
	// sessions" (ISCSI_ERR_NO_OBJS_FOUND).
	iscsiErrNoObjsFound = 21

	iscsiadmModeNode = "node"
	iscsiadmLogin    = "--login"
)

// Static errors for iscsiadm conditions that are not about the target.
var (
	ErrISCSINodeDBLocked    = errors.New("open-iscsi node database lock is held (lock.write); an iscsiadm may have been killed while holding it")
	ErrISCSIAdmStillRunning = errors.New("iscsiadm still running")
)

// iscsiadmDBModes are the iscsiadm modes that read or write the node database under its lock.
var iscsiadmDBModes = map[string]bool{"discovery": true, "discoverydb": true, iscsiadmModeNode: true, "iface": true}

// iscsiadmNeedsDBLock reports whether args run an iscsiadm database operation that must go
// through the node's gate. Logins and logouts are excluded (see above).
func iscsiadmNeedsDBLock(args []string) bool {
	if iscsiadmIsSessionAction(args) {
		return false
	}
	for i, arg := range args {
		switch {
		case (arg == "-m" || arg == "--mode") && i+1 < len(args):
			return iscsiadmDBModes[args[i+1]]
		case strings.HasPrefix(arg, "--mode="):
			return iscsiadmDBModes[strings.TrimPrefix(arg, "--mode=")]
		}
	}
	return false
}

// iscsiadmIsSessionAction reports whether args log in or out, which can wait on the network.
func iscsiadmIsSessionAction(args []string) bool {
	for _, arg := range args {
		switch arg {
		case iscsiadmLogin, "-l", "--logout", "-u":
			return true
		}
	}
	return false
}

// iscsiadmCommand returns the command line that runs iscsiadm in the host's namespaces when
// the plugin runs in a container with hostPID, so the host's iscsid and /etc/iscsi are used.
// nsenter does not fork, so the process the driver waits on is iscsiadm itself.
func iscsiadmCommand(args []string) (name string, cmdArgs []string) {
	if _, err := os.Stat("/proc/1/ns/mnt"); err == nil {
		nsenterArgs := make([]string, 0, 4+len(args))
		nsenterArgs = append(nsenterArgs, "--mount=/proc/1/ns/mnt", "--ipc=/proc/1/ns/ipc", "--", "iscsiadm")
		nsenterArgs = append(nsenterArgs, args...)
		klog.V(5).Infof("Running iscsiadm via nsenter: nsenter %v", nsenterArgs)
		return "nsenter", nsenterArgs
	}
	klog.V(5).Infof("Running iscsiadm directly: iscsiadm %v", args)
	return "iscsiadm", args
}

// iscsiadmGate serializes node-database iscsiadm calls. The zero value is ready to use.
type iscsiadmGate struct {
	ch   chan struct{}
	once sync.Once
}

func (g *iscsiadmGate) acquire(ctx context.Context) error {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
	select {
	case g.ch <- struct{}{}:
		return nil
	default:
	}
	metrics.ISCSIAdmWaiting()
	defer metrics.ISCSIAdmDoneWaiting()
	select {
	case g.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *iscsiadmGate) release() { <-g.ch }

// iscsiadmRunner runs iscsiadm for this node. One driver process serves one node, so there
// is a single package-level runner (nodeISCSIAdm); tests build their own.
type iscsiadmRunner struct {
	// runFn replaces the real command in tests; it gets a context bounded by the timeout.
	runFn func(context.Context, ...string) ([]byte, error)
	// clearStaleFn removes an orphaned open-iscsi DB lock; nil means never.
	clearStaleFn func() bool
	gate         iscsiadmGate
}

var nodeISCSIAdm = &iscsiadmRunner{clearStaleFn: hostISCSIDBLockJanitor}

var (
	hostJanitorOnce sync.Once
	hostJanitor     *iscsiDBLockJanitor
)

// hostISCSIDBLockJanitor clears a stale lock on the host. The janitor is built on first use
// so the hostPID check and its log line only happen on nodes that use iSCSI.
func hostISCSIDBLockJanitor() bool {
	hostJanitorOnce.Do(func() { hostJanitor = newISCSIDBLockJanitor() })
	return hostJanitor.clearStale()
}

// runISCSIAdm runs iscsiadm with args through this node's runner. timeout bounds how long
// the caller waits; see iscsiadmRunner.run.
func runISCSIAdm(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	return nodeISCSIAdm.run(ctx, timeout, args...)
}

// run runs iscsiadm with args. timeout bounds how long the caller waits; for database
// operations it starts once this call owns the node's iscsiadm gate. A command still running
// at the deadline is left to finish and reported as ErrISCSIAdmStillRunning; for database
// operations the gate stays held until it exits.
func (r *iscsiadmRunner) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if !iscsiadmNeedsDBLock(args) {
		hardCeiling := iscsiadmQueryHardCeiling
		if iscsiadmIsSessionAction(args) {
			// A login or logout can legitimately take minutes with iscsid's default retries.
			hardCeiling = iscsiadmHardCeiling
		}
		output, err := r.exec(ctx, timeout, hardCeiling, args)
		if cmdexec.IsStillRunning(err) {
			go logLeftRunningResult(err, "iscsiadm "+strings.Join(args, " "))
		} else if err != nil && isISCSIDBLockTimeout(output) {
			// Not holding the gate, so no janitor here; the next gated call will run it.
			err = fmt.Errorf("%w: %w, output: %s", ErrISCSINodeDBLocked, err, strings.TrimSpace(string(output)))
		}
		return output, wrapISCSIAdmStillRunning(err, timeout)
	}

	if err := r.gate.acquire(ctx); err != nil {
		return nil, fmt.Errorf("waiting for this node's iscsiadm gate: %w", err)
	}
	holding := true
	defer func() {
		if holding {
			r.gate.release()
		}
	}()

	r.clearStale()
	output, err := r.exec(ctx, timeout, iscsiadmHardCeiling, args)
	// A command still running may already have printed the lock timeout; retrying then would
	// start a second iscsiadm alongside it.
	if err != nil && !cmdexec.IsStillRunning(err) && isISCSIDBLockTimeout(output) {
		// Retry inside this hold: calling run again would wait on our own gate.
		if r.clearStale() {
			output, err = r.exec(ctx, timeout, iscsiadmHardCeiling, args)
		}
		if err != nil && isISCSIDBLockTimeout(output) {
			err = fmt.Errorf("%w: %w, output: %s", ErrISCSINodeDBLocked, err, strings.TrimSpace(string(output)))
		}
	}

	if done, running := cmdexec.StillRunningDone(err); running {
		holding = false
		go func() {
			<-done
			r.gate.release()
			logLeftRunningResult(err, "iscsiadm "+strings.Join(args, " "))
		}()
	}
	return output, wrapISCSIAdmStillRunning(err, timeout)
}

func (r *iscsiadmRunner) exec(ctx context.Context, timeout, hardCeiling time.Duration, args []string) ([]byte, error) {
	if r.runFn != nil {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return r.runFn(callCtx, args...)
	}
	name, cmdArgs := iscsiadmCommand(args)
	return cmdexec.RunProtected(ctx, timeout, hardCeiling, name, cmdArgs...)
}

func (r *iscsiadmRunner) clearStale() bool {
	if r.clearStaleFn != nil {
		return r.clearStaleFn()
	}
	return false
}

func isISCSIDBLockTimeout(output []byte) bool {
	return strings.Contains(string(output), iscsiDBLockTimeoutMessage)
}

func wrapISCSIAdmStillRunning(err error, timeout time.Duration) error {
	if err == nil || !cmdexec.IsStillRunning(err) {
		return err
	}
	return fmt.Errorf("%w after %v; left running so open-iscsi's node database lock is not orphaned: %w",
		ErrISCSIAdmStillRunning, timeout, err)
}

// iscsiadmErrorIs reports whether err is one of the iscsiadm conditions that are not about
// the target at all (lock held, command left running), for callers choosing what to report.
func iscsiadmErrorIs(err error) bool {
	return errors.Is(err, ErrISCSINodeDBLocked) || errors.Is(err, ErrISCSIAdmStillRunning)
}

// iscsiadmNoObjects reports whether err is iscsiadm's "No records found" / "No active
// sessions" exit, which is an empty answer rather than a failure.
func iscsiadmNoObjects(err error) bool {
	code, ok := cmdexec.ExitCode(err)
	return ok && code == iscsiErrNoObjsFound
}

// logLeftRunningResult waits for a command left running to exit and logs how it ended, so a
// failure in the background (including a kill at its hard ceiling) is not silent.
func logLeftRunningResult(err error, what string) {
	var still *cmdexec.StillRunningError
	if !errors.As(err, &still) {
		return
	}
	<-still.Done
	output, resultErr := still.Result()
	if resultErr != nil {
		klog.Warningf("%s, left running after its request ended, failed: %v, output: %s", what, resultErr, bytes.TrimSpace(output))
		return
	}
	klog.Infof("%s, left running after its request ended, completed successfully", what)
}
