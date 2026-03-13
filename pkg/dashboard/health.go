package dashboard

import (
	"context"
	"strings"

	"github.com/nasty-project/nasty-csi/pkg/nasty-api"
	"k8s.io/klog/v2"
)

// healthResourceMaps holds bulk-queried resource maps for health checking.
type healthResourceMaps struct {
	nfsShareMap    map[string]*nastyapi.NFSShare
	nvmeSubsysMap  map[string]*nastyapi.NVMeOFSubsystem
	smbShareMap    map[string]*nastyapi.SMBShare
	iscsiTargetMap map[string]*nastyapi.ISCSITarget
}

// buildHealthResourceMaps queries all protocol resources in bulk for health checking.
func buildHealthResourceMaps(ctx context.Context, client nastyapi.ClientInterface) *healthResourceMaps {
	m := &healthResourceMaps{
		nfsShareMap:    make(map[string]*nastyapi.NFSShare),
		nvmeSubsysMap:  make(map[string]*nastyapi.NVMeOFSubsystem),
		smbShareMap:    make(map[string]*nastyapi.SMBShare),
		iscsiTargetMap: make(map[string]*nastyapi.ISCSITarget),
	}

	nfsShares, err := client.ListNFSShares(ctx)
	if err == nil {
		for i := range nfsShares {
			m.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
		}
	}

	nvmeSubsystems, err := client.ListNVMeOFSubsystems(ctx)
	if err == nil {
		for i := range nvmeSubsystems {
			m.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
		}
	}

	smbShares, err := client.ListSMBShares(ctx)
	if err == nil {
		for i := range smbShares {
			m.smbShareMap[smbShares[i].Path] = &smbShares[i]
		}
	}

	iscsiTargets, err := client.ListISCSITargets(ctx)
	if err == nil {
		for i := range iscsiTargets {
			m.iscsiTargetMap[iscsiTargets[i].IQN] = &iscsiTargets[i]
		}
	}

	return m
}

// CheckVolumeHealth checks the health of all managed volumes.
func CheckVolumeHealth(ctx context.Context, client nastyapi.ClientInterface) (*HealthReport, error) {
	subvols, err := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
	if err != nil {
		return nil, err
	}

	resources := buildHealthResourceMaps(ctx, client)

	report := &HealthReport{
		Volumes:  make([]VolumeHealth, 0),
		Problems: make([]VolumeHealth, 0),
	}

	for i := range subvols {
		sv := &subvols[i]

		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			continue
		}

		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		if volumeID == "" {
			continue
		}

		health := VolumeHealth{
			VolumeID:  volumeID,
			Dataset:   sv.Pool + "/" + sv.Name,
			DatasetOK: true,
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		health.Protocol = sv.Properties[nastyapi.PropertyProtocol]

		switch health.Protocol {
		case protocolNFS:
			CheckNFSHealth(sv, resources.nfsShareMap, &health)
		case protocolNVMeOF:
			CheckNVMeOFHealth(sv, resources.nvmeSubsysMap, &health)
		case protocolSMB:
			CheckSMBHealth(sv, resources.smbShareMap, &health)
		case protocolISCSI:
			CheckISCSIHealth(sv, resources.iscsiTargetMap, &health)
		}

		if len(health.Issues) > 0 {
			health.Status = HealthStatusDegraded
			for _, issue := range health.Issues {
				issueLower := strings.ToLower(issue)
				if strings.Contains(issueLower, "not found") || strings.Contains(issueLower, "disabled") {
					health.Status = HealthStatusUnhealthy
					break
				}
			}
		}

		report.Summary.TotalVolumes++
		switch health.Status {
		case HealthStatusHealthy:
			report.Summary.HealthyVolumes++
		case HealthStatusDegraded:
			report.Summary.DegradedVolumes++
		case HealthStatusUnhealthy:
			report.Summary.UnhealthyVolumes++
		}

		report.Volumes = append(report.Volumes, health)
		if health.Status != HealthStatusHealthy {
			report.Problems = append(report.Problems, health)
		}
	}

	return report, nil
}

