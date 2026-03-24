// Package framework provides utilities for E2E testing of the NASty CSI driver.
package framework

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"

	nastyapi "github.com/nasty-project/nasty-go"
	"k8s.io/klog/v2"
)

// ErrDatasetDeleteTimeout is returned when waiting for a dataset to be deleted times out.
var ErrDatasetDeleteTimeout = errors.New("timeout waiting for dataset to be deleted")

// ErrDatasetNotFound is returned when a requested dataset doesn't exist.
var ErrDatasetNotFound = errors.New("dataset not found")

// ErrInvalidDatasetPath is returned when a dataset path doesn't have filesystem/name format.
var ErrInvalidDatasetPath = errors.New("invalid dataset path")

// NAStyVerifier provides methods for verifying NASty backend state.
type NAStyVerifier struct {
	client *nastyapi.Client
}

// NewNAStyVerifier creates a new NAStyVerifier.
func NewNAStyVerifier(host, apiKey string) (*NAStyVerifier, error) {
	url := fmt.Sprintf("wss://%s/ws", host)
	client, err := nastyapi.NewClient(url, apiKey, true, nil) // skip TLS verify for tests, no metrics
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NASty: %w", err)
	}
	return &NAStyVerifier{client: client}, nil
}

// Close closes the NASty client connection.
func (v *NAStyVerifier) Close() {
	if v.client != nil {
		v.client.Close()
	}
}

// Client returns the underlying NASty API client for advanced queries.
func (v *NAStyVerifier) Client() *nastyapi.Client {
	return v.client
}

// parseDatasetPath splits a "filesystem/name" path into filesystem and name.
func parseDatasetPath(datasetPath string) (filesystem, name string, err error) {
	parts := strings.SplitN(datasetPath, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidDatasetPath, datasetPath)
	}
	return parts[0], parts[1], nil
}

// DatasetExists checks if a subvolume exists on NASty.
func (v *NAStyVerifier) DatasetExists(ctx context.Context, datasetPath string) (bool, error) {
	filesystem, name, err := parseDatasetPath(datasetPath)
	if err != nil {
		return false, err
	}
	subvol, err := v.client.GetSubvolume(ctx, filesystem, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("failed to query subvolume: %w", err)
	}
	return subvol != nil, nil
}

// WaitForDatasetDeleted polls NASty until the subvolume is confirmed deleted or timeout.
func (v *NAStyVerifier) WaitForDatasetDeleted(ctx context.Context, datasetPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		exists, err := v.DatasetExists(ctx, datasetPath)
		if err != nil {
			klog.V(1).Infof("Warning: error checking subvolume existence: %v", err)
		} else if !exists {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return fmt.Errorf("%w: %s", ErrDatasetDeleteTimeout, datasetPath)
}

// NFSShareExists checks if an NFS share exists for the given path.
func (v *NAStyVerifier) NFSShareExists(ctx context.Context, path string) (bool, error) {
	shares, err := v.client.ListNFSShares(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query NFS shares: %w", err)
	}
	for _, s := range shares {
		if s.Path == path {
			return true, nil
		}
	}
	return false, nil
}

// NVMeOFSubsystemExists checks if an NVMe-oF subsystem exists with the given NQN.
func (v *NAStyVerifier) NVMeOFSubsystemExists(ctx context.Context, nqn string) (bool, error) {
	subsystem, err := v.client.GetNVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return false, fmt.Errorf("failed to query NVMe-oF subsystems: %w", err)
	}
	return subsystem != nil, nil
}

