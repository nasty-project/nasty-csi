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

var _ = Describe("Chain Operations", func() {
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
			It("should snapshot a COW clone independently from source", func() {
				ctx := context.Background()
				snapClass := "chain-snapclone-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source → write "original"
				srcPVC := "chain-sc-src-" + proto.name
				srcPod := "chain-sc-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "base.txt", "original")

				// COW clone from source
				clonePVC := "chain-sc-clone-" + proto.name
				clonePod := "chain-sc-clone-pod-" + proto.name
				createCloneAndMount(ctx, f, proto, clonePVC, clonePod, srcPVC)

				// Write to clone
				By("Writing clone-modification to clone")
				writeData(ctx, f.K8s, clonePod, "clone-mod.txt", "clone-modification")

				// Snapshot the clone (not source)
				snapName := "chain-sc-snap-" + proto.name
				By("Creating snapshot of the clone")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, clonePVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snapName)
				})
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Restore from clone's snapshot
				restPVC := "chain-sc-rest-" + proto.name
				restPod := "chain-sc-rest-pod-" + proto.name

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

				// Verify restored has both original + clone-modification
				By("Verifying restored volume has original data from clone lineage")
				Expect(readData(ctx, f.K8s, restPod, "base.txt")).To(Equal("original"))
				Expect(readData(ctx, f.K8s, restPod, "clone-mod.txt")).To(Equal("clone-modification"))

				// Verify source only has "original"
				By("Verifying source only has original data")
				Expect(readData(ctx, f.K8s, srcPod, "base.txt")).To(Equal("original"))
				verifyDataAbsent(ctx, f.K8s, srcPod, "clone-mod.txt")
			})

			It("should snapshot a restored volume (snap-of-restore)", func() {
				ctx := context.Background()
				snapClass := "chain-sor-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source → write "phase1"
				srcPVC := "chain-sor-src-" + proto.name
				srcPod := "chain-sor-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "data.txt", "phase1")

				// Snapshot source → snap1
				snap1 := "chain-sor-snap1-" + proto.name
				restPVC1 := "chain-sor-rest1-" + proto.name
				restPod1 := "chain-sor-rest1-pod-" + proto.name
				snapshotAndRestore(ctx, f, proto, snap1, snapClass, srcPVC, restPVC1, restPod1)

				// Write "phase2" to restored volume
				By("Writing phase2 to restored volume")
				writeData(ctx, f.K8s, restPod1, "data.txt", "phase1\nphase2")

				// Snapshot restored → snap2
				snap2 := "chain-sor-snap2-" + proto.name
				By("Creating snapshot of the restored volume")
				err := f.K8s.CreateVolumeSnapshot(ctx, snap2, restPVC1, snapClass)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snap2)
				})
				err = f.K8s.WaitForSnapshotReady(ctx, snap2, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Restore snap2 → re-restored
				reRestPVC := "chain-sor-rerest-" + proto.name
				reRestPod := "chain-sor-rerest-pod-" + proto.name

				err = f.K8s.CreatePVCFromSnapshot(ctx, reRestPVC, snap2, proto.storageClass, pvcSize,
					[]corev1.PersistentVolumeAccessMode{proto.accessMode})
				Expect(err).NotTo(HaveOccurred())
				f.RegisterPVCCleanup(reRestPVC)

				err = f.K8s.WaitForPVCBound(ctx, reRestPVC, proto.pvcTimeout)
				Expect(err).NotTo(HaveOccurred())

				reRestPodObj, err := f.CreatePod(ctx, framework.PodOptions{
					Name:      reRestPod,
					PVCName:   reRestPVC,
					MountPath: mountPath,
				})
				Expect(err).NotTo(HaveOccurred())
				err = f.K8s.WaitForPodReady(ctx, reRestPodObj.Name, proto.podTimeout)
				Expect(err).NotTo(HaveOccurred())

				// Verify re-restored has phase1 + phase2
				By("Verifying re-restored volume has both phases")
				Expect(readData(ctx, f.K8s, reRestPod, "data.txt")).To(Equal("phase1\nphase2"))
			})

			It("should handle a 3-level deep chain", func() {
				ctx := context.Background()
				snapClass := "chain-deep-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Level 0: source → write "level-0"
				srcPVC := "chain-deep-src-" + proto.name
				srcPod := "chain-deep-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "level0.txt", "level-0")

				// Level 1: snapshot → restore → write "level-1"
				snap1 := "chain-deep-snap1-" + proto.name
				l1PVC := "chain-deep-l1-" + proto.name
				l1Pod := "chain-deep-l1-pod-" + proto.name
				snapshotAndRestore(ctx, f, proto, snap1, snapClass, srcPVC, l1PVC, l1Pod)
				writeData(ctx, f.K8s, l1Pod, "level1.txt", "level-1")

				// Level 2: snapshot L1 → restore → write "level-2"
				snap2 := "chain-deep-snap2-" + proto.name
				l2PVC := "chain-deep-l2-" + proto.name
				l2Pod := "chain-deep-l2-pod-" + proto.name
				snapshotAndRestore(ctx, f, proto, snap2, snapClass, l1PVC, l2PVC, l2Pod)
				writeData(ctx, f.K8s, l2Pod, "level2.txt", "level-2")

				// Verify: source only has level-0
				By("Verifying source has only level-0")
				Expect(readData(ctx, f.K8s, srcPod, "level0.txt")).To(Equal("level-0"))
				verifyDataAbsent(ctx, f.K8s, srcPod, "level1.txt")
				verifyDataAbsent(ctx, f.K8s, srcPod, "level2.txt")

				// Verify: L1 has level-0 + level-1
				By("Verifying L1 has level-0 and level-1")
				Expect(readData(ctx, f.K8s, l1Pod, "level0.txt")).To(Equal("level-0"))
				Expect(readData(ctx, f.K8s, l1Pod, "level1.txt")).To(Equal("level-1"))
				verifyDataAbsent(ctx, f.K8s, l1Pod, "level2.txt")

				// Verify: L2 has all three
				By("Verifying L2 has all three levels")
				Expect(readData(ctx, f.K8s, l2Pod, "level0.txt")).To(Equal("level-0"))
				Expect(readData(ctx, f.K8s, l2Pod, "level1.txt")).To(Equal("level-1"))
				Expect(readData(ctx, f.K8s, l2Pod, "level2.txt")).To(Equal("level-2"))
			})
		})
	}
})
