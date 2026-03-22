// Package nfs contains NFS-specific E2E tests for the NASty CSI driver.
package nfs

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

// These tests cover snapshot scenarios:
// 1. Creating a snapshot and restoring from it
// 2. Snapshot surviving source volume deletion (DR scenario)
// bcachefs snapshots are always independent — no detach/promote needed.

var _ = Describe("Snapshot Advanced", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred(), "Failed to create framework")

		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred(), "Failed to setup framework")
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should create snapshot and restore from it", func() {
		ctx := context.Background()

		storageClass := "nasty-csi-nfs"
		accessMode := corev1.ReadWriteMany
		podTimeout := 2 * time.Minute

		By("Creating source PVC")
		sourcePVCName := "snap-adv-source-nfs"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sourcePVCName,
			StorageClassName: storageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Creating source POD to write data")
		sourcePodName := "snap-adv-source-pod-nfs"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      sourcePodName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source POD")

		By("Waiting for source POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Source POD did not become ready")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Writing test data to source volume")
		testData := fmt.Sprintf("Snapshot Data - NFS - %d", time.Now().UnixNano())
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/snap-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Creating VolumeSnapshotClass")
		snapshotClassName := "snap-adv-snapclass-nfs"
		err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "nasty.csi.io", "Delete", map[string]string{})
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
		})

		By("Creating VolumeSnapshot")
		snapshotName := "snap-adv-snap-nfs"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClassName)
		Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
		})

		By("Waiting for snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Snapshot did not become ready")

		By("Restoring PVC from snapshot")
		restoredPVCName := "snap-adv-restored-nfs"
		err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, storageClass, "1Gi",
			[]corev1.PersistentVolumeAccessMode{accessMode})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot")
		f.RegisterPVCCleanup(restoredPVCName)

		By("Creating POD to mount restored volume")
		restoredPodName := "snap-adv-restored-pod-nfs"
		restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      restoredPodName,
			PVCName:   restoredPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create restored POD")

		By("Waiting for restored POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Restored POD did not become ready")

		By("Waiting for restored PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored PVC did not become Bound")

		By("Verifying data was restored from snapshot")
		output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/snap-test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
		Expect(output).To(Equal(testData), "Restored data should match original")
	})

	It("should preserve snapshot after source volume deletion (DR scenario)", func() {
		ctx := context.Background()

		storageClass := "nasty-csi-nfs"
		accessMode := corev1.ReadWriteMany
		podTimeout := 2 * time.Minute

		By("Creating source PVC")
		sourcePVCName := "snap-dr-source-nfs"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sourcePVCName,
			StorageClassName: storageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Creating source POD to write data")
		sourcePodName := "snap-dr-source-pod-nfs"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      sourcePodName,
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source POD")

		By("Waiting for source POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Source POD did not become ready")

		By("Waiting for source PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC did not become Bound")

		By("Writing test data to source volume")
		testData := fmt.Sprintf("DR Test Data - NFS - %d", time.Now().UnixNano())
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/dr-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Creating VolumeSnapshotClass")
		snapshotClassName := "snap-dr-snapclass-nfs"
		err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "nasty.csi.io", "Delete", map[string]string{})
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
		})

		By("Creating VolumeSnapshot")
		snapshotName := "snap-dr-snap-nfs"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClassName)
		Expect(err).NotTo(HaveOccurred(), "Failed to create snapshot")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(ctx, snapshotName)
		})

		By("Waiting for snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Snapshot did not become ready")

		By("Deleting source POD")
		err = f.K8s.DeletePod(ctx, sourcePodName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete source POD")
		err = f.K8s.WaitForPodDeleted(ctx, sourcePodName, 60*time.Second)
		Expect(err).NotTo(HaveOccurred(), "Source POD was not deleted")

		By("Deleting source PVC — snapshot should survive (bcachefs snapshots are independent)")
		err = f.K8s.DeletePVC(ctx, sourcePVCName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete source PVC")
		err = f.K8s.WaitForPVCDeleted(ctx, sourcePVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC was not deleted")

		By("Waiting a moment for cleanup")
		time.Sleep(5 * time.Second)

		By("Verifying snapshot still exists and is ready")
		snapshotInfo, err := f.K8s.GetVolumeSnapshot(ctx, snapshotName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get snapshot after source deletion")
		Expect(snapshotInfo).NotTo(BeNil(), "Snapshot should still exist")
		Expect(snapshotInfo.ReadyToUse).NotTo(BeNil(), "Snapshot should have ReadyToUse status")
		Expect(*snapshotInfo.ReadyToUse).To(BeTrue(), "Snapshot should still be ready")

		By("Restoring PVC from snapshot after source was deleted")
		restoredPVCName := "snap-dr-restored-nfs"
		err = f.K8s.CreatePVCFromSnapshot(ctx, restoredPVCName, snapshotName, storageClass, "1Gi",
			[]corev1.PersistentVolumeAccessMode{accessMode})
		Expect(err).NotTo(HaveOccurred(), "Failed to create PVC from snapshot")
		f.RegisterPVCCleanup(restoredPVCName)

		By("Creating POD to mount restored volume")
		restoredPodName := "snap-dr-restored-pod-nfs"
		restoredPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      restoredPodName,
			PVCName:   restoredPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create restored POD")

		By("Waiting for restored POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, restoredPod.Name, podTimeout)
		Expect(err).NotTo(HaveOccurred(), "Restored POD did not become ready")

		By("Waiting for restored PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, restoredPVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Restored PVC did not become Bound")

		By("Verifying data was restored from snapshot after source deletion")
		output, err := f.K8s.ExecInPod(ctx, restoredPod.Name, []string{"cat", "/data/dr-test.txt"})
		Expect(err).NotTo(HaveOccurred(), "Failed to read data from restored volume")
		Expect(output).To(Equal(testData), "Restored data should match original even after source deletion")
	})
})