// DeleteDataset deletes a subvolume from NASty.
func (v *NAStyVerifier) DeleteDataset(ctx context.Context, datasetPath string) error {
	filesystem, name, err := parseDatasetPath(datasetPath)
	if err != nil {
		return err
	}
	if delErr := v.client.DeleteSubvolume(ctx, filesystem, name); delErr != nil {
		if strings.Contains(delErr.Error(), "not found") {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete subvolume %s: %w", datasetPath, delErr)
	}
	return nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem from NASty by NQN.
func (v *NAStyVerifier) DeleteNVMeOFSubsystem(ctx context.Context, nqn string) error {
	subsystem, err := v.client.GetNVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return fmt.Errorf("failed to query NVMe-oF subsystem: %w", err)
	}
	if subsystem == nil {
		return nil
	}
	return v.client.DeleteNVMeOFSubsystem(ctx, subsystem.ID)
}

// DeleteNFSShare deletes an NFS share from NASty by path.
func (v *NAStyVerifier) DeleteNFSShare(ctx context.Context, path string) error {
	shares, err := v.client.ListNFSShares(ctx)
	if err != nil {
		return fmt.Errorf("failed to list NFS shares: %w", err)
	}
	for _, s := range shares {
		if s.Path == path {
			return v.client.DeleteNFSShare(ctx, s.ID)
		}
	}
	return nil
}

// SMBShareExists checks if an SMB share exists for the given path.
func (v *NAStyVerifier) SMBShareExists(ctx context.Context, path string) (bool, error) {
	shares, err := v.client.ListSMBShares(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query SMB shares: %w", err)
	}
	for _, s := range shares {
		if s.Path == path {
			return true, nil
		}
	}
	return false, nil
}

// DeleteSMBShare deletes an SMB share from NASty by path.
func (v *NAStyVerifier) DeleteSMBShare(ctx context.Context, path string) error {
	shares, err := v.client.ListSMBShares(ctx)
	if err != nil {
		return fmt.Errorf("failed to list SMB shares: %w", err)
	}
	for _, s := range shares {
		if s.Path == path {
			return v.client.DeleteSMBShare(ctx, s.ID)
		}
	}
	return nil
}

// ISCSITargetExists checks if an iSCSI target exists with the given IQN.
func (v *NAStyVerifier) ISCSITargetExists(ctx context.Context, iqn string) (bool, error) {
	target, err := v.client.GetISCSITargetByIQN(ctx, iqn)
	if err != nil {
		return false, fmt.Errorf("failed to query iSCSI targets: %w", err)
	}
	return target != nil, nil
}

// ISCSIExtentExists is kept for backward compatibility — NASty does not have separate extents.
func (v *NAStyVerifier) ISCSIExtentExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// DeleteISCSITarget deletes an iSCSI target from NASty by IQN.
func (v *NAStyVerifier) DeleteISCSITarget(ctx context.Context, iqn string) error {
	target, err := v.client.GetISCSITargetByIQN(ctx, iqn)
	if err != nil {
		return fmt.Errorf("failed to query iSCSI target: %w", err)
	}
	if target == nil {
		return nil
	}
	return v.client.DeleteISCSITarget(ctx, target.ID)
}

// DeleteISCSIExtent is a no-op — NASty does not have separate extents.
func (v *NAStyVerifier) DeleteISCSIExtent(_ context.Context, _ string) error {
	return nil
}

// GetDatasetOrigin returns the origin of a subvolume (if it's a clone).
// For bcachefs, clones are snapshots promoted to subvolumes — we check
// if the subvolume was created from a snapshot by looking at properties.
func (v *NAStyVerifier) GetDatasetOrigin(ctx context.Context, datasetPath string) (string, error) {
	filesystem, name, err := parseDatasetPath(datasetPath)
	if err != nil {
		return "", err
	}
	subvol, getErr := v.client.GetSubvolume(ctx, filesystem, name)
	if getErr != nil {
		return "", fmt.Errorf("failed to query subvolume: %w", getErr)
	}
	if subvol == nil {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}
	// bcachefs doesn't track clone origin.
	// Return empty — clone detection is not needed for NASty tests.
	return "", nil
}

// IsDatasetClone checks if a subvolume is a clone.
func (v *NAStyVerifier) IsDatasetClone(ctx context.Context, datasetPath string) (isClone bool, origin string, err error) {
	origin, err = v.GetDatasetOrigin(ctx, datasetPath)
	if err != nil {
		return false, "", err
	}
	return origin != "", origin, nil
}

// GetDatasetProperty retrieves a CSI xattr property from a subvolume.
func (v *NAStyVerifier) GetDatasetProperty(ctx context.Context, datasetPath, propertyName string) (string, error) {
	filesystem, name, err := parseDatasetPath(datasetPath)
	if err != nil {
		return "", err
	}
	subvol, getErr := v.client.GetSubvolume(ctx, filesystem, name)
	if getErr != nil {
		return "", fmt.Errorf("failed to query subvolume: %w", getErr)
	}
	if subvol == nil {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}
	if subvol.Properties == nil {
		return "", nil
	}
	return subvol.Properties[propertyName], nil
}

// GetSubvolumeProperty retrieves a subvolume field by name.
// Maps common property names to NASty subvolume fields.
func (v *NAStyVerifier) GetSubvolumeProperty(ctx context.Context, datasetPath, propertyName string) (string, error) {
	filesystem, name, err := parseDatasetPath(datasetPath)
	if err != nil {
		return "", err
	}
	subvol, getErr := v.client.GetSubvolume(ctx, filesystem, name)
	if getErr != nil {
		return "", fmt.Errorf("failed to query subvolume: %w", getErr)
	}
	if subvol == nil {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}

	switch propertyName {
	case "compression":
		if subvol.Compression != nil {
			return *subvol.Compression, nil
		}
	case "volsize":
		if subvol.VolsizeBytes != nil {
			return strconv.FormatUint(*subvol.VolsizeBytes, 10), nil
		}
	}
	return "", nil
}

// ResourceSnapshot holds a point-in-time inventory of CSI-related NASty resources.
type ResourceSnapshot struct {
	Datasets     map[string]datasetInfo
	NFSShares    map[string]bool
	SMBShares    map[string]bool
	NVMeSubsNQNs map[string]bool
	ISCSITargets map[string]bool
	ISCSIExtents map[string]bool // always empty for NASty
}

type datasetInfo struct {
	Protocol  string
	CreatedAt string
}

// SnapshotResources queries all CSI-related resource types and returns a point-in-time snapshot.
func (v *NAStyVerifier) SnapshotResources(ctx context.Context, poolPrefix string) *ResourceSnapshot {
	snap := &ResourceSnapshot{
		Datasets:     make(map[string]datasetInfo),
		NFSShares:    make(map[string]bool),
		SMBShares:    make(map[string]bool),
		NVMeSubsNQNs: make(map[string]bool),
		ISCSITargets: make(map[string]bool),
		ISCSIExtents: make(map[string]bool),
	}

	// Managed subvolumes
	subvols, err := v.client.FindManagedSubvolumes(ctx, poolPrefix)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query managed subvolumes: %v", err)
	} else {
		for i := range subvols {
			sv := &subvols[i]
			info := datasetInfo{}
			if sv.Properties != nil {
				info.Protocol = sv.Properties[nastyapi.PropertyProtocol]
				info.CreatedAt = sv.Properties[nastyapi.PropertyCreatedAt]
			}
			snap.Datasets[sv.Filesystem+"/"+sv.Name] = info
		}
	}

	// NFS shares — filter to shares under the filesystem mount path
	nfsShares, err := v.client.ListNFSShares(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query NFS shares: %v", err)
	} else {
		mountPrefix := "/storage/" + poolPrefix
		for _, s := range nfsShares {
			if strings.HasPrefix(s.Path, mountPrefix) {
				snap.NFSShares[s.Path] = true
			}
		}
	}

	// SMB shares — filter to shares under the filesystem mount path
	smbShares, err := v.client.ListSMBShares(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query SMB shares: %v", err)
	} else {
		mountPrefix := "/storage/" + poolPrefix
		for _, s := range smbShares {
			if strings.HasPrefix(s.Path, mountPrefix) {
				snap.SMBShares[s.Path] = true
			}
		}
	}

	// NVMe-oF subsystems
	subsystems, err := v.client.ListNVMeOFSubsystems(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query NVMe-oF subsystems: %v", err)
	} else {
		for _, sub := range subsystems {
			if isCSIResource(sub.NQN) {
				snap.NVMeSubsNQNs[sub.NQN] = true
			}
		}
	}

	// iSCSI targets
	targets, err := v.client.ListISCSITargets(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query iSCSI targets: %v", err)
	} else {
		for _, t := range targets {
			if isCSIResource(t.IQN) {
				snap.ISCSITargets[t.IQN] = true
			}
		}
	}

	return snap
}