// CheckNFSHealth checks if the NFS share for a subvolume is healthy.
func CheckNFSHealth(sv *nastyapi.Subvolume, nfsShareMap map[string]*nastyapi.NFSShare, health *VolumeHealth) {
	sharePath := sv.Properties[nastyapi.PropertyNFSSharePath]
	if sharePath == "" {
		sharePath = sv.Path
	}

	if sharePath == "" {
		health.Issues = append(health.Issues, "NFS share path not found in properties")
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	share, exists := nfsShareMap[sharePath]
	if !exists {
		health.Issues = append(health.Issues, "NFS share not found for path "+sharePath)
		shareOK := false
		health.ShareOK = &shareOK
		return
	}

	shareOK := true
	if !share.Enabled {
		health.Issues = append(health.Issues, "NFS share is disabled")
		shareOK = false
	}
	health.ShareOK = &shareOK
}

// CheckNVMeOFHealth checks if the NVMe-oF subsystem for a subvolume is healthy.
func CheckNVMeOFHealth(sv *nastyapi.Subvolume, nvmeSubsysMap map[string]*nastyapi.NVMeOFSubsystem, health *VolumeHealth) {
	nqn := sv.Properties[nastyapi.PropertyNVMeSubsystemNQN]

	if nqn == "" {
		health.Issues = append(health.Issues, "NVMe-oF subsystem NQN not found in properties")
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	_, exists := nvmeSubsysMap[nqn]
	if !exists {
		health.Issues = append(health.Issues, "NVMe-oF subsystem not found: "+nqn)
		subsysOK := false
		health.SubsysOK = &subsysOK
		return
	}

	subsysOK := true
	health.SubsysOK = &subsysOK
}

// CheckSMBHealth checks if the SMB share for a subvolume is healthy.
func CheckSMBHealth(sv *nastyapi.Subvolume, smbShareMap map[string]*nastyapi.SMBShare, health *VolumeHealth) {
	sharePath := sv.Path

	if sharePath == "" {
		health.Issues = append(health.Issues, "SMB share path not found")
		smbOK := false
		health.SMBShareOK = &smbOK
		return
	}

	share, exists := smbShareMap[sharePath]
	if !exists {
		health.Issues = append(health.Issues, "SMB share not found for path "+sharePath)
		smbOK := false
		health.SMBShareOK = &smbOK
		return
	}

	smbOK := true
	if !share.Enabled {
		health.Issues = append(health.Issues, "SMB share is disabled")
		smbOK = false
	}
	health.SMBShareOK = &smbOK
}

// CheckISCSIHealth checks if the iSCSI target for a subvolume is healthy.
func CheckISCSIHealth(sv *nastyapi.Subvolume, iscsiTargetMap map[string]*nastyapi.ISCSITarget, health *VolumeHealth) {
	iqn := sv.Properties[nastyapi.PropertyISCSIIQN]

	if iqn == "" {
		health.Issues = append(health.Issues, "iSCSI IQN not found in properties")
		targetOK := false
		health.TargetOK = &targetOK
		return
	}

	_, exists := iscsiTargetMap[iqn]
	if !exists {
		health.Issues = append(health.Issues, "iSCSI target not found: "+iqn)
		targetOK := false
		health.TargetOK = &targetOK
		return
	}

	targetOK := true
	health.TargetOK = &targetOK
}

// BuildHealthMapsFromData builds health resource maps from pre-fetched data (no API calls).
func BuildHealthMapsFromData(
	nfsShares []nastyapi.NFSShare,
	smbShares []nastyapi.SMBShare,
	nvmeSubsystems []nastyapi.NVMeOFSubsystem,
	iscsiTargets []nastyapi.ISCSITarget,
) *healthResourceMaps {
	m := &healthResourceMaps{
		nfsShareMap:    make(map[string]*nastyapi.NFSShare, len(nfsShares)),
		nvmeSubsysMap:  make(map[string]*nastyapi.NVMeOFSubsystem, len(nvmeSubsystems)),
		smbShareMap:    make(map[string]*nastyapi.SMBShare, len(smbShares)),
		iscsiTargetMap: make(map[string]*nastyapi.ISCSITarget, len(iscsiTargets)),
	}

	for i := range nfsShares {
		m.nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}
	for i := range nvmeSubsystems {
		m.nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}
	for i := range smbShares {
		m.smbShareMap[smbShares[i].Path] = &smbShares[i]
	}
	for i := range iscsiTargets {
		m.iscsiTargetMap[iscsiTargets[i].IQN] = &iscsiTargets[i]
	}

	return m
}

// AnnotateHealthFromMaps annotates volumes with health status using pre-fetched resource maps and subvolumes.
func AnnotateHealthFromMaps(volumes []VolumeInfo, managedSubvols []nastyapi.Subvolume, resources *healthResourceMaps) {
	subvolMap := make(map[string]*nastyapi.Subvolume, len(managedSubvols))
	for i := range managedSubvols {
		sv := &managedSubvols[i]
		if sv.Properties[nastyapi.PropertyDetachedSnapshot] == valueTrue {
			continue
		}
		volumeID := sv.Properties[nastyapi.PropertyCSIVolumeName]
		if volumeID != "" {
			subvolMap[volumeID] = sv
		}
	}

	for i := range volumes {
		sv, ok := subvolMap[volumes[i].VolumeID]
		if !ok {
			continue
		}

		health := VolumeHealth{
			VolumeID:  volumes[i].VolumeID,
			Dataset:   sv.Pool + "/" + sv.Name,
			DatasetOK: true,
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		protocol := sv.Properties[nastyapi.PropertyProtocol]

		switch protocol {
		case protocolNFS:
			CheckNFSHealth(sv, resources.nfsShareMap, &health)
		case protocolNVMeOF:
			CheckNVMeOFHealth(sv, resources.nvmeSubsysMap, &health)
		case protocolSMB:
			CheckSMBHealth(sv, resources.smbShareMap, &health)
		case protocolISCSI:
			CheckISCSIHealth(sv, resources.iscsiTargetMap, &health)
		}

		if len(health.Issues) > 0 {
			health.Status = HealthStatusDegraded
			for _, issue := range health.Issues {
				issueLower := strings.ToLower(issue)
				if strings.Contains(issueLower, "not found") || strings.Contains(issueLower, "disabled") {
					health.Status = HealthStatusUnhealthy
					break
				}
			}
		}

		volumes[i].HealthStatus = string(health.Status)
		if len(health.Issues) > 0 {
			volumes[i].HealthIssue = health.Issues[0]
		}
	}
}

// AnnotateVolumesWithHealth runs health checks and annotates VolumeInfo slices with health status.
func AnnotateVolumesWithHealth(ctx context.Context, client nastyapi.ClientInterface, volumes []VolumeInfo) {
	healthReport, err := CheckVolumeHealth(ctx, client)
	if err != nil {
		klog.Warningf("Failed to check volume health: %v", err)
		return
	}

	healthMap := make(map[string]*VolumeHealth, len(healthReport.Volumes))
	for i := range healthReport.Volumes {
		healthMap[healthReport.Volumes[i].VolumeID] = &healthReport.Volumes[i]
	}

	for i := range volumes {
		if h, ok := healthMap[volumes[i].VolumeID]; ok {
			volumes[i].HealthStatus = string(h.Status)
			if len(h.Issues) > 0 {
				volumes[i].HealthIssue = h.Issues[0]
			}
		}
	}
}
