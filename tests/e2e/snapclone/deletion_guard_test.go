package snapclone

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("Deletion Guards", func() {
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
			It("should block parent deletion while COW clone exists", func() {
				ctx := context.Background()

				// Source → COW clone
				srcPVC := "guard-clone-src-" + proto.name
				srcPod := "guard-clone-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "guard-test")

				clonePVC := "guard-clone-" + proto.name
				clonePod := "guard-clone-pod-" + proto.name
				createCloneAndMount(ctx, f, proto, clonePVC, clonePod, srcPVC)

				// Get PV name before deletion
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)

				// Delete source pod then source PVC
				By("Deleting source pod and PVC")
				deletePodAndWait(ctx, f, srcPod)
				err := f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())

				// Verify PV survives for 30s (guard blocks deletion)
				By("Verifying source PV is protected by deletion guard (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be protected while clone exists")

				// Check controller logs for guard message
				By("Checking controller logs for dependent clones message")
				logs, logErr := f.K8s.GetControllerLogs(ctx, 200)
				if logErr == nil {
					Expect(logs).To(ContainSubstring("dependent clone"),
						"Controller should log about dependent clones blocking deletion")
				}

				// Delete clone → source PV should now delete
				By("Deleting clone to unblock source deletion")
				deletePodAndWait(ctx, f, clonePod)
				err = f.K8s.DeletePVC(ctx, clonePVC)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is now deleted")
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Source PV should delete after clone is removed")
			})

			It("should block parent deletion while CSI snapshot exists", func() {
				ctx := context.Background()
				snapClass := "guard-snap-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source → snapshot
				srcPVC := "guard-snap-src-" + proto.name
				srcPod := "guard-snap-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "snapshot-guard-test")

				snapName := "guard-snap-" + proto.name
				By("Creating snapshot")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snapName)
				})
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Get PV name before deletion
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)

				// Delete source pod then source PVC
				By("Deleting source pod and PVC")
				deletePodAndWait(ctx, f, srcPod)
				err = f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())

				// Verify PV survives for 30s
				By("Verifying source PV is protected by snapshot guard (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be protected while snapshot exists")

				// Check controller logs
				By("Checking controller logs for CSI-managed snapshots message")
				logs, logErr := f.K8s.GetControllerLogs(ctx, 200)
				if logErr == nil {
					Expect(logs).To(ContainSubstring("CSI-managed snapshot"),
						"Controller should log about CSI snapshots blocking deletion")
				}

				// Delete snapshot → source PV should now delete
				By("Deleting snapshot to unblock source deletion")
				err = f.K8s.DeleteVolumeSnapshot(ctx, snapName)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is now deleted")
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Source PV should delete after snapshot is removed")
			})

			It("should allow snapshot deletion while restored clone from it still works", func() {
				ctx := context.Background()
				snapClass := "guard-snapdel-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source → snapshot → restore
				srcPVC := "guard-snapdel-src-" + proto.name
				srcPod := "guard-snapdel-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "snapshot-delete-test")

				snapName := "guard-snapdel-snap-" + proto.name
				restPVC := "guard-snapdel-rest-" + proto.name
				restPod := "guard-snapdel-rest-pod-" + proto.name
				snapshotAndRestore(ctx, f, proto, snapName, snapClass, srcPVC, restPVC, restPod)

				By("Verifying restored volume has data before snapshot deletion")
				Expect(readData(ctx, f.K8s, restPod, "data.txt")).To(Equal("snapshot-delete-test"))

				// Delete the VolumeSnapshot
				By("Deleting VolumeSnapshot while restored volume exists")
				err := f.K8s.DeleteVolumeSnapshot(ctx, snapName)
				Expect(err).NotTo(HaveOccurred())

				// Verify restored PVC still works
				By("Verifying restored volume still has data after snapshot deletion")
				Expect(readData(ctx, f.K8s, restPod, "data.txt")).To(Equal("snapshot-delete-test"))

				// Write new data to verify volume is fully functional
				writeData(ctx, f.K8s, restPod, "post-snapdel.txt", "still-working")
				Expect(readData(ctx, f.K8s, restPod, "post-snapdel.txt")).To(Equal("still-working"))

				// Verify source still works
				By("Verifying source volume still works")
				Expect(readData(ctx, f.K8s, srcPod, "data.txt")).To(Equal("snapshot-delete-test"))
			})
		})
	}
})
