package driver

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/cmdexec"
)

const (
	loginTestIQN    = "iqn.2137-04.storage.nasty:pvc-login-test"
	loginTestPortal = "192.0.2.10:3260"
	lockTimeoutLine = "iscsiadm: Timeout on acquiring lock on DB: /run/lock/iscsi/lock, errno: 11"
)

// fakeNodeISCSIAdm replaces this node's iscsiadm with fn for the test and records every call.
func fakeNodeISCSIAdm(t *testing.T, fn func(args ...string) ([]byte, error)) func() [][]string {
	t.Helper()
	var (
		mu    sync.Mutex
		calls [][]string
	)
	previous := nodeISCSIAdm
	nodeISCSIAdm = &iscsiadmRunner{runFn: func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		return fn(args...)
	}}
	t.Cleanup(func() { nodeISCSIAdm = previous })
	return func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if c, ok := cmdexec.ExitCode(err); !ok || c != code {
		t.Fatalf("exit fixture = %v, want exit %d", err, code)
	}
	return err
}

func countCalls(calls [][]string, arg string) int {
	n := 0
	for _, c := range calls {
		if slices.Contains(c, arg) {
			n++
		}
	}
	return n
}

func isNodeListing(args []string) bool {
	return slices.Equal(args, []string{"-m", "node", "-P", "0"})
}

var loginTestParams = &iscsiConnectionParams{iqn: loginTestIQN, server: "192.0.2.10", port: "3260"}

// After a reboot every stage on the node used to run a full SendTargets discovery, rewriting
// every target's record under open-iscsi's lock. An existing record is enough to log in.
func TestLoginISCSITargetSkipsDiscoveryWhenRecordExists(t *testing.T) {
	calls := fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		if isNodeListing(args) {
			return []byte(loginTestPortal + ",1 " + loginTestIQN + "\n"), nil
		}
		return nil, nil
	})
	if err := (&NodeService{}).loginISCSITarget(context.Background(), loginTestParams); err != nil {
		t.Fatalf("loginISCSITarget() = %v", err)
	}
	if n := countCalls(calls(), "discovery"); n != 0 {
		t.Fatalf("discovery ran %d times with the record already present", n)
	}
	if n := countCalls(calls(), "--login"); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
}

func TestLoginISCSITargetDiscoversWhenNodeHasNoRecords(t *testing.T) {
	noRecords := exitError(t, iscsiErrNoObjsFound)
	calls := fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		switch {
		case isNodeListing(args):
			return []byte("iscsiadm: No records found"), noRecords
		case slices.Contains(args, "discovery"):
			return []byte(loginTestPortal + ",1 " + loginTestIQN + "\n"), nil
		}
		return nil, nil
	})
	if err := (&NodeService{}).loginISCSITarget(context.Background(), loginTestParams); err != nil {
		t.Fatalf("loginISCSITarget() = %v", err)
	}
	if n := countCalls(calls(), "discovery"); n != 1 {
		t.Fatalf("discoveries = %d, want 1", n)
	}
}

// A record on another portal (NAT, dual stack) is not ours to log in through.
func TestLoginISCSITargetDiscoversWhenRecordIsOnAnotherPortal(t *testing.T) {
	calls := fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		switch {
		case isNodeListing(args):
			return []byte("10.0.0.5:3260,1 " + loginTestIQN + "\n"), nil
		case slices.Contains(args, "discovery"):
			return []byte(loginTestPortal + ",1 " + loginTestIQN + "\n"), nil
		}
		return nil, nil
	})
	if err := (&NodeService{}).loginISCSITarget(context.Background(), loginTestParams); err != nil {
		t.Fatalf("loginISCSITarget() = %v", err)
	}
	if n := countCalls(calls(), "discovery"); n != 1 {
		t.Fatalf("discoveries = %d, want 1", n)
	}
}

func TestLoginISCSITargetRefreshesStaleRecordOnce(t *testing.T) {
	loginFailed := exitError(t, 8)
	logins := 0
	calls := fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		switch {
		case isNodeListing(args):
			return []byte(loginTestPortal + ",1 " + loginTestIQN + "\n"), nil
		case slices.Contains(args, "discovery"):
			return []byte(loginTestPortal + ",1 " + loginTestIQN + "\n"), nil
		case slices.Contains(args, "--login"):
			logins++
			if logins == 1 {
				return []byte("iscsiadm: initiator reported error (8 - connection timed out)"), loginFailed
			}
		}
		return nil, nil
	})
	if err := (&NodeService{}).loginISCSITarget(context.Background(), loginTestParams); err != nil {
		t.Fatalf("loginISCSITarget() = %v, want success after refreshing the record", err)
	}
	if n := countCalls(calls(), "discovery"); n != 1 || logins != 2 {
		t.Fatalf("discoveries = %d, logins = %d; want one refresh and one retried login", n, logins)
	}
}

