// Package dashboard provides shared dashboard data types and functions.
package dashboard

// Type aliases re-exported from nasty-go/dashboard so the rest of this package
// can reference them without qualification. External consumers (e.g. tests, the
// driver) should import github.com/nasty-project/nasty-go/dashboard directly.

import dashlib "github.com/nasty-project/nasty-go/dashboard"

type (
	// Data holds the full dashboard data payload.
	Data = dashlib.Data
	// SummaryData holds aggregated summary statistics.
	SummaryData = dashlib.SummaryData
	// VolumeInfo holds volume information for the dashboard.
	VolumeInfo = dashlib.VolumeInfo
	// SnapshotInfo holds snapshot information for the dashboard.
	SnapshotInfo = dashlib.SnapshotInfo
	// CloneInfo holds clone information for the dashboard.
	CloneInfo = dashlib.CloneInfo
	// UnmanagedVolume holds information about non-CSI-managed volumes.
	UnmanagedVolume = dashlib.UnmanagedVolume
	// HealthStatus represents the health status of a resource.
	HealthStatus = dashlib.HealthStatus
	// VolumeHealth holds health information for a volume.
	VolumeHealth = dashlib.VolumeHealth
	// HealthReport holds a full health report.
	HealthReport = dashlib.HealthReport
	// HealthSummary holds aggregated health statistics.
	HealthSummary = dashlib.HealthSummary
	// HealthResourceMaps holds resource maps for health checking.
	HealthResourceMaps = dashlib.HealthResourceMaps
	// K8sVolumeBinding holds Kubernetes PV/PVC binding information.
	K8sVolumeBinding = dashlib.K8sVolumeBinding
	// K8sEnrichmentResult holds the result of Kubernetes data enrichment.
	K8sEnrichmentResult = dashlib.K8sEnrichmentResult
	// VolumeDetails holds detailed volume information.
	VolumeDetails = dashlib.VolumeDetails
	// NFSShareDetails holds NFS share details.
	NFSShareDetails = dashlib.NFSShareDetails
	// NVMeOFSubsystemDetails holds NVMe-oF subsystem details.
	NVMeOFSubsystemDetails = dashlib.NVMeOFSubsystemDetails
	// SMBShareDetails holds SMB share details.
	SMBShareDetails = dashlib.SMBShareDetails
	// ISCSITargetDetails holds iSCSI target details.
	ISCSITargetDetails = dashlib.ISCSITargetDetails
	// MetricsSummary holds metrics summary data.
	MetricsSummary = dashlib.MetricsSummary
	// PaginationParams holds pagination parameters.
	PaginationParams = dashlib.PaginationParams
	// PaginatedVolumes holds a paginated list of volumes.
	PaginatedVolumes = dashlib.PaginatedVolumes
	// PaginatedSnapshots holds a paginated list of snapshots.
	PaginatedSnapshots = dashlib.PaginatedSnapshots
	// PaginatedClones holds a paginated list of clones.
	PaginatedClones = dashlib.PaginatedClones
	// PaginatedUnmanaged holds a paginated list of unmanaged volumes.
	PaginatedUnmanaged = dashlib.PaginatedUnmanaged
)

const (
	// HealthStatusHealthy indicates a healthy status.
	HealthStatusHealthy = dashlib.HealthStatusHealthy
	// HealthStatusDegraded indicates a degraded status.
	HealthStatusDegraded = dashlib.HealthStatusDegraded
	// HealthStatusUnhealthy indicates an unhealthy status.
	HealthStatusUnhealthy = dashlib.HealthStatusUnhealthy
)

// Re-export functions from nasty-go/dashboard used within this package.
var (
	FindManagedVolumes        = dashlib.FindManagedVolumes
	FindManagedSnapshots      = dashlib.FindManagedSnapshots
	FindClonedVolumes         = dashlib.FindClonedVolumes
	FindUnmanagedVolumes      = dashlib.FindUnmanagedVolumes
	GetVolumeDetails          = dashlib.GetVolumeDetails
	FormatBytes               = dashlib.FormatBytes
	CheckVolumeHealth         = dashlib.CheckVolumeHealth
	CheckNFSHealth            = dashlib.CheckNFSHealth
	CheckNVMeOFHealth         = dashlib.CheckNVMeOFHealth
	CheckSMBHealth            = dashlib.CheckSMBHealth
	CheckISCSIHealth          = dashlib.CheckISCSIHealth
	BuildHealthMapsFromData   = dashlib.BuildHealthMapsFromData
	AnnotateHealthFromMaps    = dashlib.AnnotateHealthFromMaps
	AnnotateVolumesWithHealth = dashlib.AnnotateVolumesWithHealth
	ParsePaginationParams     = dashlib.ParsePaginationParams
	PaginateVolumes           = dashlib.PaginateVolumes
	PaginateSnapshots         = dashlib.PaginateSnapshots
	PaginateClones            = dashlib.PaginateClones
	PaginateUnmanaged         = dashlib.PaginateUnmanaged
	CalculateSummary          = dashlib.CalculateSummary
	MatchK8sBinding           = dashlib.MatchK8sBinding
)

// Protocol constants forwarded for use within this package.
const (
	protocolNFS    = dashlib.ProtocolNFS
	protocolNVMeOF = dashlib.ProtocolNVMeOF
	protocolISCSI  = dashlib.ProtocolISCSI
	protocolSMB    = dashlib.ProtocolSMB
)
