package snapclone

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

const (
	mountPath = "/data"
	pvcSize   = "1Gi"
)

type protocolConfig struct {
	name         string
	storageClass string
	accessMode   corev1.PersistentVolumeAccessMode
	isBlock      bool // RWO protocol — needs pod sequencing for same-PVC access
	pvcTimeout   time.Duration
	podTimeout   time.Duration
}

var protocols = []protocolConfig{
	{
		name:         "nfs",
		storageClass: "nasty-csi-nfs",
		accessMode:   corev1.ReadWriteMany,
		isBlock:      false,
		pvcTimeout:   2 * time.Minute,
		podTimeout:   2 * time.Minute,
	},
	{
		name:         "nvmeof",
		storageClass: "nasty-csi-nvmeof",
		accessMode:   corev1.ReadWriteOnce,
		isBlock:      true,
		pvcTimeout:   3 * time.Minute,
		podTimeout:   3 * time.Minute,
	},
	{
		name:         "iscsi",
		storageClass: "nasty-csi-iscsi",
		accessMode:   corev1.ReadWriteOnce,
		isBlock:      true,
		pvcTimeout:   3 * time.Minute,
		podTimeout:   3 * time.Minute,
	},
	{
		name:         "smb",
		storageClass: "nasty-csi-smb",
		accessMode:   corev1.ReadWriteMany,
		isBlock:      false,
		pvcTimeout:   2 * time.Minute,
		podTimeout:   2 * time.Minute,
	},
}

// createSnapshotClass creates a VolumeSnapshotClass and registers cleanup.
func createSnapshotClass(ctx context.Context, f *framework.Framework, name string) {
	err := f.K8s.CreateVolumeSnapshotClass(ctx, name, "nasty.csi.io", "Delete")
	Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass %s", name)
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		return f.K8s.DeleteVolumeSnapshotClass(context.Background(), name)
	})
}

