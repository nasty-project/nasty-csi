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

var _ = Describe("VolSync Pattern", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		err = f.Setup("all")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	for _, proto := range protocols {

		Context(fmt.Sprintf("[%s]", proto.name), func() {
			It("should protect source during backup snapshot and restore independently", func() {
				ctx := context.Background()
				snapClass := "volsync-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source volume with production data
				srcPVC := "volsync-src-" + proto.name
				srcPod := "volsync-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "production.txt", "production-data")

				// Snapshot (simulates VolSync backup)
				snapName := "volsync-backup-" + proto.name
				By("Creating backup snapshot (VolSync-like)")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snapName)
				})
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Get PV name before attempting source deletion
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)

				// Attempt to delete source PVC — should be blocked by snapshot guard
				By("Deleting source pod and PVC (simulates disaster)")
				deletePodAndWait(ctx, f, srcPod)
				err = f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())

				// Verify PV survives for 30s (CSI snapshot guard)
				By("Verifying source PV is protected by snapshot guard (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be protected while backup snapshot exists")

				// Restore from snapshot (simulates VolSync restore)
				restPVC := "volsync-restore-" + proto.name
				restPod := "volsync-restore-pod-" + proto.name

				By("Restoring from backup snapshot")
				err = f.K8s.CreatePVCFromSnapshot(ctx, restPVC, snapName, proto.storageClass, pvcSize,
					[]corev1.PersistentVolumeAccessMode{proto.accessMode})
				Expect(err).NotTo(HaveOccurred())
				f.RegisterPVCCleanup(restPVC)

				err = f.K8s.WaitForPVCBound(ctx, restPVC, proto.pvcTimeout)
				Expect(err).NotTo(HaveOccurred())

				restPodObj, err := f.CreatePod(ctx, framework.PodOptions{
					Name:      restPod,
					PVCName:   restPVC,
					MountPath: mountPath,
				})
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPodReady(ctx, restPodObj.Name, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred())

				// Verify restored data
				By("Verifying restored volume has production-data")
				Expect(readData(ctx, f.K8s, restPod, "production.txt")).To(Equal("production-data"))

				// Delete snapshot → source PV should now delete
				By("Deleting backup snapshot to release source PV")
				err = f.K8s.DeleteVolumeSnapshot(ctx, snapName)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is now deleted")
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Verify restored volume still works independently
				By("Verifying restored volume still works after source PV deletion")
				Expect(readData(ctx, f.K8s, restPod, "production.txt")).To(Equal("production-data"))
				writeData(ctx, f.K8s, restPod, "new-data.txt", "post-restore-write")
				Expect(readData(ctx, f.K8s, restPod, "new-data.txt")).To(Equal("post-restore-write"))
			})
		})
	}
})
