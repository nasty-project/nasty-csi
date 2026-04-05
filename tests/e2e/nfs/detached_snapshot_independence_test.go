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

// This test verifies that snapshots are independent of their source volume.
// In bcachefs, all snapshots are first-class subvolumes — deleting the source
// does not affect the snapshot.

var _ = Describe("Snapshot Independence", func() {
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

	It("should create independent snapshot that survives source deletion", func() {
		ctx := context.Background()

		storageClass := "nasty-csi-nfs"
		accessMode := corev1.ReadWriteMany
		podTimeout := 2 * time.Minute

		if f.NASty == nil {
			Skip("NASty verifier not configured — skipping backend verification")
		}

		By("Creating source PVC")
		sourcePVCName := "snap-indep-src-nfs"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             sourcePVCName,
			StorageClassName: storageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to create source PVC")

		By("Creating source POD to write data and trigger volume provisioning")
		sourcePodName := "snap-indep-src-pod-nfs"
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

		By("Getting the source volume handle")
		sourcePVName, err := f.K8s.GetPVName(ctx, pvc.Name)
		Expect(err).NotTo(HaveOccurred(), "Failed to get PV name")
		sourceDatasetPath, err := f.K8s.GetVolumeHandle(ctx, sourcePVName)
		Expect(err).NotTo(HaveOccurred(), "Failed to get volume handle")
		Expect(sourceDatasetPath).NotTo(BeEmpty(), "Volume handle is empty")

		// Verify the subvolume exists
		exists, err := f.NASty.DatasetExists(ctx, sourceDatasetPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to check subvolume existence")
		Expect(exists).To(BeTrue(), "Source subvolume should exist")

		By("Writing test data to source volume")
		testData := fmt.Sprintf("Independence Test Data - NFS - %d", time.Now().UnixNano())
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/independence-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred(), "Failed to write test data")

		By("Creating VolumeSnapshotClass")
		snapshotClassName := "snap-indep-snapclass-nfs"
		err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClassName, "nasty.csi.io", "Delete", map[string]string{})
		Expect(err).NotTo(HaveOccurred(), "Failed to create VolumeSnapshotClass")
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(ctx, snapshotClassName)
		})

		By("Creating VolumeSnapshot")
		snapshotName := "snap-indep-snap-nfs"
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

		By("Deleting source PVC")
		err = f.K8s.DeletePVC(ctx, sourcePVCName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete source PVC")

		By("Waiting for source PVC to be deleted")
		err = f.K8s.WaitForPVCDeleted(ctx, sourcePVCName, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred(), "Source PVC was not deleted in time")

		By("Verifying source subvolume was deleted")
		// With attachRequired=true, the full cleanup chain is:
		// PVC delete → VolumeAttachment delete → PV release → snapshot content finalizer → DeleteVolume
		// This can take 15-30 seconds depending on controller reconciliation timing.
		Eventually(func() bool {
			exists, _ := f.NASty.DatasetExists(ctx, sourceDatasetPath)
			return exists
		}, 60*time.Second, 5*time.Second).Should(BeFalse(),
			fmt.Sprintf("Source subvolume %s should be deleted after PVC deletion", sourceDatasetPath))
		GinkgoWriter.Printf("[NFS] SUCCESS: Source subvolume %s was deleted\n", sourceDatasetPath)

		By("Verifying snapshot still exists in K8s after source deletion")
		snapshotInfo, err := f.K8s.GetVolumeSnapshot(ctx, snapshotName)
		Expect(err).NotTo(HaveOccurred(), "Snapshot should still exist after source deletion")
		Expect(snapshotInfo).NotTo(BeNil(), "Snapshot should not be nil")
		Expect(snapshotInfo.ReadyToUse).NotTo(BeNil(), "Snapshot should have ReadyToUse status")
		Expect(*snapshotInfo.ReadyToUse).To(BeTrue(), "Snapshot should still be ready")

		GinkgoWriter.Printf("[NFS] SUCCESS: Snapshot is independent — survived source volume deletion\n")
	})
})
