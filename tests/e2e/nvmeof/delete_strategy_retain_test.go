package nvmeof_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Delete Strategy Retain", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	// Timeouts
	const (
		pvcTimeout    = 120 * time.Second
		podTimeout    = 120 * time.Second
		deleteTimeout = 60 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
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

	It("should retain NASty resources when deleteStrategy=retain is set", func() {
		By("Creating StorageClass with deleteStrategy=retain")
		retainStorageClass := "nasty-csi-nvmeof-retain"
		err = f.K8s.CreateStorageClassWithParams(ctx, retainStorageClass, "nasty.csi.io", map[string]string{
			"protocol":       "nvmeof",
			"server":         f.Config.NAStyHost,
			"pool":           f.Config.NAStyPool,
			"transport":      "tcp",
			"port":           "4420",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, retainStorageClass)
		})

		By("Creating a PVC with retain StorageClass")
		pvcName := "test-pvc-retain"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: retainStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting PV name and volume handle for later verification")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvName).NotTo(BeEmpty())

		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(volumeHandle).NotTo(BeEmpty())

		// Volume handle is the full dataset path (e.g., pool/parent/pvc-xxx)
		zvolPath := volumeHandle
		pv, err := f.K8s.GetPV(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Spec.CSI).NotTo(BeNil(), "PV should contain CSI spec")
		subsystemNQN, ok := pv.Spec.CSI.VolumeAttributes["nqn"]
		Expect(ok).To(BeTrue(), "PV should contain CSI volumeAttributes.nqn")
		Expect(subsystemNQN).NotTo(BeEmpty())
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected block subvolume path on NASty: %s\n", zvolPath)
		GinkgoWriter.Printf("Expected NVMe-oF subsystem NQN: %s\n", subsystemNQN)

		By("Creating a POD to verify volume works")
		podName := "test-pod-retain"
		pod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      podName,
			PVCName:   pvcName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		By("Waiting for POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, podName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Writing test data to verify volume is working")
		testData := "Retain Test Data NVMe-oF"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/retain-test.txt && sync", testData),
		})
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the POD")
		err = f.K8s.DeletePod(ctx, podName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, podName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Deleting the PVC (triggers DeleteVolume with retain strategy)")
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for PV to be deleted from Kubernetes")
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying block subvolume still exists on NASty")
		Expect(f.NASty).NotTo(BeNil(), "NASty verifier must be available for this test")
		exists, err := f.NASty.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Block subvolume should still exist on NASty after PVC deletion with deleteStrategy=retain")

		By("Verifying NVMe-oF subsystem still exists on NASty")
		subsystemExists, err := f.NASty.NVMeOFSubsystemExists(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystemExists).To(BeTrue(), "NVMe-oF subsystem should still exist on NASty after PVC deletion with deleteStrategy=retain")

		By("Block subvolume and subsystem confirmed to still exist on NASty - retain strategy working correctly")
		GinkgoWriter.Printf("Successfully verified block subvolume %s was retained on NASty\n", zvolPath)
		GinkgoWriter.Printf("Successfully verified NVMe-oF subsystem %s was retained on NASty\n", subsystemNQN)

		By("Cleaning up retained NVMe-oF subsystem from NASty")
		err = f.NASty.DeleteNVMeOFSubsystem(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained NVMe-oF subsystem from NASty")

		By("Cleaning up retained block subvolume from NASty")
		err = f.NASty.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained block subvolume from NASty")

		By("Verifying block subvolume was successfully deleted from NASty")
		exists, err = f.NASty.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Block subvolume should no longer exist on NASty after cleanup")

		By("Verifying NVMe-oF subsystem was successfully deleted from NASty")
		subsystemExists, err = f.NASty.NVMeOFSubsystemExists(ctx, subsystemNQN)
		Expect(err).NotTo(HaveOccurred())
		Expect(subsystemExists).To(BeFalse(), "NVMe-oF subsystem should no longer exist on NASty after cleanup")

		By("Cleanup verified - block subvolume and subsystem successfully removed from NASty")
		GinkgoWriter.Printf("Successfully cleaned up block subvolume %s from NASty\n", zvolPath)
		GinkgoWriter.Printf("Successfully cleaned up NVMe-oF subsystem %s from NASty\n", subsystemNQN)
	})
})