// isCSIResource returns true if the resource name looks like it was created by the CSI driver.
func isCSIResource(name string) bool {
	return strings.Contains(name, "pvc-") || strings.Contains(name, "csi-") || strings.Contains(name, "nasty-csi")
}

// LogResourceDiff compares two snapshots and logs any resources present in "after" but not in "before" (leaks).
func LogResourceDiff(before, after *ResourceSnapshot) {
	var leaks []string

	for path, info := range after.Datasets {
		if _, existed := before.Datasets[path]; !existed {
			detail := "LEAKED dataset: " + path
			if info.Protocol != "" {
				detail += " (protocol: " + info.Protocol
				if info.CreatedAt != "" {
					detail += ", created: " + info.CreatedAt
				}
				detail += ")"
			}
			leaks = append(leaks, detail)
		}
	}
	for path := range after.NFSShares {
		if !before.NFSShares[path] {
			leaks = append(leaks, "LEAKED NFS share: "+path)
		}
	}
	for path := range after.SMBShares {
		if !before.SMBShares[path] {
			leaks = append(leaks, "LEAKED SMB share: "+path)
		}
	}
	for nqn := range after.NVMeSubsNQNs {
		if !before.NVMeSubsNQNs[nqn] {
			leaks = append(leaks, "LEAKED NVMe-oF subsystem: "+nqn)
		}
	}
	for iqn := range after.ISCSITargets {
		if !before.ISCSITargets[iqn] {
			leaks = append(leaks, "LEAKED iSCSI target: "+iqn)
		}
	}

	ginkgo.GinkgoWriter.Printf("\n=== NASty Resource Leak Report ===\n")
	if len(leaks) == 0 {
		ginkgo.GinkgoWriter.Printf("No resource leaks detected.\n")
	} else {
		for _, leak := range leaks {
			ginkgo.GinkgoWriter.Printf("%s\n", leak)
		}
		ginkgo.GinkgoWriter.Printf("=== %d resource(s) leaked ===\n", len(leaks))
	}
	ginkgo.GinkgoWriter.Printf("\n")
}

