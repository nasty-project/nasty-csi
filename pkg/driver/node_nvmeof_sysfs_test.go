package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeNVMeFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func namespaceFixture(t *testing.T, root, name, nqn, nsid string) {
	t.Helper()
	writeNVMeFixture(t, filepath.Join(root, name, "device", "subsysnqn"), nqn)
	writeNVMeFixture(t, filepath.Join(root, name, "nsid"), nsid)
}

func TestNVMeNamespaceIdentity(t *testing.T) {
	root := t.TempDir()
	namespaceFixture(t, root, "nvme1n1", "nqn:foreign", "1")
	namespaceFixture(t, root, "nvme7n1", "nqn:ours", "1")
	// Hidden per-path namespaces and partitions must never win discovery.
	namespaceFixture(t, root, "nvme7c2n1", "nqn:ours", "1")
	namespaceFixture(t, root, "nvme7n1p1", "nqn:ours", "1")
	device, err := nvmeNamespaceDeviceForNQN(root, "nqn:ours")
	if err != nil || device != "/dev/nvme7n1" {
		t.Fatalf("resolved %q, %v", device, err)
	}
	if nqn, err := nvmeNQNForDevice(root, device); err != nil || nqn != "nqn:ours" {
		t.Fatalf("reverse lookup %q, %v", nqn, err)
	}
	if _, err := nvmeNamespaceDeviceForNQN(root, "nqn:missing"); !errors.Is(err, ErrNVMeDeviceNotFound) {
		t.Fatal(err)
	}
	namespaceFixture(t, root, "nvme8n2", "nqn:ours", "2")
	if device, err := nvmeNamespaceDeviceForNQN(root, "nqn:ours"); err != nil || device != "/dev/nvme7n1" {
		t.Fatalf("NSID 1 resolution: %q, %v", device, err)
	}
	namespaceFixture(t, root, "nvme9n1", "nqn:ours", "1")
	if device, err := nvmeNamespaceDeviceForNQN(root, "nqn:ours"); device != "" || !errors.Is(err, errNVMeAmbiguousNamespace) {
		t.Fatalf("ambiguous NSID 1: %q, %v", device, err)
	}
}

func TestNVMeNamespaceAmbiguityWithoutNSIDOne(t *testing.T) {
	root := t.TempDir()
	namespaceFixture(t, root, "nvme7n2", "nqn:ours", "2")
	namespaceFixture(t, root, "nvme8n3", "nqn:ours", "3")
	if device, err := nvmeNamespaceDeviceForNQN(root, "nqn:ours"); device != "" || !errors.Is(err, errNVMeAmbiguousNamespace) {
		t.Fatalf("ambiguous namespaces: %q, %v", device, err)
	}
}

func TestNVMeControllerUsesNamespaceOwner(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(root, "owners", "nvme-subsys7")
	writeNVMeFixture(t, filepath.Join(owner, "subsysnqn"), "nqn:ours")
	writeNVMeFixture(t, filepath.Join(owner, "nvme2", "state"), "connecting")
	writeNVMeFixture(t, filepath.Join(owner, "nvme12", "state"), "live")
	writeNVMeFixture(t, filepath.Join(root, "decoy", "nvme7", "state"), "dead")
	block := filepath.Join(root, "block")
	if err := os.MkdirAll(filepath.Join(block, "nvme7n1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owner, filepath.Join(block, "nvme7n1", "device")); err != nil {
		t.Fatal(err)
	}
	previous := sysBlockPath
	sysBlockPath = block
	t.Cleanup(func() { sysBlockPath = previous })
	for _, device := range []string{"/dev/nvme7n1", "/dev/nvme7n1p1"} {
		controller, state, err := nvmeControllerForNamespace(block, device)
		if err != nil || controller != "/dev/nvme12" || state != "live" {
			t.Fatalf("owner resolution: %q, %q, %v", controller, state, err)
		}
		if got := extractNVMeController(device); got != "/dev/nvme12" {
			t.Fatalf("rescan controller: %q", got)
		}
		if state, err := getNVMeControllerState(device); err != nil || state != "live" {
			t.Fatalf("health state: %q, %v", state, err)
		}
		if nqn, err := nvmeNQNForDevice(block, device); err != nil || nqn != "nqn:ours" {
			t.Fatalf("unstage NQN: %q, %v", nqn, err)
		}
	}
	// A non-multipath owner is a controller directly.
	controllerOwner := filepath.Join(root, "owners", "nvme3")
	writeNVMeFixture(t, filepath.Join(controllerOwner, "state"), "live")
	if err := os.MkdirAll(filepath.Join(block, "nvme4n1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(controllerOwner, filepath.Join(block, "nvme4n1", "device")); err != nil {
		t.Fatal(err)
	}
	if controller, _, err := nvmeControllerForNamespace(block, "/dev/nvme4n1"); err != nil || controller != "/dev/nvme3" {
		t.Fatalf("direct owner: %q, %v", controller, err)
	}
}

func TestNVMeDiscoveryDoesNotGuessControllerNamespace(t *testing.T) {
	root := t.TempDir()
	namespaceFixture(t, root, "nvme1n1", "nqn:foreign", "1")
	namespaceFixture(t, root, "nvme7n1", "nqn:ours", "1")
	previous := sysBlockPath
	sysBlockPath = root
	t.Cleanup(func() { sysBlockPath = previous })
	s := &NodeService{}
	output := []byte("{\n\"NQN\": \"nqn:ours\",\n\"Name\": \"nvme1\"\n}")
	writeNVMeFixture(t, filepath.Join(root, "nvme7n1", "device", "nvme12", "state"), "live")
	bin := t.TempDir()
	command := filepath.Join(bin, "nvme")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat <<'EOF'\n"+string(output)+"\nEOF\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	device, controller, err := s.findNVMeDeviceByNQNWithController(context.Background(), "nqn:ours")
	if err != nil || device != "/dev/nvme7n1" || controller != "nvme12" {
		t.Fatalf("poll identity: %q, %q, %v", device, controller, err)
	}
	if device := s.parseNVMeListSubsysOutputForNQN(output, "nqn:ours"); device != "/dev/nvme7n1" {
		t.Fatalf("list-subsys guessed foreign namespace: %q", device)
	}
	namespaceFixture(t, root, "nvme8n1", "nqn:ours", "1")
	if device := s.parseNVMeListSubsysOutputForNQN(output, "nqn:ours"); device != "" {
		t.Fatalf("ambiguous identity returned %q", device)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.findNVMeDeviceByNQNWithController(ctx, "nqn:ours"); err == nil {
		t.Fatal("canceled discovery must fail")
	}
}
