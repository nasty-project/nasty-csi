package iscsi_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("iSCSI Volume Adoption", func() {
	var f *framework.Framework
	var ctx context.Context

	// Timeouts
	const (
		pvcTimeout    = 120 * time.Second
		podTimeout    = 120 * time.Second
		deleteTimeout = 60 * time.Second
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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

	It("should adopt an orphaned volume with markAdoptable=true when adoptExisting=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		adoptableStorageClass := "nasty-csi-iscsi-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, adoptableStorageClass, "nasty.csi.io", map[string]string{
			"protocol":       "iscsi",
			"server":         f.Config.NAStyHost,
			"filesystem":           f.Config.NAStyFilesystem,
			"port":           "3260",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptableStorageClass)
		})

		By("Creating the original PVC")
		pvcName := "test-pvc-iscsi-adoption"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: adoptableStorageClass,
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

		zvolPath := volumeHandle
		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		}
		if f.Verbose() {
			GinkgoWriter.Printf("Expected block subvolume path on NASty: %s\n", zvolPath)
		}

		By("Creating a POD to write test data")
		podName := "test-pod-iscsi-adoption"
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
		testData := "iSCSI Adoption Test Data - Do Not Lose This!"
		_, err = f.K8s.ExecInPod(ctx, podName, []string{
			"sh", "-c", fmt.Sprintf("echo '%s' > /data/adoption-test.txt && sync", testData),
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

		By("Deleting the iSCSI target and extent to simulate orphaned volume (block subvolume exists, but iSCSI resources are missing)")
		err = f.NASty.DeleteISCSITarget(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred())
		err = f.NASty.DeleteISCSIExtent(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying iSCSI resources were deleted")
		targetExists, err := f.NASty.ISCSITargetExists(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred())
		Expect(targetExists).To(BeFalse(), "iSCSI target should be deleted")
		extentExists, err := f.NASty.ISCSIExtentExists(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred())
		Expect(extentExists).To(BeFalse(), "iSCSI extent should be deleted")
		if f.Verbose() {
			GinkgoWriter.Printf("Volume is now orphaned: block subvolume exists at %s but iSCSI resources are missing\n", zvolPath)
		}

		By("Creating StorageClass with adoptExisting=true for adoption")
		adoptingStorageClass := "nasty-csi-iscsi-adopting"
		err = f.K8s.CreateStorageClassWithParams(ctx, adoptingStorageClass, "nasty.csi.io", map[string]string{
			"protocol":      "iscsi",
			"server":        f.Config.NAStyHost,
			"filesystem":          f.Config.NAStyFilesystem,
			"port":          "3260",
			"fsType":        "ext4",
			"adoptExisting": "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, adoptingStorageClass)
		})

		By("Creating a new PVC with the adopting StorageClass")
		adoptedPVCName := "test-pvc-iscsi-adoption-new"
		adoptedPVC, err := f.K8s.CreatePVC(ctx, framework.PVCOptions{
			Name:             adoptedPVCName,
			StorageClassName: adoptingStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptedPVC).NotTo(BeNil())
		f.RegisterPVCCleanup(adoptedPVCName)

		By("Waiting for adopted PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, adoptedPVCName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting the new PV name and volume handle")
		newPVName, err := f.K8s.GetPVName(ctx, adoptedPVCName)
		Expect(err).NotTo(HaveOccurred())
		newVolumeHandle, err := f.K8s.GetVolumeHandle(ctx, newPVName)
		Expect(err).NotTo(HaveOccurred())
		if f.Verbose() {
			GinkgoWriter.Printf("New volume handle: %s\n", newVolumeHandle)
		}

		By("Creating a POD to verify the new volume")
		adoptedPodName := "test-pod-iscsi-adopted"
		adoptedPod, err := f.CreatePod(ctx, framework.PodOptions{
			Name:      adoptedPodName,
			PVCName:   adoptedPVCName,
			MountPath: "/data",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptedPod).NotTo(BeNil())

		By("Waiting for adopted POD to be ready")
		err = f.K8s.WaitForPodReady(ctx, adoptedPodName, podTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying the volume is usable")
		_, err = f.K8s.ExecInPod(ctx, adoptedPodName, []string{
			"sh", "-c", "echo 'test' > /data/new-test.txt && cat /data/new-test.txt",
		})
		Expect(err).NotTo(HaveOccurred())

		By("Cleaning up adopted resources")
		err = f.K8s.DeletePod(ctx, adoptedPodName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPodDeleted(ctx, adoptedPodName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		// The adopted PVC cleanup (via RegisterPVCCleanup) triggers CSI DeleteVolume
		// which cleans up the NEW iSCSI target/extent and block subvolume. However, the ORIGINAL
		// retained block subvolume (zvolPath) is left behind because adoption creates a new
		// subvolume path. Clean up the original retained block subvolume explicitly.
		By("Cleaning up original retained block subvolume from NASty")
		err = f.NASty.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete original retained block subvolume")
	})

	It("should mark a volume as adoptable when markAdoptable=true", func() {
		By("Creating StorageClass with markAdoptable=true and deleteStrategy=retain")
		markAdoptableStorageClass := "nasty-csi-iscsi-mark-adoptable"
		err := f.K8s.CreateStorageClassWithParams(ctx, markAdoptableStorageClass, "nasty.csi.io", map[string]string{
			"protocol":       "iscsi",
			"server":         f.Config.NAStyHost,
			"filesystem":           f.Config.NAStyFilesystem,
			"port":           "3260",
			"fsType":         "ext4",
			"deleteStrategy": "retain",
			"markAdoptable":  "true",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, markAdoptableStorageClass)
		})

		By("Creating a PVC")
		pvcName := "test-pvc-iscsi-marked-adoptable"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: markAdoptableStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting volume handle")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		volumeHandle, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())

		zvolPath := volumeHandle
		if f.Verbose() {
			GinkgoWriter.Printf("Volume handle: %s\n", volumeHandle)
		}
		if f.Verbose() {
			GinkgoWriter.Printf("Block subvolume path: %s\n", zvolPath)
		}

		By("Verifying adoptable property is set on NASty subvolume")
		Expect(f.NASty).NotTo(BeNil())
		adoptableValue, err := f.NASty.GetDatasetProperty(ctx, zvolPath, "nasty-csi:adoptable")
		Expect(err).NotTo(HaveOccurred())
		Expect(adoptableValue).To(Equal("true"), "Subvolume should have nasty-csi:adoptable=true")
		if f.Verbose() {
			GinkgoWriter.Printf("Subvolume %s has adoptable property set to: %s\n", zvolPath, adoptableValue)
		}

		By("Deleting PVC to trigger retain (not delete)")
		err = f.K8s.DeletePVC(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVCDeleted(ctx, pvcName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())
		err = f.K8s.WaitForPVDeleted(ctx, pvName, deleteTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying subvolume still exists on NASty after deletion")
		exists, err := f.NASty.DatasetExists(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue(), "Subvolume should be retained with deleteStrategy=retain")

		By("Cleaning up retained resources from NASty")
		err = f.NASty.DeleteISCSITarget(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained iSCSI target from NASty")
		err = f.NASty.DeleteISCSIExtent(ctx, volumeHandle)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete retained iSCSI extent from NASty")
		err = f.NASty.DeleteDataset(ctx, zvolPath)
		Expect(err).NotTo(HaveOccurred())
		if f.Verbose() {
			GinkgoWriter.Printf("Cleaned up retained subvolume: %s\n", zvolPath)
		}
	})
})