// A held lock says nothing about the target: report it as such, and do not pile a discovery
// (another database write) on top of it.
func TestLoginISCSITargetReportsHeldLockWithoutDiscovery(t *testing.T) {
	lockErr := exitError(t, 6)
	calls := fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		if isNodeListing(args) {
			return []byte(lockTimeoutLine), lockErr
		}
		return nil, nil
	})
	err := (&NodeService{}).loginISCSITarget(context.Background(), loginTestParams)
	if !errors.Is(err, ErrISCSINodeDBLocked) {
		t.Fatalf("loginISCSITarget() = %v, want ErrISCSINodeDBLocked", err)
	}
	if n := countCalls(calls(), "discovery"); n != 0 {
		t.Fatalf("discovery ran %d times behind a held lock", n)
	}
}

// Once the RPC is canceled, staging must stop retrying instead of sleeping through its
// remaining attempts and piling more iscsiadm work onto the node.
func TestStageISCSIVolumeStopsRetryingWhenContextEnds(t *testing.T) {
	noSessions := exitError(t, iscsiErrNoObjsFound)
	loginFailed := exitError(t, 8)
	logins := 0
	fakeNodeISCSIAdm(t, func(args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "--login"):
			logins++
			return []byte("iscsiadm: initiator reported error (8 - connection timed out)"), loginFailed
		case slices.Contains(args, "session"):
			return []byte("iscsiadm: No active sessions."), noSessions
		case isNodeListing(args):
			return []byte(loginTestPortal + ",1 " + loginTestIQN), nil
		}
		return nil, nil
	})
	service := &NodeService{isMountedFn: func(context.Context, string) (bool, error) { return false, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	request := &csi.NodeStageVolumeRequest{
		VolumeId: "pool/csi/volume", StagingTargetPath: filepath.Join(t.TempDir(), "globalmount"),
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}},
		VolumeContext:    map[string]string{VolumeContextKeyProtocol: ProtocolISCSI, VolumeContextKeyISCSIIQN: loginTestIQN, "server": "192.0.2.10", "port": "3260"},
	}
	start := time.Now()
	_, err := service.NodeStageVolume(ctx, request)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("NodeStageVolume() returned after %v; retries outlived the RPC", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "connection timed out") {
		t.Fatalf("NodeStageVolume() = %v; want the last real login error, not only the context error", err)
	}
	if logins == 0 {
		t.Fatal("login never attempted")
	}
}

func TestCheckISCSIAdmDoesNotCallSlownessMissing(t *testing.T) {
	exited := make(chan struct{})
	defer close(exited)
	fakeNodeISCSIAdm(t, func(...string) ([]byte, error) {
		return nil, &cmdexec.StillRunningError{Name: "iscsiadm", Done: exited, Err: context.DeadlineExceeded}
	})
	if err := (&NodeService{}).checkISCSIAdm(context.Background()); errors.Is(err, ErrISCSIAdmNotFound) {
		t.Fatalf("checkISCSIAdm() = %v; a slow iscsiadm is not a missing one", err)
	}
}

// Session-query failures are not "no device": they must not be hidden as ErrISCSIDeviceNotFound.
func TestFindISCSIDeviceReportsQueryFailures(t *testing.T) {
	noSessions := exitError(t, iscsiErrNoObjsFound)
	fakeNodeISCSIAdm(t, func(...string) ([]byte, error) {
		return []byte("iscsiadm: No active sessions."), noSessions
	})
	if _, err := (&NodeService{}).findISCSIDevice(context.Background(), loginTestParams); !errors.Is(err, ErrISCSIDeviceNotFound) {
		t.Fatalf("no sessions = %v, want ErrISCSIDeviceNotFound", err)
	}
	failed := exitError(t, 4)
	fakeNodeISCSIAdm(t, func(...string) ([]byte, error) {
		return []byte("iscsiadm: could not connect to iscsid"), failed
	})
	_, err := (&NodeService{}).findISCSIDevice(context.Background(), loginTestParams)
	if errors.Is(err, ErrISCSIDeviceNotFound) || !errors.Is(err, errISCSISessionQuery) {
		t.Fatalf("query failure = %v, want errISCSISessionQuery", err)
	}
}

// The health fallback used to return the first session's state for any device.
func TestParseISCSISessionStateForDevice(t *testing.T) {
	output := `Target: iqn.2137-04.storage.nasty:vol-a (non-flash)
	Current Portal: 192.0.2.10:3260,1
		iSCSI Session State: LOGGED_IN
			Attached scsi disk sda		State: running
Target: iqn.2137-04.storage.nasty:vol-b (non-flash)
	Current Portal: 192.0.2.10:3260,1
		iSCSI Session State: FAILED
			Attached scsi disk sdb		State: transport-offline
`
	if got := parseISCSISessionStateForDevice(output, "sdb"); got != "FAILED" {
		t.Errorf("state for sdb = %q, want FAILED", got)
	}
	if got := parseISCSISessionStateForDevice(output, "sda"); got != iscsiSessionStateLoggedIn {
		t.Errorf("state for sda = %q, want LOGGED_IN", got)
	}
	if got := parseISCSISessionStateForDevice(output, "sdc"); got != "" {
		t.Errorf("state for an unknown device = %q, want empty", got)
	}
}
