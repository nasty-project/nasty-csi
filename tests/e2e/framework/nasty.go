// Package framework provides utilities for E2E testing of the NASty CSI driver.
package framework

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"

	"github.com/nasty-project/nasty-csi/pkg/retry"
	nastyapi "github.com/nasty-project/nasty-go"
	"k8s.io/klog/v2"
)

// ErrDatasetDeleteTimeout is returned when waiting for a dataset to be deleted times out.
var ErrDatasetDeleteTimeout = errors.New("timeout waiting for dataset to be deleted")

// ErrMissingIDField is returned when a NASty resource is missing its ID field.
var ErrMissingIDField = errors.New("resource has no ID field")

// ErrInvalidIDType is returned when a NASty resource ID cannot be converted to int.
var ErrInvalidIDType = errors.New("cannot convert resource ID to int")

// ErrDatasetNotFound is returned when a requested dataset doesn't exist.
var ErrDatasetNotFound = errors.New("dataset not found")

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

// DatasetExists checks if a dataset exists on NASty.
func (v *NAStyVerifier) DatasetExists(ctx context.Context, datasetPath string) (bool, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter}, &datasets); err != nil {
		return false, fmt.Errorf("failed to query dataset: %w", err)
	}
	return len(datasets) > 0, nil
}

// WaitForDatasetDeleted polls NASty until the dataset is confirmed deleted or timeout.
func (v *NAStyVerifier) WaitForDatasetDeleted(ctx context.Context, datasetPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		exists, err := v.DatasetExists(ctx, datasetPath)
		if err != nil {
			// Log but continue polling - transient errors are possible
			klog.V(1).Infof("Warning: error checking dataset existence: %v", err)
		} else if !exists {
			return nil // Dataset is deleted
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
			// Continue polling
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

// DeleteDataset deletes a dataset from NASty with recursive+force flags and retry logic.
// This matches the driver's DeleteDataset approach: passes recursive=true, force=true,
// and retries on EBUSY errors (12 attempts × 5s interval = ~60s total).
func (v *NAStyVerifier) DeleteDataset(ctx context.Context, datasetPath string) error {
	return retry.WithRetryNoResult(ctx, retry.DeletionConfig("DeleteDataset("+datasetPath+")"), func() error {
		var result any
		params := []any{datasetPath, map[string]any{"recursive": true, "force": true}}
		if err := v.client.Call(ctx, "pool.dataset.delete", params, &result); err != nil {
			return fmt.Errorf("failed to delete dataset %s: %w", datasetPath, err)
		}
		return nil
	})
}

// deleteResourceByFilter is a helper that queries for a resource by filter, gets its ID, and deletes it.
func (v *NAStyVerifier) deleteResourceByFilter(
	ctx context.Context,
	queryMethod string,
	deleteMethod string,
	filterKey string,
	filterValue string,
	resourceDesc string,
) error {
	// Query for the resource
	var resources []map[string]any
	filter := []any{[]any{filterKey, "=", filterValue}}
	if err := v.client.Call(ctx, queryMethod, []any{filter}, &resources); err != nil {
		return fmt.Errorf("failed to query %s: %w", resourceDesc, err)
	}
	if len(resources) == 0 {
		// Resource doesn't exist, nothing to delete
		return nil
	}

	// Get the resource ID
	resourceID, ok := resources[0]["id"]
	if !ok {
		return fmt.Errorf("%s: %w", resourceDesc, ErrMissingIDField)
	}

	// Delete the resource
	var result any
	if err := v.client.Call(ctx, deleteMethod, []any{resourceID}, &result); err != nil {
		return fmt.Errorf("failed to delete %s (id=%v): %w", resourceDesc, resourceID, err)
	}
	return nil
}

// DeleteNVMeOFSubsystem deletes an NVMe-oF subsystem from NASty by NQN.
// This is used for cleaning up retained NVMe-oF subsystems after tests.
func (v *NAStyVerifier) DeleteNVMeOFSubsystem(ctx context.Context, nqn string) error {
	subsystem, err := v.client.GetNVMeOFSubsystemByNQN(ctx, nqn)
	if err != nil {
		return fmt.Errorf("failed to query NVMe-oF subsystem: %w", err)
	}
	if subsystem == nil {
		// Subsystem doesn't exist, nothing to delete
		return nil
	}

	return v.client.DeleteNVMeOFSubsystem(ctx, subsystem.ID)
}

// deleteRelatedResources deletes all resources that reference a parent resource ID.
// This is used to delete namespaces/port-bindings associated with a subsystem.
//
// NASty API returns the parent reference (e.g., "subsys") as a nested object like:
//
//	{"id": 123, "name": "nqn...", "subnqn": "..."}
//
// NOT as a direct integer. This function handles both formats for robustness.
func (v *NAStyVerifier) deleteRelatedResources(
	ctx context.Context,
	parentID int,
	queryMethod string,
	deleteMethod string,
	parentIDField string,
	resourceDesc string,
) error {
	// Query all resources
	var resources []map[string]any
	if err := v.client.Call(ctx, queryMethod, []any{}, &resources); err != nil {
		return fmt.Errorf("failed to query %ss: %w", resourceDesc, err)
	}

	// Find and delete resources belonging to the parent
	for _, res := range resources {
		// Check if this resource belongs to our parent
		resParentID, ok := res[parentIDField]
		if !ok {
			continue
		}

		// Extract parent ID - handle both nested object and direct int formats
		resParentIDInt, err := extractID(resParentID)
		if err != nil {
			continue
		}
		if resParentIDInt != parentID {
			continue
		}

		// Get the resource ID
		resID, ok := res["id"]
		if !ok {
			continue
		}
		resIDInt, err := toInt(resID)
		if err != nil {
			continue
		}

		// Delete this resource
		var result any
		if err := v.client.Call(ctx, deleteMethod, []any{resIDInt}, &result); err != nil {
			return fmt.Errorf("failed to delete %s %d: %w", resourceDesc, resIDInt, err)
		}
	}

	return nil
}

// toInt converts a JSON-unmarshaled value to int.
func toInt(v any) (int, error) {
	switch val := v.(type) {
	case float64:
		return int(val), nil
	case int:
		return val, nil
	case int64:
		return int(val), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// extractID extracts an ID from a value that can be a number or a nested {"id": N} object.
func extractID(v any) (int, error) {
	if m, ok := v.(map[string]any); ok {
		if id, exists := m["id"]; exists {
			return toInt(id)
		}
		return 0, fmt.Errorf("nested object has no 'id' field")
	}
	return toInt(v)
}

// DeleteNFSShare deletes an NFS share from NASty by path.
// This is used for cleaning up retained NFS shares after tests.
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
	// Not found, nothing to delete
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
	// Not found, nothing to delete
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
// Always returns false, nil.
func (v *NAStyVerifier) ISCSIExtentExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// DeleteISCSITarget deletes an iSCSI target from NASty by IQN.
// This is used for cleaning up retained iSCSI targets after tests.
func (v *NAStyVerifier) DeleteISCSITarget(ctx context.Context, iqn string) error {
	target, err := v.client.GetISCSITargetByIQN(ctx, iqn)
	if err != nil {
		return fmt.Errorf("failed to query iSCSI target: %w", err)
	}
	if target == nil {
		return nil // Target doesn't exist
	}
	return v.client.DeleteISCSITarget(ctx, target.ID)
}

// DeleteISCSIExtent is kept for backward compatibility — NASty does not have separate extents.
// This is a no-op.
func (v *NAStyVerifier) DeleteISCSIExtent(_ context.Context, _ string) error {
	return nil
}

// GetDatasetOrigin returns the origin of a dataset (if it's a clone).
// Returns empty string if the dataset is not a clone.
// The origin is the snapshot from which the clone was created.
func (v *NAStyVerifier) GetDatasetOrigin(ctx context.Context, datasetPath string) (string, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter}, &datasets); err != nil {
		return "", fmt.Errorf("failed to query dataset: %w", err)
	}
	if len(datasets) == 0 {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}

	dataset := datasets[0]
	// The origin property is returned as {"value": "pool/dataset@snapshot", "source": "local", ...}
	origin, ok := dataset["origin"]
	if !ok {
		return "", nil // No origin property
	}

	// Handle the origin structure
	if originMap, isMap := origin.(map[string]any); isMap {
		if val, hasValue := originMap["value"]; hasValue {
			if strVal, isStr := val.(string); isStr && strVal != "" && strVal != "-" {
				return strVal, nil
			}
		}
	}

	return "", nil // Not a clone
}

// IsDatasetClone checks if a dataset is a ZFS clone (has an origin).
func (v *NAStyVerifier) IsDatasetClone(ctx context.Context, datasetPath string) (isClone bool, origin string, err error) {
	origin, err = v.GetDatasetOrigin(ctx, datasetPath)
	if err != nil {
		return false, "", err
	}
	return origin != "", origin, nil
}

// ResourceSnapshot holds a point-in-time inventory of CSI-related NASty resources.
// Used for before/after comparison to detect resource leaks from test runs.
type ResourceSnapshot struct {
	Datasets     map[string]datasetInfo // dataset path -> info
	NFSShares    map[string]bool        // share path -> exists
	SMBShares    map[string]bool        // share path -> exists
	NVMeSubsNQNs map[string]bool        // subsystem NQN -> exists
	ISCSITargets map[string]bool        // target IQN -> exists
	ISCSIExtents map[string]bool        // extent name -> exists (legacy, always empty for NASty)
}

type datasetInfo struct {
	Protocol  string
	CreatedAt string
}

// SnapshotResources queries all CSI-related resource types and returns a point-in-time snapshot.
// Errors are logged but non-fatal — an incomplete snapshot is better than failing the suite.
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
		for _, sv := range subvols {
			info := datasetInfo{}
			if sv.Properties != nil {
				info.Protocol = sv.Properties[nastyapi.PropertyProtocol]
				info.CreatedAt = sv.Properties[nastyapi.PropertyCreatedAt]
			}
			snap.Datasets[sv.Pool+"/"+sv.Name] = info
		}
	}

	// NFS shares — filter to shares under the pool mount path
	nfsShares, err := v.client.ListNFSShares(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query NFS shares: %v", err)
	} else {
		mountPrefix := "/mnt/" + poolPrefix
		for _, s := range nfsShares {
			if strings.HasPrefix(s.Path, mountPrefix) {
				snap.NFSShares[s.Path] = true
			}
		}
	}

	// SMB shares — filter to shares under the pool mount path
	smbShares, err := v.client.ListSMBShares(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query SMB shares: %v", err)
	} else {
		smbMountPrefix := "/mnt/" + poolPrefix
		for _, s := range smbShares {
			if strings.HasPrefix(s.Path, smbMountPrefix) {
				snap.SMBShares[s.Path] = true
			}
		}
	}

	// NVMe-oF subsystems — filter to CSI-created ones (NQN contains "nasty-csi" or "pvc-")
	subsystems, err := v.client.ListNVMeOFSubsystems(ctx)
	if err != nil {
		klog.Warningf("Resource snapshot: failed to query NVMe-oF subsystems: %v", err)
	} else {
		for _, sub := range subsystems {
			nqn := sub.NQN
			if isCSIResource(nqn) {
				snap.NVMeSubsNQNs[nqn] = true
			}
		}
	}

	// iSCSI targets — filter to CSI-created ones (by IQN)
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

	// ISCSIExtents is always empty for NASty (no separate extents)

	return snap
}

