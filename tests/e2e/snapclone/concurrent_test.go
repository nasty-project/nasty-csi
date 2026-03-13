package snapclone

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("Concurrent Operations", func() {
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
			It("should handle concurrent snapshots and clones without cross-contamination", func() {
				ctx := context.Background()
				snapClass := "concurrent-" + proto.name
				createSnapshotClass(ctx, f, snapClass)

				// Source volume
				srcPVC := "concurrent-src-" + proto.name
				srcPod := "concurrent-src-pod-" + proto.name
				createAndMountPVC(ctx, f, proto, srcPVC, srcPod)
				writeData(ctx, f.K8s, srcPod, "source.txt", "source-data")

				// Concurrently create 3 snapshots + 2 COW clones
				const numSnaps = 3
				const numClones = 2

				snapNames := make([]string, numSnaps)
				cloneNames := make([]string, numClones)

				var mu sync.Mutex
				var errs []error

				var wg sync.WaitGroup

				// Create snapshots concurrently
				for i := range numSnaps {
					snapNames[i] = fmt.Sprintf("concurrent-snap-%d-%s", i, proto.name)
					wg.Add(1)
					go func(idx int, name string) {
						defer wg.Done()
						defer GinkgoRecover()
						if err := f.K8s.CreateVolumeSnapshot(ctx, name, srcPVC, snapClass); err != nil {
							mu.Lock()
							errs = append(errs, fmt.Errorf("snapshot %d: %w", idx, err))
							mu.Unlock()
							return
						}
						f.Cleanup.Add(func() error {
							return f.K8s.DeleteVolumeSnapshot(context.Background(), name)
						})
					}(i, snapNames[i])
				}

				// Create clones concurrently
				for i := range numClones {
					cloneNames[i] = fmt.Sprintf("concurrent-clone-%d-%s", i, proto.name)
					wg.Add(1)
					go func(idx int, name string) {
						defer wg.Done()
						defer GinkgoRecover()
						if err := f.K8s.CreatePVCFromPVC(ctx, name, srcPVC, proto.storageClass, pvcSize,
							[]corev1.PersistentVolumeAccessMode{proto.accessMode}); err != nil {
							mu.Lock()
							errs = append(errs, fmt.Errorf("clone %d: %w", idx, err))
							mu.Unlock()
							return
						}
						f.RegisterPVCCleanup(name)
					}(i, cloneNames[i])
				}

				wg.Wait()
				Expect(errs).To(BeEmpty(), "Concurrent creation errors: %v", errs)

				// Wait for all snapshots to be ready
				By("Waiting for all snapshots to be ready")
				for _, name := range snapNames {
					err := f.K8s.WaitForSnapshotReady(ctx, name, 3*time.Minute)
					Expect(err).NotTo(HaveOccurred(), "Snapshot %s did not become ready", name)
				}

				// Wait for all clones to bind
				By("Waiting for all clone PVCs to bind")
				for _, name := range cloneNames {
					err := f.K8s.WaitForPVCBound(ctx, name, proto.pvcTimeout)
					Expect(err).NotTo(HaveOccurred(), "Clone PVC %s did not become Bound", name)
				}

				// Restore from each snapshot
				By("Restoring from each snapshot")
				restNames := make([]string, numSnaps)
				restPodNames := make([]string, numSnaps)
				for i, snapName := range snapNames {
					restNames[i] = fmt.Sprintf("concurrent-rest-%d-%s", i, proto.name)
					restPodNames[i] = fmt.Sprintf("concurrent-rest-pod-%d-%s", i, proto.name)

					err := f.K8s.CreatePVCFromSnapshot(ctx, restNames[i], snapName, proto.storageClass, pvcSize,
						[]corev1.PersistentVolumeAccessMode{proto.accessMode})
					Expect(err).NotTo(HaveOccurred())
					f.RegisterPVCCleanup(restNames[i])

					err = f.K8s.WaitForPVCBound(ctx, restNames[i], proto.pvcTimeout)
					Expect(err).NotTo(HaveOccurred())
				}

				// Mount and verify all 5 derivatives (3 restores + 2 clones)
				// Verify restored volumes
				By("Verifying all restored volumes have source data")
				for i := range numSnaps {
					pod, err := f.CreatePod(ctx, framework.PodOptions{
						Name:      restPodNames[i],
						PVCName:   restNames[i],
						MountPath: mountPath,
					})
					Expect(err).NotTo(HaveOccurred())
					err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
					Expect(err).NotTo(HaveOccurred())

					Expect(readData(ctx, f.K8s, restPodNames[i], "source.txt")).To(Equal("source-data"),
						"Restored volume %d should have source data", i)
				}

				// Verify clone volumes
				By("Verifying all clone volumes have source data")
				clonePodNames := make([]string, numClones)
				for i := range numClones {
					clonePodNames[i] = fmt.Sprintf("concurrent-clone-pod-%d-%s", i, proto.name)
					pod, err := f.CreatePod(ctx, framework.PodOptions{
						Name:      clonePodNames[i],
						PVCName:   cloneNames[i],
						MountPath: mountPath,
					})
					Expect(err).NotTo(HaveOccurred())
					err = f.K8s.WaitForPodReady(ctx, pod.Name, proto.podTimeout)
					Expect(err).NotTo(HaveOccurred())

					Expect(readData(ctx, f.K8s, clonePodNames[i], "source.txt")).To(Equal("source-data"),
						"Clone volume %d should have source data", i)
				}

				// Write unique data to each derivative and verify no cross-contamination
				By("Writing unique data to each derivative")
				allPodNames := make([]string, 0, len(restPodNames)+len(clonePodNames))
				allPodNames = append(allPodNames, restPodNames...)
				allPodNames = append(allPodNames, clonePodNames...)
				for i, podName := range allPodNames {
					uniqueFile := fmt.Sprintf("unique-%d.txt", i)
					uniqueData := fmt.Sprintf("derivative-%d-data", i)
					writeData(ctx, f.K8s, podName, uniqueFile, uniqueData)
				}

				By("Verifying no cross-contamination between derivatives")
				for i, podName := range allPodNames {
					// This derivative's file should exist
					ownFile := fmt.Sprintf("unique-%d.txt", i)
					Expect(readData(ctx, f.K8s, podName, ownFile)).To(
						Equal(fmt.Sprintf("derivative-%d-data", i)))

					// Other derivatives' files should NOT exist
					for j := range allPodNames {
						if j == i {
							continue
						}
						otherFile := fmt.Sprintf("unique-%d.txt", j)
						verifyDataAbsent(ctx, f.K8s, podName, otherFile)
					}
				}
			})
		})
	}
})
