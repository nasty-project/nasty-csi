package nfs_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("NFS Delete Strategy Retain", func() {
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

		err = f.Setup("nfs")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if f != nil {
			f.Teardown()
		}
	})

	It("should retain NASty resources when deleteStrategy=retain is set", func() {
		By("Creating StorageClass with deleteStrategy=retain")
		retainStorageClass := "nasty-csi-nfs-retain"
		err = f.K8s.CreateStorageClassWithParams(ctx, retainStorageClass, "nasty.csi.io", map[string]string{
			"protocol":       "nfs",
			"server":         f.Config.NAStyHost,
			"filesystem":           f.Config.NAStyFilesystem,
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
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
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

		// Volume handle is the full dataset path (e.g., filesystem/parent/pvc-xxx)
		datasetPath := volumeHandle
		// NFS share path format: /fs/<filesystem>/<subvolume>
		nfsSharePath := "/fs/" + volumeHandle
		GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		GinkgoWriter.Printf("Expected dataset path on NASty: %s\n", datasetPath)
		GinkgoWriter.Printf("Expected NFS share path on NASty: %s\n", nfsSharePath)

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
		testData := "Retain Test Data"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/retain-test.txt", testData),
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

		By("Verifying dataset still exists on NASty")
		Expect(f.NASty).NotTo(BeNil(), "NASty verifier must be available for this test")
		exists, err := f.NASty.DatasetExists(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Dataset should still exist on NASty after PVC deletion with deleteStrategy=retain")

		By("Verifying NFS share still exists on NASty")
		shareExists, err := f.NASty.NFSShareExists(ctx, nfsSharePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(shareExists).To(BeTrue(), "NFS share should still exist on NASty after PVC deletion with deleteStrategy=retain")

		By("Dataset and NFS share confirmed to still exist on NASty - retain strategy working correctly")
		GinkgoWriter.Printf("Successfully verified dataset %s was retained on NASty\n", datasetPath)
		GinkgoWriter.Printf("Successfully verified NFS share %s was retained on NASty\n", nfsSharePath)

		By("Cleaning up retained NFS share from NASty")
		err = f.NASty.DeleteNFSShare(ctx, nfsSharePath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained NFS share from NASty")

		By("Cleaning up retained dataset from NASty")
		err = f.NASty.DeleteDataset(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained dataset from NASty")

		By("Verifying dataset was successfully deleted from NASty")
		exists, err = f.NASty.DatasetExists(ctx, datasetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeFalse(), "Dataset should no longer exist on NASty after cleanup")

		By("Verifying NFS share was successfully deleted from NASty")
		shareExists, err = f.NASty.NFSShareExists(ctx, nfsSharePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(shareExists).To(BeFalse(), "NFS share should no longer exist on NASty after cleanup")

		By("Cleanup verified - dataset and NFS share successfully removed from NASty")
		GinkgoWriter.Printf("Successfully cleaned up dataset %s from NASty\n", datasetPath)
		GinkgoWriter.Printf("Successfully cleaned up NFS share %s from NASty\n", nfsSharePath)
	})
})
