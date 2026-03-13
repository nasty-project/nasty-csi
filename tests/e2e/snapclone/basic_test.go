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

var _ = Describe("Basic Snapshot and Clone", func() {
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
		// capture range variable

		Context(fmt.Sprintf("[%s]", proto.name), func() {
			It("should create a snapshot and restore with correct point-in-time data", func() {
				ctx := context.Background()
				snapClass := "snapclone-basic-snap-" + proto.name

				createSnapshotClass(ctx, f, snapClass)

				// Source volume with initial data
				srcPVC := "basic-snap-src-" + proto.name
				srcPod := "basic-snap-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)

				By("Writing v1-data to source")
				writeData(ctx, f.K8s, srcPod, "version.txt", "v1-data")

				// Take snapshot
				snapName := "basic-snap-" + proto.name
				By("Creating snapshot of source")
				err := f.K8s.CreateVolumeSnapshot(ctx, snapName, srcPVC, snapClass)
				Expect(err).NotTo(HaveOccurred())
				f.Cleanup.Add(func() error {
					return f.K8s.DeleteVolumeSnapshot(context.Background(), snapName)
				})
				err = f.K8s.WaitForSnapshotReady(ctx, snapName, 3*time.Minute)
				Expect(err).NotTo(HaveOccurred())

				// Modify source after snapshot
				By("Writing v2-data to source (after snapshot)")
				writeData(ctx, f.K8s, srcPod, "version.txt", "v2-data")

				// Restore from snapshot
				restPVC := "basic-snap-rest-" + proto.name
				restPod := "basic-snap-rest-pod-" + proto.name

				By("Restoring PVC from snapshot")
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

				// Verify point-in-time correctness
				By("Verifying restored volume has v1-data (snapshot point-in-time)")
				Expect(readData(ctx, f.K8s, restPod, "version.txt")).To(Equal("v1-data"))

				By("Verifying source still has v2-data")
				Expect(readData(ctx, f.K8s, srcPod, "version.txt")).To(Equal("v2-data"))

				// Verify independence: write to restored, verify source unaffected
				By("Writing to restored volume and verifying independence")
				writeData(ctx, f.K8s, restPod, "restored-only.txt", "restored-write")
				verifyDataAbsent(ctx, f.K8s, srcPod, "restored-only.txt")
			})

			It("should create a COW clone with independent data", func() {
				ctx := context.Background()

				// Source volume
				srcPVC := "basic-clone-src-" + proto.name
				srcPod := "basic-clone-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)

				By("Writing source-original to source")
				writeData(ctx, f.K8s, srcPod, "data.txt", "source-original")

				// COW clone
				clonePVC := "basic-clone-" + proto.name
				clonePod := "basic-clone-pod-" + proto.name
				createCloneAndMount(ctx, f, proto, clonePVC, clonePod, srcPVC)

				By("Verifying clone has source-original")
				Expect(readData(ctx, f.K8s, clonePod, "data.txt")).To(Equal("source-original"))

				// Write to clone — verify NOT in source
				By("Writing clone-only to clone")
				writeData(ctx, f.K8s, clonePod, "clone-only.txt", "clone-data")
				verifyDataAbsent(ctx, f.K8s, srcPod, "clone-only.txt")

				// Write to source — verify NOT in clone
				By("Writing source-after to source")
				writeData(ctx, f.K8s, srcPod, "source-after.txt", "source-after")
				verifyDataAbsent(ctx, f.K8s, clonePod, "source-after.txt")
			})
		})
	}
})
