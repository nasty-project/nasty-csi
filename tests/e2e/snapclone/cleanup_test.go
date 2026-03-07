package snapclone

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/fenio/tns-csi/tests/e2e/framework"
)

var _ = Describe("Complex Cleanup", func() {
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
			It("should cleanly delete a complex resource graph in dependency order", func() {
				ctx := context.Background()
				snapClass := "cleanup-graph-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Build resource graph: source → snapshot → restore → COW clone
				srcPVC := "cleanup-src-" + proto.name
				srcPod := "cleanup-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "cleanup-test")

				// Snapshot
				snapName := "cleanup-snap-" + proto.name
				By("Creating snapshot")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				// Don't register cleanup — we'll delete manually
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Restore from snapshot
				restPVC := "cleanup-rest-" + proto.name
				restPod := "cleanup-rest-pod-" + proto.name

				err = f.K8s.CreatePVCFromSnapshot(ctx, restPVC, snapName, proto.storageClass, pvcSize,
					[]corev1.PersistentVolumeAccessMode{proto.accessMode})
				Expect(err).NotTo(HaveOccurred())
				// Don't register cleanup — we'll delete manually

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

				// COW clone from restored
				clonePVC := "cleanup-clone-" + proto.name
				clonePod := "cleanup-clone-pod-" + proto.name

				err = f.K8s.CreatePVCFromPVC(ctx, clonePVC, restPVC, proto.storageClass, pvcSize,
					[]corev1.PersistentVolumeAccessMode{proto.accessMode})
				Expect(err).NotTo(HaveOccurred())
				// Don't register cleanup — we'll delete manually

				err = f.K8s.WaitForPVCBound(ctx, clonePVC, proto.pvcTimeout)
				Expect(err).NotTo(HaveOccurred())

				clonePodObj, err := f.CreatePod(ctx, framework.PodOptions{
					Name:      clonePod,
					PVCName:   clonePVC,
					MountPath: mountPath,
				})
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPodReady(ctx, clonePodObj.Name, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred())

				// Verify the full graph works
				By("Verifying all volumes in the graph have data")
				Expect(readData(ctx, f.K8s, srcPod, "data.txt")).To(Equal("cleanup-test"))
				Expect(readData(ctx, f.K8s, restPod, "data.txt")).To(Equal("cleanup-test"))
				Expect(readData(ctx, f.K8s, clonePod, "data.txt")).To(Equal("cleanup-test"))

				// Take TrueNAS resource snapshot (before cleanup)
				var beforeSnap *framework.ResourceSnapshot
				if f.TrueNAS != nil {
					By("Taking TrueNAS resource snapshot before cleanup")
					beforeSnap = f.TrueNAS.SnapshotResources(ctx, f.Config.TrueNASPool)
				}

				// Record PV names before manual cleanup
				srcPVName := getPVNameForPVC(ctx, f, srcPVC)
				restPVName := getPVNameForPVC(ctx, f, restPVC)
				clonePVName := getPVNameForPVC(ctx, f, clonePVC)

				// Manual cleanup in dependency order:
				// 1. Delete all pods first
				By("Step 1: Deleting all pods")
				deletePodAndWait(ctx, f, clonePod)
				deletePodAndWait(ctx, f, restPod)
				deletePodAndWait(ctx, f, srcPod)

				// 2. Delete clone PVC (leaf of dependency graph)
				By("Step 2: Deleting clone PVC (leaf)")
				err = f.K8s.DeletePVC(ctx, clonePVC)
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPVDeleted(ctx, clonePVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Clone PV should be deleted")

				// 3. Delete snapshot
				By("Step 3: Deleting VolumeSnapshot")
				err = f.K8s.DeleteVolumeSnapshot(ctx, snapName)
				Expect(err).NotTo(HaveOccurred())

				// 4. Delete restored PVC
				By("Step 4: Deleting restored PVC")
				err = f.K8s.DeletePVC(ctx, restPVC)
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPVDeleted(ctx, restPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Restored PV should be deleted")

				// 5. Delete source PVC (root of dependency graph)
				By("Step 5: Deleting source PVC (root)")
				err = f.K8s.DeletePVC(ctx, srcPVC)
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPVDeleted(ctx, srcPVName, 4*time.Minute)
				Expect(err).NotTo(HaveOccurred(), "Source PV should be deleted")

				// Take TrueNAS resource snapshot (after cleanup) and verify zero leaks
				if f.TrueNAS != nil && beforeSnap != nil {
					By("Taking TrueNAS resource snapshot after cleanup and verifying zero leaks")
					// Wait briefly for TrueNAS to process all deletions
					time.Sleep(5 * time.Second)
					afterSnap := f.TrueNAS.SnapshotResources(ctx, f.Config.TrueNASPool)

					// Verify no new resources were leaked (after should have <= before)
					for dsName := range afterSnap.Datasets {
						if _, existed := beforeSnap.Datasets[dsName]; !existed {
							Fail("Leaked dataset after cleanup: " + dsName)
						}
					}
					for share := range afterSnap.NFSShares {
						if _, existed := beforeSnap.NFSShares[share]; !existed {
							Fail("Leaked NFS share after cleanup: " + share)
						}
					}
					for nqn := range afterSnap.NVMeSubsNQNs {
						if _, existed := beforeSnap.NVMeSubsNQNs[nqn]; !existed {
							Fail("Leaked NVMe-oF subsystem after cleanup: " + nqn)
						}
					}
					for target := range afterSnap.ISCSITargets {
						if _, existed := beforeSnap.ISCSITargets[target]; !existed {
							Fail("Leaked iSCSI target after cleanup: " + target)
						}
					}
					for extent := range afterSnap.ISCSIExtents {
						if _, existed := beforeSnap.ISCSIExtents[extent]; !existed {
							Fail("Leaked iSCSI extent after cleanup: " + extent)
						}
					}
					for share := range afterSnap.SMBShares {
						if _, existed := beforeSnap.SMBShares[share]; !existed {
							Fail("Leaked SMB share after cleanup: " + share)
						}
					}
				}
			})
		})
	}
})