// isCSIResource returns true if the resource name looks like it was created by the CSI driver.
func isCSIResource(name string) bool {
	return strings.Contains(name, "pvc-") || strings.Contains(name, "csi-") || strings.Contains(name, "nasty-csi")
}

// LogResourceDiff compares two snapshots and logs any resources present in "after" but not in "before" (leaks).
func LogResourceDiff(before, after *ResourceSnapshot) {
	var leaks []string

	// Datasets
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

	// NFS shares
	for path := range after.NFSShares {
		if !before.NFSShares[path] {
			leaks = append(leaks, "LEAKED NFS share: "+path)
		}
	}

	// SMB shares
	for path := range after.SMBShares {
		if !before.SMBShares[path] {
			leaks = append(leaks, "LEAKED SMB share: "+path)
		}
	}

	// NVMe-oF subsystems
	for nqn := range after.NVMeSubsNQNs {
		if !before.NVMeSubsNQNs[nqn] {
			leaks = append(leaks, "LEAKED NVMe-oF subsystem: "+nqn)
		}
	}

	// iSCSI targets
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

// GetDatasetProperty retrieves a specific ZFS user property from a dataset.
// Returns empty string if the property doesn't exist or is unset.
func (v *NAStyVerifier) GetDatasetProperty(ctx context.Context, datasetPath, propertyName string) (string, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	// Request user properties to be included in the response
	options := map[string]any{
		"extra": map[string]any{
			"user_properties": true,
		},
	}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter, options}, &datasets); err != nil {
		return "", fmt.Errorf("failed to query dataset: %w", err)
	}
	if len(datasets) == 0 {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}

	// User properties are returned under the "user_properties" key
	dataset := datasets[0]
	userProps, ok := dataset["user_properties"]
	if !ok {
		return "", nil // No user properties
	}

	// user_properties is a map of property name -> {value, source, ...}
	propsMap, ok := userProps.(map[string]any)
	if !ok {
		return "", nil // Unexpected format
	}

	propData, ok := propsMap[propertyName]
	if !ok {
		return "", nil // Property not set
	}

	// Property value is in the "value" field
	if propMap, isMap := propData.(map[string]any); isMap {
		if val, hasValue := propMap["value"]; hasValue {
			if strVal, isStr := val.(string); isStr {
				return strVal, nil
			}
		}
	}

	return "", nil // Property not set or unexpected format
}

// GetZFSProperty retrieves a native ZFS property (e.g., "compression", "recordsize", "atime", "volblocksize")
// from a dataset. Returns the parsed value as a string. Returns empty string if the property doesn't exist.
func (v *NAStyVerifier) GetZFSProperty(ctx context.Context, datasetPath, propertyName string) (string, error) {
	var datasets []map[string]any
	filter := []any{[]any{"id", "=", datasetPath}}
	if err := v.client.Call(ctx, "pool.dataset.query", []any{filter}, &datasets); err != nil {
		return "", fmt.Errorf("failed to query dataset: %w", err)
	}
	if len(datasets) == 0 {
		return "", fmt.Errorf("%s: %w", datasetPath, ErrDatasetNotFound)
	}

	dataset := datasets[0]
	propData, ok := dataset[propertyName]
	if !ok {
		return "", nil
	}

	// Native ZFS properties are returned as {"value": "lz4", "rawvalue": "lz4", "parsed": "...", "source": "LOCAL"}
	if propMap, isMap := propData.(map[string]any); isMap {
		if val, hasValue := propMap["value"]; hasValue {
			if strVal, isStr := val.(string); isStr {
				return strVal, nil
			}
		}
	}

	return "", nil
}
