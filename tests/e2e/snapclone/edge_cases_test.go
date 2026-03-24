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

var _ = Describe("Edge Cases", func() {
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
			It("should block deletion when source has both snapshot AND clone", func() {
				ctx := context.Background()
				snapClass := "edge-both-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source volume
				srcPVC := "edge-both-src-" + proto.name
				srcPod := "edge-both-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "edge-both-test")

				// Create CSI snapshot from source
				snapName := "edge-both-snap-" + proto.name
				By("Creating CSI snapshot from source")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				// Don't register cleanup — we'll delete manually
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Create COW clone from source
				clonePVC := "edge-both-clone-" + proto.name
				clonePod := "edge-both-clone-pod-" + proto.name
				createCloneAndMount(ctx, f, proto, clonePVC, clonePod, srcPVC)

				// Get PV name before deletion
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)

				// Delete source pod and PVC — should be blocked (snapshot + clone)
				By("Deleting source pod and PVC")
				deletePodAndWait(ctx, f, srcPod)
				err = f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is protected (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be protected while snapshot and clone exist")

				// Delete clone — source should still be blocked (snapshot remains)
				By("Deleting clone — snapshot still blocks deletion")
				deletePodAndWait(ctx, f, clonePod)
				err = f.K8s.DeletePVC(ctx, clonePVC)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is still protected after clone deletion (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should still be protected while snapshot exists")

				// Delete snapshot — source PV should now delete
				By("Deleting snapshot to fully unblock source deletion")
				err = f.K8s.DeleteVolumeSnapshot(ctx, snapName)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is now deleted")
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Source PV should delete after both clone and snapshot are removed")
			})

			It("should release guard as snapshots are deleted one by one", func() {
				ctx := context.Background()
				snapClass := "edge-multi-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source volume
				srcPVC := "edge-multi-src-" + proto.name
				srcPod := "edge-multi-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "multi-snap-test")

				// Create 3 snapshots sequentially
				snapNames := make([]string, 3)
				for i := range 3 {
					snapNames[i] = fmt.Sprintf("edge-multi-snap%d-%s", i, proto.name)
					By(fmt.Sprintf("Creating snapshot %d: %s", i, snapNames[i]))
					err := f.K8s.CreateVolumeSnapshot(ctx, snapNames[i], srcPVC, snapClass)
					Expect(err).NotTo(HaveOccurred())
					// Don't register cleanup — we'll delete manually
					err = f.K8s.WaitForSnapshotReady(ctx, snapNames[i], 3*time.Minute)
					Expect(err).NotTo(HaveOccurred())
				}

				// Get PV name before deletion
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)

				// Delete source pod and PVC — should be blocked (3 snapshots)
				By("Deleting source pod and PVC")
				deletePodAndWait(ctx, f, srcPod)
				err := f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is protected (30s)")
				err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be protected with 3 snapshots")

				// Delete snapshots one by one
				for i := range 2 {
					By(fmt.Sprintf("Deleting snapshot %d: %s", i, snapNames[i]))
					err = f.K8s.DeleteVolumeSnapshot(ctx, snapNames[i])
					Expect(err).NotTo(HaveOccurred())

					By(fmt.Sprintf("Verifying source PV still protected after deleting snapshot %d (30s)", i))
					err = f.K8s.WaitForPVNotDeletedWithin(ctx, srcPVName, 30*time.Second)
					Expect(err).NotTo(HaveOccurred(),
						fmt.Sprintf("Source PV should be protected with %d snapshot(s) remaining", 2-i))
				}

				// Delete last snapshot — source PV should now delete
				By("Deleting last snapshot to fully unblock source deletion")
				err = f.K8s.DeleteVolumeSnapshot(ctx, snapNames[2])
				Expect(err).NotTo(HaveOccurred())

				By("Verifying source PV is now deleted")
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Source PV should delete after all snapshots are removed")
			})

			It("should survive rapid snapshot create-delete cycles without leaks", func() {
				ctx := context.Background()
				snapClass := "edge-rapid-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source volume
				srcPVC := "edge-rapid-src-" + proto.name
				srcPod := "edge-rapid-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "rapid-test")

				// Take NASty resource snapshot before cycles
				var beforeSnap *framework.ResourceSnapshot
				if f.NASty != nil {
					By("Taking NASty resource snapshot before rapid cycles")
					beforeSnap = f.NASty.SnapshotResources(ctx, f.Config.NAStyFilesystem)
				}

				// Run 5 rapid snapshot create-delete cycles
				for i := range 5 {
					snapName := fmt.Sprintf("edge-rapid-snap%d-%s", i, proto.name)
					restPVC := fmt.Sprintf("edge-rapid-rest%d-%s", i, proto.name)
					restPod := fmt.Sprintf("edge-rapid-rest%d-pod-%s", i, proto.name)

					By(fmt.Sprintf("Cycle %d: Creating snapshot %s", i, snapName))
					err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
					Expect(err).NotTo(HaveOccurred())
					err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
					Expect(err).NotTo(HaveOccurred())

					By(fmt.Sprintf("Cycle %d: Restoring from snapshot", i))
					err = f.K8s.CreatePVCFromSnapshot(ctx, restPVC, snapName, proto.storageClass, pvcSize,
						[]corev1.PersistentVolumeAccessMode{proto.accessMode})
					Expect(err).NotTo(HaveOccurred())
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
					Expect(readData(ctx, f.K8s, restPod, "data.txt")).To(Equal("rapid-test"))

					// Get PV name for the restored volume
					restPVName := getPVNameForPVC(ctx, f, restPVC)

					// Tear down: pod → PVC → snapshot
					By(fmt.Sprintf("Cycle %d: Tearing down restored volume", i))
					deletePodAndWait(ctx, f, restPod)
					err = f.K8s.DeletePVC(ctx, restPVC)
					Expect(err).NotTo(HaveOccurred())
					err = f.K8s.WaitForPVDeleted(ctx, restPVName, 4*time.Minute)
					Expect(err).NotTo(HaveOccurred())

					By(fmt.Sprintf("Cycle %d: Deleting snapshot", i))
					err = f.K8s.DeleteVolumeSnapshot(ctx, snapName)
					Expect(err).NotTo(HaveOccurred())
				}

				// Verify source still works after all cycles
				By("Verifying source volume still works after 5 cycles")
				Expect(readData(ctx, f.K8s, srcPod, "data.txt")).To(Equal("rapid-test"))
				writeData(ctx, f.K8s, srcPod, "post-cycles.txt", "still-alive")
				Expect(readData(ctx, f.K8s, srcPod, "post-cycles.txt")).To(Equal("still-alive"))

				// Verify zero resource leaks
				if f.NASty != nil && beforeSnap != nil {
					By("Verifying zero resource leaks after rapid cycles")
					time.Sleep(5 * time.Second)
					afterSnap := f.NASty.SnapshotResources(ctx, f.Config.NAStyFilesystem)

					for dsName := range afterSnap.Datasets {
						if _, existed := beforeSnap.Datasets[dsName]; !existed {
							Fail("Leaked dataset after rapid cycles: " + dsName)
						}
					}
					for share := range afterSnap.NFSShares {
						if _, existed := beforeSnap.NFSShares[share]; !existed {
							Fail("Leaked NFS share after rapid cycles: " + share)
						}
					}
					for share := range afterSnap.SMBShares {
						if _, existed := beforeSnap.SMBShares[share]; !existed {
							Fail("Leaked SMB share after rapid cycles: " + share)
						}
					}
					for nqn := range afterSnap.NVMeSubsNQNs {
						if _, existed := beforeSnap.NVMeSubsNQNs[nqn]; !existed {
							Fail("Leaked NVMe-oF subsystem after rapid cycles: " + nqn)
						}
					}
					for target := range afterSnap.ISCSITargets {
						if _, existed := beforeSnap.ISCSITargets[target]; !existed {
							Fail("Leaked iSCSI target after rapid cycles: " + target)
						}
					}
					for extent := range afterSnap.ISCSIExtents {
						if _, existed := beforeSnap.ISCSIExtents[extent]; !existed {
							Fail("Leaked iSCSI extent after rapid cycles: " + extent)
						}
					}
				}
			})
		})
	}
})
