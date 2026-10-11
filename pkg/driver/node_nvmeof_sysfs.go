package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sysBlockPath = "/sys/block"

var (
	errNVMeAmbiguousNamespace = errors.New("ambiguous NVMe namespace identity")
	nvmeNamespaceName         = regexp.MustCompile(`^nvme\d+n\d+$`)
	nvmeNamespacePartition    = regexp.MustCompile(`^(nvme\d+n\d+)p\d+$`)
	nvmeControllerName        = regexp.MustCompile(`^nvme\d+$`)
)

func readNVMeSysfsValue(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // kernel sysfs path; tests use an isolated fixture root
	return strings.TrimSpace(string(data)), err
}

// Native multipath names namespaces after subsystem instances, not controllers.
// Resolve each visible namespace's actual owner instead of constructing a name.
func nvmeNamespaceDeviceForNQN(root, nqn string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read NVMe block devices: %w", err)
	}
	var matches []string
	for _, entry := range entries {
		name := entry.Name()
		if !nvmeNamespaceName.MatchString(name) {
			continue
		}
		owner, err := readNVMeSysfsValue(filepath.Join(root, name, "device", "subsysnqn"))
		if err != nil || owner != nqn || nqn == "" {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w for NQN %s", ErrNVMeDeviceNotFound, nqn)
	}
	if len(matches) == 1 {
		return "/dev/" + matches[0], nil
	}
	var nsidOne []string
	for _, name := range matches {
		if nsid, err := readNVMeSysfsValue(filepath.Join(root, name, "nsid")); err == nil && nsid == "1" {
			nsidOne = append(nsidOne, name)
		}
	}
	if len(nsidOne) == 1 {
		return "/dev/" + nsidOne[0], nil
	}
	return "", fmt.Errorf("%w for NQN %s: %v", errNVMeAmbiguousNamespace, nqn, matches)
}

func nvmeNamespaceBase(device string) (string, error) {
	name := filepath.Base(device)
	if match := nvmeNamespacePartition.FindStringSubmatch(name); match != nil {
		name = match[1]
	}
	if !nvmeNamespaceName.MatchString(name) {
		return "", fmt.Errorf("%s: %w", device, ErrNVMeNotNVMeDevice)
	}
	return name, nil
}

func nvmeNQNForDevice(root, device string) (string, error) {
	name, err := nvmeNamespaceBase(device)
	if err != nil {
		return "", err
	}
	nqn, err := readNVMeSysfsValue(filepath.Join(root, name, "device", "subsysnqn"))
	if err != nil {
		return "", fmt.Errorf("read namespace NQN: %w", err)
	}
	if nqn == "" {
		return "", ErrNVMeEmptyNQN
	}
	return nqn, nil
}

// Resolve the namespace's controller owner, or a live path beneath its multipath
// subsystem. Never consult the similarly numbered but potentially foreign controller.
func nvmeControllerForNamespace(root, device string) (controller, state string, err error) {
	name, err := nvmeNamespaceBase(device)
	if err != nil {
		return "", "", err
	}
	owner, err := filepath.EvalSymlinks(filepath.Join(root, name, "device"))
	if err != nil {
		return "", "", fmt.Errorf("resolve namespace owner: %w", err)
	}
	if nvmeControllerName.MatchString(filepath.Base(owner)) {
		ctrlState, readErr := readNVMeSysfsValue(filepath.Join(owner, "state"))
		return "/dev/" + filepath.Base(owner), ctrlState, readErr
	}
	entries, err := os.ReadDir(owner)
	if err != nil {
		return "", "", fmt.Errorf("read namespace controllers: %w", err)
	}
	var first, firstState string
	for _, entry := range entries {
		if !nvmeControllerName.MatchString(entry.Name()) {
			continue
		}
		ctrlState, readErr := readNVMeSysfsValue(filepath.Join(owner, entry.Name(), "state"))
		if readErr != nil {
			continue
		}
		ctrlPath := "/dev/" + entry.Name()
		if ctrlState == "live" {
			return ctrlPath, ctrlState, nil
		}
		if first == "" {
			first, firstState = ctrlPath, ctrlState
		}
	}
	if first == "" {
		return "", "", fmt.Errorf("%w: %s", ErrNVMeControllerNotFound, device)
	}
	return first, firstState, nil
}