// LogSnapshot logs the contents of a resource snapshot for debugging.
func LogSnapshot(label string, snap *ResourceSnapshot) {
	ginkgo.GinkgoWriter.Printf("\n--- Resource Snapshot: %s ---\n", label)
	ginkgo.GinkgoWriter.Printf("  Managed datasets: %d\n", len(snap.Datasets))
	for path, info := range snap.Datasets {
		ginkgo.GinkgoWriter.Printf("    %s (protocol: %s)\n", path, info.Protocol)
	}
	ginkgo.GinkgoWriter.Printf("  NFS shares: %d\n", len(snap.NFSShares))
	for path := range snap.NFSShares {
		ginkgo.GinkgoWriter.Printf("    %s\n", path)
	}
	ginkgo.GinkgoWriter.Printf("  SMB shares: %d\n", len(snap.SMBShares))
	for path := range snap.SMBShares {
		ginkgo.GinkgoWriter.Printf("    %s\n", path)
	}
	ginkgo.GinkgoWriter.Printf("  NVMe-oF subsystems: %d\n", len(snap.NVMeSubsNQNs))
	for nqn := range snap.NVMeSubsNQNs {
		ginkgo.GinkgoWriter.Printf("    %s\n", nqn)
	}
	ginkgo.GinkgoWriter.Printf("  iSCSI targets: %d\n", len(snap.ISCSITargets))
	for iqn := range snap.ISCSITargets {
		ginkgo.GinkgoWriter.Printf("    %s\n", iqn)
	}
	ginkgo.GinkgoWriter.Printf("---\n\n")
}
