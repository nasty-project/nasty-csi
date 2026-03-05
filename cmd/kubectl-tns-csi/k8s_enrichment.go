package main

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// enrichWithK8sData fetches K8s PV/PVC data and optionally pod data.
// Returns best-effort results — if K8s is unavailable, Available will be false.
func enrichWithK8sData(ctx context.Context, includePods bool) *K8sEnrichmentResult {
	result := &K8sEnrichmentResult{
		Bindings: make(map[string]*K8sVolumeBinding),
	}

	// Apply a 5-second timeout to avoid blocking if the cluster is slow/unreachable
	enrichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client, err := getK8sClient()
	if err != nil {
		klog.V(4).Infof("K8s enrichment unavailable: %v", err)
		return result
	}

	pvMap, _, err := getK8sVolumeInfo(enrichCtx, client, true)
	if err != nil {
		klog.V(4).Infof("K8s enrichment failed to fetch PV/PVC info: %v", err)
		return result
	}

	result.Available = true

	// Build bindings from PV map
	for volumeID, pv := range pvMap {
		binding := &K8sVolumeBinding{
			PVName:       pv.Name,
			PVCName:      pv.PVCName,
			PVCNamespace: pv.PVCNs,
			PVStatus:     pv.Status,
		}
		result.Bindings[volumeID] = binding
	}

	// Optionally scan pods for PVC usage
	if includePods {
		pods, err := client.CoreV1().Pods("").List(enrichCtx, metav1.ListOptions{})
		if err != nil {
			klog.V(4).Infof("K8s enrichment failed to list pods: %v", err)
			return result
		}

		// Build a reverse map: "namespace/pvcName" -> list of "namespace/podName"
		pvcToPods := make(map[string][]string)
		for i := range pods.Items {
			pod := &pods.Items[i]
			for j := range pod.Spec.Volumes {
				pvc := pod.Spec.Volumes[j].PersistentVolumeClaim
				if pvc != nil {
					key := pod.Namespace + "/" + pvc.ClaimName
					podRef := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
					pvcToPods[key] = append(pvcToPods[key], podRef)
				}
			}
		}

		// Attach pod lists to bindings
		for _, binding := range result.Bindings {
			if binding.PVCName != "" && binding.PVCNamespace != "" {
				key := binding.PVCNamespace + "/" + binding.PVCName
				if podRefs, ok := pvcToPods[key]; ok {
					binding.Pods = podRefs
				}
			}
		}
	}

	return result
}
