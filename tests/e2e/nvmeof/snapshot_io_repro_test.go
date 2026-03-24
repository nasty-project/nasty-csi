package nvmeof_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

// Minimal reproducer for bcachefs snapshot I/O error over NVMe-oF.
//
// Steps:
//  1. Create block volume via NVMe-oF
//  2. Write data from pod
//  3. Take bcachefs snapshot
//  4. Write more data from pod → FAILS with Input/output error
//
// This works fine locally (loop device + direct mount) and over iSCSI.
// Only fails over NVMe-oF, suggesting the nvmet target or initiator
// doesn't handle the brief I/O stall during bcachefs snapshot.
var _ = Describe("NVMe-oF Snapshot I/O Repro", func() {
	var f *framework.Framework

	BeforeEach(func() {
		var err error
		f, err = framework.NewFramework()
		Expect(err).NotTo(HaveOccurred())
		err = f.Setup("nvmeof")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should write data after snapshot without I/O error", func() {
		ctx := context.Background()

		By("Creating PVC")
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             "snap-io-repro",
			StorageClassName: "nasty-csi-nvmeof",
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())

		By("Creating POD")
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      "snap-io-repro-pod",
			PVCName:   pvc.Name,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, pod.Name, 6*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PVC to become Bound")
		err = f.K8s.WaitForPVCBound(ctx, pvc.Name, 2*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Writing data BEFORE snapshot")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'before-snapshot' > /data/test.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Pre-snapshot write failed")

		By("Verifying pre-snapshot data")
		output, err := f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("before-snapshot"))

		By("Creating VolumeSnapshotClass")
		snapshotClass := "snap-io-repro-class"
		err = f.K8s.CreateVolumeSnapshotClassWithParams(ctx, snapshotClass, "nasty.csi.io", "Delete", map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshotClass(context.Background(), snapshotClass)
		})

		By("Taking snapshot")
		snapshotName := "snap-io-repro-snap"
		err = f.K8s.CreateVolumeSnapshot(ctx, snapshotName, pvc.Name, snapshotClass)
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteVolumeSnapshot(context.Background(), snapshotName)
		})

		By("Waiting for snapshot to be ready")
		err = f.K8s.WaitForSnapshotReady(ctx, snapshotName, 3*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		By("Writing data AFTER snapshot — this is where I/O error occurs")
		_, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"sh", "-c", "echo 'after-snapshot' > /data/test2.txt && sync"})
		Expect(err).NotTo(HaveOccurred(), "Post-snapshot write failed — bcachefs snapshot stalled NVMe-oF I/O")

		By("Verifying post-snapshot data")
		output, err = f.K8s.ExecInPod(ctx, pod.Name, []string{"cat", "/data/test2.txt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("after-snapshot"))
	})
})
