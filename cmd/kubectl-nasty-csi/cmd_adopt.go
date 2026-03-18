package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	nastyapi "github.com/nasty-project/nasty-go"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Static errors for adopt command.
var (
	errDatasetNotFound    = errors.New("dataset not found")
	errNoUserProperties   = errors.New("no user properties found")
	errNotManagedByNastyCSI = errors.New("not managed by nasty-csi")
)

func newAdoptCmd(url, apiKey, secretRef, outputFormat *string, skipTLSVerify *bool) *cobra.Command {
	var (
		pvcName      string
		namespace    string
		storageClass string
		accessMode   string
	)

	cmd := &cobra.Command{
		Use:   "adopt <dataset-path>",
		Short: "Generate static PV/PVC manifests to adopt an orphaned volume",
		Long: `Generate Kubernetes PersistentVolume and PersistentVolumeClaim manifests
for adopting an orphaned volume into the cluster.

The generated manifests use the static provisioning pattern - the PV references
the existing NASty dataset, and the PVC binds to it.

Examples:
  # Generate manifests for a specific dataset
  kubectl nasty-csi adopt tank/csi/pvc-abc123 --pvc-name my-data --namespace default

  # Use stored PVC metadata from volume properties
  kubectl nasty-csi adopt tank/csi/pvc-abc123

  # Output as single YAML document
  kubectl nasty-csi adopt tank/csi/pvc-abc123 -o yaml > adopt.yaml
  kubectl apply -f adopt.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			datasetPath := args[0]
			return runAdopt(cmd.Context(), url, apiKey, secretRef, outputFormat, skipTLSVerify,
				datasetPath, pvcName, namespace, storageClass, accessMode)
		},
	}

	cmd.Flags().StringVar(&pvcName, "pvc-name", "", "PVC name (defaults to volume's stored pvc_name or volume ID)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace for the PVC")
	cmd.Flags().StringVar(&storageClass, "storage-class", "", "StorageClass name (defaults to volume's stored storage_class)")
	cmd.Flags().StringVar(&accessMode, "access-mode", "", "Access mode: ReadWriteOnce, ReadWriteMany (auto-detected from protocol)")

	return cmd
}

func runAdopt(ctx context.Context, url, apiKey, secretRef, _ *string, skipTLSVerify *bool,
	datasetPath, pvcName, namespace, storageClass, accessMode string) error {

	// Get connection config
	cfg, err := getConnectionConfig(ctx, url, apiKey, secretRef, skipTLSVerify)
	if err != nil {
		return err
	}

	// Connect to NASty
	client, err := connectToNASty(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	// Get dataset info
	dataset, err := getDatasetWithProperties(ctx, client, datasetPath)
	if err != nil {
		return fmt.Errorf("failed to get dataset %s: %w", datasetPath, err)
	}

	// Extract volume metadata
	volumeInfo, err := extractVolumeInfo(dataset)
	if err != nil {
		return fmt.Errorf("dataset %s is not a valid nasty-csi volume: %w", datasetPath, err)
	}

	// Apply overrides from flags
	if pvcName != "" {
		volumeInfo.pvcName = pvcName
	}
	if namespace != "" {
		volumeInfo.namespace = namespace
	}
	if storageClass != "" {
		volumeInfo.storageClass = storageClass
	}
	if accessMode != "" {
		volumeInfo.accessMode = accessMode
	}

	// Generate manifests
	manifests, err := generateAdoptionManifests(volumeInfo, cfg.URL)
	if err != nil {
		return fmt.Errorf("failed to generate manifests: %w", err)
	}

	// Output
	fmt.Println("# Generated adoption manifests for", datasetPath)
	fmt.Println("# Apply with: kubectl apply -f <file>")
	fmt.Println("---")
	fmt.Println(manifests)

	return nil
}

// adoptionVolumeInfo holds all info needed to generate adoption manifests.
type adoptionVolumeInfo struct {
	volumeID      string
	dataset       string
	protocol      string
	pvcName       string
	namespace     string
	storageClass  string
	accessMode    string
	nfsSharePath  string // NFS specific
	nvmeNQN       string // NVMe-oF specific
	iscsiIQN      string // iSCSI specific
	smbShareName  string // SMB specific
	capacityBytes int64
}

func getDatasetWithProperties(ctx context.Context, client nastyapi.ClientInterface, datasetPath string) (*nastyapi.Subvolume, error) {
	datasets, err := client.FindSubvolumesByProperty(ctx, nastyapi.PropertyManagedBy, nastyapi.ManagedByValue, "")
	if err != nil {
		return nil, err
	}

	// Look for exact match
	for i := range datasets {
		if datasets[i].Pool+"/"+datasets[i].Name == datasetPath {
			return &datasets[i], nil
		}
	}

	// If no exact match, try all managed subvolumes
	// This handles cases where the dataset exists but might not have the managed_by property yet
	allDatasets, err := client.FindManagedSubvolumes(ctx, "")
	if err != nil {
		return nil, err
	}

	for i := range allDatasets {
		if allDatasets[i].Pool+"/"+allDatasets[i].Name == datasetPath {
			return &allDatasets[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errDatasetNotFound, datasetPath)
}

func extractVolumeInfo(ds *nastyapi.Subvolume) (*adoptionVolumeInfo, error) {
	props := ds.Properties
	if props == nil {
		return nil, errNoUserProperties
	}

	// Verify it's managed by nasty-csi
	if val, ok := props[nastyapi.PropertyManagedBy]; !ok || val != nastyapi.ManagedByValue {
		return nil, errNotManagedByNastyCSI
	}

	info := &adoptionVolumeInfo{
		dataset:   ds.Pool + "/" + ds.Name,
		namespace: "default", // Default
	}

	// Extract volume ID
	if val, ok := props[nastyapi.PropertyCSIVolumeName]; ok {
		info.volumeID = val
		info.pvcName = val // Default PVC name to volume ID
	}

	// Extract protocol
	if val, ok := props[nastyapi.PropertyProtocol]; ok {
		info.protocol = val
		// Set default access mode based on protocol
		switch val {
		case nastyapi.ProtocolNFS:
			info.accessMode = "ReadWriteMany"
		case nastyapi.ProtocolNVMeOF:
			info.accessMode = "ReadWriteOnce"
		case nastyapi.ProtocolISCSI:
			info.accessMode = "ReadWriteOnce"
		case nastyapi.ProtocolSMB:
			info.accessMode = "ReadWriteMany"
		}
	}

	// Extract capacity
	if val, ok := props[nastyapi.PropertyCapacityBytes]; ok {
		info.capacityBytes = nastyapi.StringToInt64(val)
	}

	// Extract stored PVC metadata (if available from previous cluster)
	if val, ok := props[nastyapi.PropertyPVCName]; ok && val != "" {
		info.pvcName = val
	}
	if val, ok := props[nastyapi.PropertyPVCNamespace]; ok && val != "" {
		info.namespace = val
	}
	if val, ok := props[nastyapi.PropertyStorageClass]; ok {
		info.storageClass = val
	}

	// Extract NFS-specific info
	if val, ok := props[nastyapi.PropertyNFSSharePath]; ok {
		info.nfsSharePath = val
	}

	// Extract NVMe-oF-specific info
	if val, ok := props[nastyapi.PropertyNVMeSubsystemNQN]; ok {
		info.nvmeNQN = val
	}

	// Extract iSCSI-specific info
	if val, ok := props[nastyapi.PropertyISCSIIQN]; ok {
		info.iscsiIQN = val
	}

	// Extract SMB-specific info
	if val, ok := props[nastyapi.PropertySMBShareName]; ok {
		info.smbShareName = val
	}

	return info, nil
}

func generateAdoptionManifests(info *adoptionVolumeInfo, nastyURL string) (string, error) {
	// Extract server from NASty URL for NFS
	server := extractServerFromURL(nastyURL)

	// Generate PV
	pv := generatePV(info, server)

	// Generate PVC
	pvc := generatePVC(info)

	// Combine
	pvYAML, err := yaml.Marshal(pv)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PV: %w", err)
	}

	pvcYAML, err := yaml.Marshal(pvc)
	if err != nil {
		return "", fmt.Errorf("failed to marshal PVC: %w", err)
	}

	return string(pvYAML) + "---\n" + string(pvcYAML), nil
}

func extractServerFromURL(url string) string {
	// Extract host from wss://host:port/path
	url = strings.TrimPrefix(url, "wss://")
	url = strings.TrimPrefix(url, "ws://")
	if idx := strings.Index(url, ":"); idx > 0 {
		return url[:idx]
	}
	if idx := strings.Index(url, "/"); idx > 0 {
		return url[:idx]
	}
	return url
}

func generatePV(info *adoptionVolumeInfo, server string) map[string]interface{} {
	pvName := "pv-" + info.volumeID

	// Build volume attributes based on protocol
	volumeAttributes := map[string]string{
		"protocol":    info.protocol,
		"datasetID":   info.dataset,
		"datasetName": info.dataset,
	}

	switch info.protocol {
	case nastyapi.ProtocolNFS:
		if info.nfsSharePath != "" {
			volumeAttributes["share"] = info.nfsSharePath
		}
		volumeAttributes["server"] = server

	case nastyapi.ProtocolNVMeOF:
		if info.nvmeNQN != "" {
			volumeAttributes["nqn"] = info.nvmeNQN
		}
		volumeAttributes["server"] = server

	case nastyapi.ProtocolISCSI:
		if info.iscsiIQN != "" {
			volumeAttributes["iqn"] = info.iscsiIQN
		}
		volumeAttributes["portal"] = server + ":3260"
		volumeAttributes["lun"] = "0"

	case nastyapi.ProtocolSMB:
		if info.smbShareName != "" {
			volumeAttributes["shareName"] = info.smbShareName
		}
		volumeAttributes["server"] = server
	}

	spec := map[string]interface{}{
		"capacity": map[string]string{
			"storage": formatBytesK8s(info.capacityBytes),
		},
		"accessModes":                   []string{info.accessMode},
		"persistentVolumeReclaimPolicy": "Retain", // Safe default for adopted volumes
		"csi": map[string]interface{}{
			"driver":           "nasty.csi.io",
			"volumeHandle":     info.dataset,
			"volumeAttributes": volumeAttributes,
		},
		"claimRef": map[string]interface{}{
			"name":      info.pvcName,
			"namespace": info.namespace,
		},
	}

	if info.storageClass != "" {
		spec["storageClassName"] = info.storageClass
	}

	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "PersistentVolume",
		"metadata": map[string]interface{}{
			"name": pvName,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "kubectl-nasty-csi",
				"nasty-csi.io/adopted":           "true",
			},
			"annotations": map[string]string{
				"nasty-csi.io/dataset": info.dataset,
			},
		},
		"spec": spec,
	}
}

func generatePVC(info *adoptionVolumeInfo) map[string]interface{} {
	pvName := "pv-" + info.volumeID

	spec := map[string]interface{}{
		"accessModes": []string{info.accessMode},
		"resources": map[string]interface{}{
			"requests": map[string]string{
				"storage": formatBytesK8s(info.capacityBytes),
			},
		},
		"volumeName": pvName,
	}

	if info.storageClass != "" {
		spec["storageClassName"] = info.storageClass
	}

	return map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]interface{}{
			"name":      info.pvcName,
			"namespace": info.namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "kubectl-nasty-csi",
				"nasty-csi.io/adopted":           "true",
			},
			"annotations": map[string]string{
				"nasty-csi.io/dataset": info.dataset,
			},
		},
		"spec": spec,
	}
}

// formatBytesK8s formats bytes in Kubernetes resource format.
func formatBytesK8s(bytes int64) string {
	const (
		Ki = 1024
		Mi = Ki * 1024
		Gi = Mi * 1024
		Ti = Gi * 1024
	)

	switch {
	case bytes >= Ti && bytes%Ti == 0:
		return fmt.Sprintf("%dTi", bytes/Ti)
	case bytes >= Gi && bytes%Gi == 0:
		return fmt.Sprintf("%dGi", bytes/Gi)
	case bytes >= Mi && bytes%Mi == 0:
		return fmt.Sprintf("%dMi", bytes/Mi)
	case bytes >= Ki && bytes%Ki == 0:
		return fmt.Sprintf("%dKi", bytes/Ki)
	default:
		// For non-aligned sizes, use Gi with decimal
		return fmt.Sprintf("%.2fGi", float64(bytes)/float64(Gi))
	}
}
