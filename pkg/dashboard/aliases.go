// Package dashboard provides shared dashboard data types and functions.
package dashboard

// Type aliases re-exported from nasty-go/dashboard so the rest of this package
// can reference them without qualification. External consumers (e.g. tests, the
// driver) should import github.com/nasty-project/nasty-go/dashboard directly.

import dashlib "github.com/nasty-project/nasty-go/dashboard"

type (
	Data                   = dashlib.Data
	SummaryData            = dashlib.SummaryData
	VolumeInfo             = dashlib.VolumeInfo
	SnapshotInfo           = dashlib.SnapshotInfo
	CloneInfo              = dashlib.CloneInfo
	UnmanagedVolume        = dashlib.UnmanagedVolume
	HealthStatus           = dashlib.HealthStatus
	VolumeHealth           = dashlib.VolumeHealth
	HealthReport           = dashlib.HealthReport
	HealthSummary          = dashlib.HealthSummary
	HealthResourceMaps     = dashlib.HealthResourceMaps
	K8sVolumeBinding       = dashlib.K8sVolumeBinding
	K8sEnrichmentResult    = dashlib.K8sEnrichmentResult
	VolumeDetails          = dashlib.VolumeDetails
	NFSShareDetails        = dashlib.NFSShareDetails
	NVMeOFSubsystemDetails = dashlib.NVMeOFSubsystemDetails
	SMBShareDetails        = dashlib.SMBShareDetails
	ISCSITargetDetails     = dashlib.ISCSITargetDetails
	MetricsSummary         = dashlib.MetricsSummary
	PaginationParams       = dashlib.PaginationParams
	PaginatedVolumes       = dashlib.PaginatedVolumes
	PaginatedSnapshots     = dashlib.PaginatedSnapshots
	PaginatedClones        = dashlib.PaginatedClones
	PaginatedUnmanaged     = dashlib.PaginatedUnmanaged
)

const (
	// HealthStatusHealthy indicates a healthy status.
	HealthStatusHealthy   = dashlib.HealthStatusHealthy
	HealthStatusDegraded  = dashlib.HealthStatusDegraded
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
