package dashboard

import (
	"context"
	"strings"

	"github.com/fenio/tns-csi/pkg/tnsapi"
	"k8s.io/klog/v2"
)

// CheckVolumeHealth checks the health of all managed volumes.
func CheckVolumeHealth(ctx context.Context, client tnsapi.ClientInterface) (*HealthReport, error) {
	datasets, err := client.FindDatasetsByProperty(ctx, "", tnsapi.PropertyManagedBy, tnsapi.ManagedByValue)
	if err != nil {
		return nil, err
	}

	nfsShares, err := client.QueryAllNFSShares(ctx, "")
	if err != nil {
		nfsShares = nil
	}
	nfsShareMap := make(map[string]*tnsapi.NFSShare)
	for i := range nfsShares {
		nfsShareMap[nfsShares[i].Path] = &nfsShares[i]
	}

	nvmeSubsystems, err := client.ListAllNVMeOFSubsystems(ctx)
	if err != nil {
		nvmeSubsystems = nil
	}
	nvmeSubsysMap := make(map[string]*tnsapi.NVMeOFSubsystem)
	for i := range nvmeSubsystems {
		nvmeSubsysMap[nvmeSubsystems[i].Name] = &nvmeSubsystems[i]
		nvmeSubsysMap[nvmeSubsystems[i].NQN] = &nvmeSubsystems[i]
	}

	smbShares, err := client.QueryAllSMBShares(ctx, "")
	if err != nil {
		smbShares = nil
	}
	smbShareMap := make(map[string]*tnsapi.SMBShare)
	for i := range smbShares {
		smbShareMap[smbShares[i].Path] = &smbShares[i]
	}

	report := &HealthReport{
		Volumes:  make([]VolumeHealth, 0),
		Problems: make([]VolumeHealth, 0),
	}

	for i := range datasets {
		ds := &datasets[i]

		if prop, ok := ds.UserProperties[tnsapi.PropertyDetachedSnapshot]; ok && prop.Value == valueTrue {
			continue
		}

		volumeID := ""
		if prop, ok := ds.UserProperties[tnsapi.PropertyCSIVolumeName]; ok {
			volumeID = prop.Value
		}
		if volumeID == "" {
			continue
		}

		health := VolumeHealth{
			VolumeID:  volumeID,
			Dataset:   ds.ID,
			DatasetOK: true,
			Status:    HealthStatusHealthy,
			Issues:    make([]string, 0),
		}

		if prop, ok := ds.UserProperties[tnsapi.PropertyProtocol]; ok {
			health.Protocol = prop.Value
		}

		switch health.Protocol {
		case protocolNFS:
			CheckNFSHealth(ds, nfsShareMap, &health)
		case protocolNVMeOF:
			CheckNVMeOFHealth(ds, nvmeSubsysMap, &health)
		case protocolSMB:
			CheckSMBHealth(ds, smbShareMap, &health)
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

// CheckNFSHealth checks if the NFS share for a dataset is healthy.
func CheckNFSHealth(ds *tnsapi.DatasetWithProperties, nfsShareMap map[string]*tnsapi.NFSShare, health *VolumeHealth) {
	sharePath := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNFSSharePath]; ok {
		sharePath = prop.Value
	} else if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
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

// CheckNVMeOFHealth checks if the NVMe-oF subsystem for a dataset is healthy.
func CheckNVMeOFHealth(ds *tnsapi.DatasetWithProperties, nvmeSubsysMap map[string]*tnsapi.NVMeOFSubsystem, health *VolumeHealth) {
	nqn := ""
	if prop, ok := ds.UserProperties[tnsapi.PropertyNVMeSubsystemNQN]; ok {
		nqn = prop.Value
	}

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

// CheckSMBHealth checks if the SMB share for a dataset is healthy.
func CheckSMBHealth(ds *tnsapi.DatasetWithProperties, smbShareMap map[string]*tnsapi.SMBShare, health *VolumeHealth) {
	sharePath := ""
	if ds.Mountpoint != "" {
		sharePath = ds.Mountpoint
	}

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

// AnnotateVolumesWithHealth runs health checks and annotates VolumeInfo slices with health status.
func AnnotateVolumesWithHealth(ctx context.Context, client tnsapi.ClientInterface, volumes []VolumeInfo) {
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
