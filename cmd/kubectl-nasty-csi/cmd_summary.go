package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nasty-project/nasty-csi/pkg/dashboard"
	nastyapi "github.com/nasty-project/nasty-go"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Icon constants for status display.
const (
	iconOK      = "✓"
	iconError   = "✗"
	iconWarning = "!"
)

// Summary contains the overall summary of nasty-csi managed resources.
//
//nolint:govet // field alignment not critical for CLI output struct
type Summary struct {
	Volumes      VolumeSummary   `json:"volumes"                yaml:"volumes"`
	Snapshots    SnapshotSummary `json:"snapshots"              yaml:"snapshots"`
	Capacity     CapacitySummary `json:"capacity"               yaml:"capacity"`
	Health       HealthSummary   `json:"health"                 yaml:"health"`
	HealthIssues []string        `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`
}

// VolumeSummary contains volume statistics.
type VolumeSummary struct {
	Total  int `json:"total"  yaml:"total"`
	NFS    int `json:"nfs"    yaml:"nfs"`
	NVMeOF int `json:"nvmeof" yaml:"nvmeof"`
	ISCSI  int `json:"iscsi"  yaml:"iscsi"`
	SMB    int `json:"smb"    yaml:"smb"`
	Clones int `json:"clones" yaml:"clones"`
}

// SnapshotSummary contains snapshot statistics.
type SnapshotSummary struct {
	Total    int `json:"total"    yaml:"total"`
	Attached int `json:"attached" yaml:"attached"`
	Detached int `json:"detached" yaml:"detached"`
}

// CapacitySummary contains capacity statistics.
//
//nolint:govet // field alignment not critical for CLI output struct
type CapacitySummary struct {
	ProvisionedBytes int64  `json:"provisionedBytes" yaml:"provisionedBytes"`
	ProvisionedHuman string `json:"provisionedHuman" yaml:"provisionedHuman"`
	UsedBytes        int64  `json:"usedBytes"        yaml:"usedBytes"`
	UsedHuman        string `json:"usedHuman"        yaml:"usedHuman"`
}

// Note: HealthSummary is already defined in cmd_health.go

func newSummaryCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show summary of all nasty-csi managed resources",
		Long: `Display a dashboard-style summary of all nasty-csi managed resources.

Shows:
  - Volume counts by protocol (NFS, NVMe-oF)
  - Snapshot counts (attached vs detached)
  - Total capacity (provisioned and used)
  - Health status breakdown

Examples:
  # Show summary
  kubectl nasty-csi summary

  # Output as JSON for scripting
  kubectl nasty-csi summary -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSummary(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify)
		},
	}
	return cmd
}

func runSummary(ctx context.Context, url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) error {
	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Show spinner while connecting and gathering data
	spin := newSpinner("Connecting to NASty...")
	client, err := connectToNASty(ctx, cfg)
	if err != nil {
		spin.stop()
		return err
	}
	defer client.Close()

	// Gather summary
	summary, err := gatherSummary(ctx, client)
	spin.stop()
	if err != nil {
		return fmt.Errorf("failed to gather summary: %w", err)
	}

	// Output based on format
	return outputSummary(summary, *outputFormat)
}

// summaryContext holds data needed for summary collection.
type summaryContext struct {
	client         nastyapi.ClientInterface
	nfsShareMap    map[string]*nastyapi.NFSShare
	nvmeSubsysMap  map[string]*nastyapi.NVMeOFSubsystem
	iscsiTargetMap map[string]*nastyapi.ISCSITarget
	smbShareMap    map[string]*nastyapi.SMBShare
}

// gatherSummary collects all summary statistics.
func gatherSummary(ctx context.Context, client nastyapi.ClientInterface) (*Summary, error) {
	summary := &Summary{}

	// Get all managed subvolumes
	subvols, err := client.FindManagedSubvolumes(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to query subvolumes: %w", err)
	}

	// Build lookup maps for health checks
	sc := buildSummaryContext(ctx, client)

	// Process volume subvolumes
	processVolumeSubvolumes(subvols, sc, summary)

	// Count attached snapshots from subvolume Snapshots field
	countAttachedSnapshots(subvols, summary)

	// Finalize summary
	summary.Snapshots.Total = summary.Snapshots.Attached + summary.Snapshots.Detached
	summary.Capacity.ProvisionedHuman = dashboard.FormatBytes(summary.Capacity.ProvisionedBytes)
	summary.Capacity.UsedHuman = dashboard.FormatBytes(summary.Capacity.UsedBytes)
	summary.Health.TotalVolumes = summary.Volumes.Total

	return summary, nil
}

// buildSummaryContext creates lookup maps for NFS shares and NVMe subsystems.
func buildSummaryContext(ctx context.Context, client nastyapi.ClientInterface) *summaryContext {
	sc := &summaryContext{
		client:         client,
		nfsShareMap:    make(map[string]*nastyapi.NFSShare),
		nvmeSubsysMap:  make(map[string]*nastyapi.NVMeOFSubsystem),
		iscsiTargetMap: make(map[string]*nastyapi.ISCSITarget),
		smbShareMap:    make(map[string]*nastyapi.SMBShare),
	}

	// Get all NFS shares for health checks (ignore errors - non-critical)
	nfsShares, _ := client.ListNFSShares(ctx) //nolint:errcheck // non-critical for summary
	for i := range nfsShares {
		sc.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}

	// Get all NVMe-oF subsystems for health checks (ignore errors - non-critical)
	nvmeSubsystems, _ := client.ListNVMeOFSubsystems(ctx) //nolint:errcheck // non-critical for summary
	for i := range nvmeSubsystems {
		sc.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}

	// Get all iSCSI targets for health checks (ignore errors - non-critical)
	iscsiTargets, _ := client.ListISCSITargets(ctx) //nolint:errcheck // non-critical for summary
	for i := range iscsiTargets {
		sc.iscsiTargetMap[iscsiTargets[i].IQN] = &iscsiTargets[i]
	}

	// Get all SMB shares for health checks (ignore errors - non-critical)
	smbShares, _ := client.ListSMBShares(ctx) //nolint:errcheck // non-critical for summary
	for i := range smbShares {
		sc.smbShareMap[smbShares[i].Path] = &smbShares[i]
	}

	return sc
}

// processVolumeSubvolumes processes all subvolumes and updates summary counters.
func processVolumeSubvolumes(subvols []nastyapi.Subvolume, sc *summaryContext, summary *Summary) {
	for i := range subvols {
		sv := &subvols[i]

		// Skip detached snapshots (they're counted separately)
		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			summary.Snapshots.Detached++
			continue
		}

		// Skip subvolumes without volume ID (not actual volumes)
		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		if volumeID == "" {
			continue
		}

		// Count and categorize this volume
		processSubvolume(sv, sc, summary)
	}
}

// processSubvolume processes a single subvolume.
func processSubvolume(sv *nastyapi.Subvolume, sc *summaryContext, summary *Summary) {
	summary.Volumes.Total++

	// Get protocol
	protocol := sv.Properties[nastyapi.PropertyProtocol]

	// Count by protocol
	switch protocol {
	case protocolNFS:
		summary.Volumes.NFS++
	case protocolNVMeOF:
		summary.Volumes.NVMeOF++
	case protocolISCSI:
		summary.Volumes.ISCSI++
	case protocolSMB:
		summary.Volumes.SMB++
	}

	// Check if it's a clone
	if sv.Properties[nastyapi.PropertyContentSourceType] != "" {
		summary.Volumes.Clones++
	}

	// Add capacity
	if capStr := sv.Properties[nastyapi.PropertyCapacityBytes]; capStr != "" {
		summary.Capacity.ProvisionedBytes += nastyapi.StringToInt64(capStr)
	}

	// Add used space
	if sv.UsedBytes != nil {
		summary.Capacity.UsedBytes += int64(*sv.UsedBytes)
	}

	// Check health
	issue := checkVolumeHealthForSummary(sv, protocol, sc)
	if issue == "" {
		summary.Health.HealthyVolumes++
	} else {
		summary.Health.UnhealthyVolumes++
		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		summary.HealthIssues = append(summary.HealthIssues,
			fmt.Sprintf("%s (%s): %s", volumeID, protocol, issue))
	}
}

// checkVolumeHealthForSummary checks if a volume is healthy based on protocol.
// Returns empty string if healthy, or a short issue description.
func checkVolumeHealthForSummary(sv *nastyapi.Subvolume, protocol string, sc *summaryContext) string {
	switch protocol {
	case protocolNFS:
		return checkNFSHealthForSummary(sv, sc.nfsShareMap)
	case protocolNVMeOF:
		return checkNVMeOFHealthForSummary(sv, sc.nvmeSubsysMap)
	case protocolISCSI:
		return checkISCSIHealthForSummary(sv, sc.iscsiTargetMap)
	case protocolSMB:
		return checkSMBHealthForSummary(sv, sc.smbShareMap)
	default:
		return "" // Unknown protocol - assume healthy
	}
}

// countAttachedSnapshots counts snapshots on managed subvolumes using the Snapshots field.
func countAttachedSnapshots(subvols []nastyapi.Subvolume, summary *Summary) {
	for i := range subvols {
		sv := &subvols[i]

		// Skip detached snapshots
		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			continue
		}
		// Skip non-volumes
		if sv.Properties[nastyapi.PropertyCSIVolumeName] == "" {
			continue
		}

		// Count snapshots from the Snapshots field
		summary.Snapshots.Attached += len(sv.Snapshots)
	}
}

// checkNFSHealthForSummary checks if NFS volume is healthy.
// Returns empty string if healthy, or a short issue description.
func checkNFSHealthForSummary(sv *nastyapi.Subvolume, nfsShareMap map[string]*nastyapi.NFSShare) string {
	sharePath := sv.Properties[nastyapi.PropertyNFSSharePath]
	if sharePath == "" {
		sharePath = sv.Path
	}

	if sharePath == "" {
		return "no NFS share path configured"
	}

	share, exists := nfsShareMap[sharePath]
	if !exists {
		return "NFS share not found"
	}

	if !share.Enabled {
		return "NFS share disabled"
	}

	return ""
}

// checkNVMeOFHealthForSummary checks if NVMe-oF volume is healthy.
// Returns empty string if healthy, or a short issue description.
func checkNVMeOFHealthForSummary(sv *nastyapi.Subvolume, nvmeSubsysMap map[string]*nastyapi.NVMeOFSubsystem) string {
	nqn := sv.Properties[nastyapi.PropertyNVMeSubsystemNQN]

	if nqn == "" {
		return "no NVMe-oF subsystem NQN configured"
	}

	_, exists := nvmeSubsysMap[nqn]
	if !exists {
		return "NVMe-oF subsystem not found"
	}

	return ""
}

// checkISCSIHealthForSummary checks if iSCSI volume is healthy.
// Returns empty string if healthy, or a short issue description.
func checkISCSIHealthForSummary(sv *nastyapi.Subvolume, iscsiTargetMap map[string]*nastyapi.ISCSITarget) string {
	iqn := sv.Properties[nastyapi.PropertyISCSIIQN]

	if iqn == "" {
		return "no iSCSI IQN configured"
	}

	if _, exists := iscsiTargetMap[iqn]; exists {
		return ""
	}

	return "iSCSI target not found"
}

// checkSMBHealthForSummary checks if SMB volume is healthy.
// Returns empty string if healthy, or a short issue description.
func checkSMBHealthForSummary(sv *nastyapi.Subvolume, smbShareMap map[string]*nastyapi.SMBShare) string {
	sharePath := sv.Path

	if sharePath == "" {
		return "no SMB share path configured"
	}

	share, exists := smbShareMap[sharePath]
	if !exists {
		return "SMB share not found"
	}

	if !share.Enabled {
		return "SMB share disabled"
	}

	return ""
}

// outputSummary outputs the summary in the specified format.
func outputSummary(summary *Summary, format string) error {
	switch format {
	case outputFormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)

	case outputFormatYAML:
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(summary)

	case outputFormatTable, "":
		return outputSummaryTable(summary)

	default:
		return fmt.Errorf("%w: %s", errUnknownOutputFormat, format)
	}
}

// outputSummaryTable outputs the summary in a clean format.
func outputSummaryTable(summary *Summary) error {
	colorHeader.Println("=== NASty CSI Summary ===") //nolint:errcheck,gosec
	fmt.Println()

	// Volumes section
	colorHeader.Println("Volumes") //nolint:errcheck,gosec
	volLine := fmt.Sprintf("  Total: %-5d", summary.Volumes.Total)
	if summary.Volumes.NFS > 0 {
		volLine += fmt.Sprintf(" %s: %-5d", colorProtocolNFS.Sprint("NFS"), summary.Volumes.NFS)
	}
	if summary.Volumes.NVMeOF > 0 {
		volLine += fmt.Sprintf(" %s: %-5d", colorProtocolNVMe.Sprint("NVMe-oF"), summary.Volumes.NVMeOF)
	}
	if summary.Volumes.ISCSI > 0 {
		volLine += fmt.Sprintf(" %s: %-5d", colorProtocolISCI.Sprint("iSCSI"), summary.Volumes.ISCSI)
	}
	if summary.Volumes.SMB > 0 {
		volLine += fmt.Sprintf(" %s: %-5d", colorProtocolSMB.Sprint("SMB"), summary.Volumes.SMB)
	}
	if summary.Volumes.Clones > 0 {
		volLine += fmt.Sprintf(" Clones: %d", summary.Volumes.Clones)
	}
	fmt.Println(volLine)
	fmt.Println()

	// Snapshots section
	colorHeader.Println("Snapshots") //nolint:errcheck,gosec
	fmt.Printf("  Total: %-6d Attached: %-6d Detached: %d\n",
		summary.Snapshots.Total, summary.Snapshots.Attached, summary.Snapshots.Detached)
	fmt.Println()

	// Capacity section
	colorHeader.Println("Capacity") //nolint:errcheck,gosec
	usagePercent := 0.0
	if summary.Capacity.ProvisionedBytes > 0 {
		usagePercent = float64(summary.Capacity.UsedBytes) / float64(summary.Capacity.ProvisionedBytes) * 100
	}
	var percentStr string
	switch {
	case usagePercent >= 90:
		percentStr = colorError.Sprintf("%.1f%%", usagePercent)
	case usagePercent >= 70:
		percentStr = colorWarning.Sprintf("%.1f%%", usagePercent)
	default:
		percentStr = colorSuccess.Sprintf("%.1f%%", usagePercent)
	}
	fmt.Printf("  Provisioned: %-10s Used: %-10s (%s)\n",
		summary.Capacity.ProvisionedHuman, summary.Capacity.UsedHuman, percentStr)
	fmt.Println()

	// Health section
	colorHeader.Println("Health") //nolint:errcheck,gosec
	var healthIconStr string
	switch {
	case summary.Health.UnhealthyVolumes > 0:
		healthIconStr = colorError.Sprint(iconError)
	case summary.Health.DegradedVolumes > 0:
		healthIconStr = colorWarning.Sprint(iconWarning)
	default:
		healthIconStr = colorSuccess.Sprint(iconOK)
	}
	healthLine := "  " + healthIconStr + " Healthy: " + colorSuccess.Sprintf("%d", summary.Health.HealthyVolumes)
	if summary.Health.DegradedVolumes > 0 {
		healthLine += "  Degraded: " + colorWarning.Sprintf("%d", summary.Health.DegradedVolumes)
	}
	if summary.Health.UnhealthyVolumes > 0 {
		healthLine += "  Unhealthy: " + colorError.Sprintf("%d", summary.Health.UnhealthyVolumes)
	}
	fmt.Println(healthLine)

	// Show details for unhealthy volumes
	if summary.Health.UnhealthyVolumes > 0 {
		fmt.Println()
		colorWarning.Printf("⚠  %d volume(s) unhealthy:\n", summary.Health.UnhealthyVolumes) //nolint:errcheck,gosec
		for _, issue := range summary.HealthIssues {
			fmt.Printf("  %s %s\n", colorError.Sprint("-"), issue)
		}
		fmt.Println()
		colorMuted.Println("Run 'kubectl nasty-csi health' for full details.") //nolint:errcheck,gosec
	}

	return nil
}
