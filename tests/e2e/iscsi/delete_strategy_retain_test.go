package iscsi_test

import (
	"context"
	"fmt"
	"path"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Delete Strategy Retain", func() {
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

		err = f.Setup("iscsi")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should retain NASty resources when deleteStrategy=retain is set", func() {
		By("Creating StorageClass with deleteStrategy=retain")
		retainStorageClass := "nasty-csi-iscsi-retain"
		err = f.K8s.CreateStorageClassWithParams(ctx, retainStorageClass, "nasty.csi.io", map[string]string{
			"protocol":       "iscsi",
			"server":         f.Config.NAStyHost,
			"pool":           f.Config.NAStyPool,
			"port":           "3260",
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
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected ZVOL path on NASty: %s\n", zvolPath)

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
		testData := "Retain Test Data iSCSI"
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

		By("Verifying ZVOL still exists on NASty")
		Expect(f.NASty).NotTo(BeNil(), "NASty verifier must be available for this test")
		exists, err := f.NASty.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "ZVOL should still exist on NASty after PVC deletion with deleteStrategy=retain")

		By("ZVOL confirmed to still exist on NASty - retain strategy working correctly")
		GinkgoWriter.Printf("Successfully verified ZVOL %s was retained on NASty\n", zvolPath)

		By("Cleaning up retained iSCSI target from NASty")
		targetName := path.Base(volumeHandle)
		err = f.NASty.DeleteISCSITarget(ctx, targetName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained iSCSI target from NASty")

		By("Cleaning up retained iSCSI extent from NASty")
		err = f.NASty.DeleteISCSIExtent(ctx, targetName)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained iSCSI extent from NASty")

		By("Cleaning up retained ZVOL from NASty")
		err = f.NASty.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained ZVOL from NASty")

		By("Verifying ZVOL was successfully deleted from NASty")
		exists, err = f.NASty.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "ZVOL should no longer exist on NASty after cleanup")

		By("Cleanup verified - ZVOL and iSCSI resources successfully removed from NASty")
		GinkgoWriter.Printf("Successfully cleaned up ZVOL %s from NASty\n", zvolPath)
	})
})