// writeData writes a string to a file inside a pod.
func writeData(ctx context.Context, k8s *framework.KubernetesClient, podName, filename, data string) {
	_, err := k8s.ExecInPod(ctx, podName, []string{
		"sh", "-c", fmt.Sprintf("echo '%s' > %s/%s && sync", data, mountPath, filename),
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to write %s to pod %s", filename, podName)
}

// readData reads a file from a pod and returns its content.
func readData(ctx context.Context, k8s *framework.KubernetesClient, podName, filename string) string {
	output, err := k8s.ExecInPod(ctx, podName, []string{"cat", fmt.Sprintf("%s/%s", mountPath, filename)})
	Expect(err).NotTo(HaveOccurred(), "Failed to read %s from pod %s", filename, podName)
	return output
}

// verifyDataAbsent asserts that a file does NOT exist in a pod.
func verifyDataAbsent(ctx context.Context, k8s *framework.KubernetesClient, podName, filename string) {
	exists, err := k8s.FileExistsInPod(ctx, podName, fmt.Sprintf("%s/%s", mountPath, filename))
	Expect(err).NotTo(HaveOccurred(), "Failed to check file existence in pod %s", podName)
	Expect(exists).To(BeFalse(), "File %s should not exist in pod %s", filename, podName)
}

// createAndMountPVC creates a PVC, waits for it to bind, creates a pod, and waits for the pod to be ready.
// Returns the PVC name and pod name.
func createAndMountPVC(ctx context.Context, f *framework.Framework, proto protocolConfig, pvcName, podName string) {
	By(fmt.Sprintf("[%s] Creating PVC %s", proto.name, pvcName))
	pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
		Name:             pvcName,
		StorageClassName: proto.storageClass,
		Size:             pvcSize,
		AccessModes:      []corev1.PersistentVolumeAccessMode{proto.accessMode},
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create PVC %s", pvcName)

	err = f.K8s.WaitForPVCBound(ctx, pvc.Name, proto.pvcTimeout)
	Expect(err).NotTo(HaveOccurred(), "PVC %s did not become Bound", pvcName)

	By(fmt.Sprintf("[%s] Creating pod %s", proto.name, podName))
	pod, err := f.CreatePod(ctx, framework.PodOptions{
		Name:      podName,
		PVCName:   pvc.Name,
		MountPath: mountPath,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create pod %s", podName)

	err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
	Expect(err).NotTo(HaveOccurred(), "Pod %s did not become ready", podName)
}

// createCloneAndMount creates a COW clone PVC from a source, waits for bind, mounts it, and waits for pod ready.
func createCloneAndMount(ctx context.Context, f *framework.Framework, proto protocolConfig, clonePVC, clonePod, sourcePVC string) {
	By(fmt.Sprintf("[%s] Creating COW clone %s from %s", proto.name, clonePVC, sourcePVC))
	err := f.K8s.CreatePVCFromPVC(ctx, clonePVC, sourcePVC, proto.storageClass, pvcSize,
		[]corev1.PersistentVolumeAccessMode{proto.accessMode})
	Expect(err).NotTo(HaveOccurred(), "Failed to create clone PVC %s", clonePVC)
	f.RegisterPVCCleanup(clonePVC) //nolint:contextcheck // RegisterPVCCleanup uses fresh context internally

	err = f.K8s.WaitForPVCBound(ctx, clonePVC, proto.pvcTimeout)
	Expect(err).NotTo(HaveOccurred(), "Clone PVC %s did not become Bound", clonePVC)

	By(fmt.Sprintf("[%s] Mounting clone %s on pod %s", proto.name, clonePVC, clonePod))
	pod, err := f.CreatePod(ctx, framework.PodOptions{
		Name:      clonePod,
		PVCName:   clonePVC,
		MountPath: mountPath,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create clone pod %s", clonePod)

	err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
	Expect(err).NotTo(HaveOccurred(), "Clone pod %s did not become ready", clonePod)
}

// snapshotAndRestore creates a snapshot from sourcePVC, restores to a new PVC, and mounts it.
func snapshotAndRestore(ctx context.Context, f *framework.Framework, proto protocolConfig,
	snapName, snapClass, sourcePVC, restoredPVC, restoredPod string,
) { //nolint:whitespace // Multi-line signature requires blank line which conflicts with whitespace linter
	By(fmt.Sprintf("[%s] Creating snapshot %s from %s", proto.name, snapName, sourcePVC))
	err := f.K8s.CreateVolumeSnapshot(ctx, snapName, sourcePVC, snapClass)
	Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot %s", snapName)
	f.Cleanup.Add(func() error { //nolint:contextcheck // Cleanup uses fresh context
		return f.K8s.DeleteVolumeSnapshot(context.Background(), snapName)
	})

	err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
	Expect(err).NotTo(HaveOccurred(), "Snapshot %s did not become ready", snapName)

	By(fmt.Sprintf("[%s] Restoring %s from snapshot %s", proto.name, restoredPVC, snapName))
	err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVC, snapName, proto.storageClass, pvcSize,
		[]corev1.PersistentVolumeAccessMode{proto.accessMode})
	Expect(err).NotTo(HaveOccurred(), "Failed to create restored PVC %s", restoredPVC)
	f.RegisterPVCCleanup(restoredPVC) //nolint:contextcheck // RegisterPVCCleanup uses fresh context internally

	err = f.K8s.WaitForPVCBound(ctx, restoredPVC, proto.pvcTimeout)
	Expect(err).NotTo(HaveOccurred(), "Restored PVC %s did not become Bound", restoredPVC)

	By(fmt.Sprintf("[%s] Mounting restored %s on pod %s", proto.name, restoredPVC, restoredPod))
	pod, err := f.CreatePod(ctx, framework.PodOptions{
		Name:      restoredPod,
		PVCName:   restoredPVC,
		MountPath: mountPath,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create restored pod %s", restoredPod)

	err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
	Expect(err).NotTo(HaveOccurred(), "Restored pod %s did not become ready", restoredPod)
}

// getPVNameForPVC returns the PV name bound to a PVC.
func getPVNameForPVC(ctx context.Context, f *framework.Framework, pvcName string) string {
	pv, err := f.K8s.GetPVForPVC(ctx, pvcName)
	Expect(err).NotTo(HaveOccurred(), "Failed to get PV for PVC %s", pvcName)
	return pv.Name
}

// deletePodAndWait deletes a pod and waits for it to be fully removed.
func deletePodAndWait(ctx context.Context, f *framework.Framework, podName string) {
	err := f.K8s.DeletePod(ctx, podName)
	Expect(err).NotTo(HaveOccurred(), "Failed to delete pod %s", podName)
	err = f.K8s.WaitForPodDeleted(ctx, podName, 2*time.Minute)
	Expect(err).NotTo(HaveOccurred(), "Pod %s was not deleted in time", podName)
}
