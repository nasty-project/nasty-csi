package nfs_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("NFS ZFS Properties", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	const (
		pvcTimeout = 120 * time.Second
		podTimeout = 120 * time.Second
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

	It("should create volume with custom ZFS properties", func() {
		By("Creating StorageClass with ZFS properties")
		zfsStorageClass := "nasty-csi-nfs-zfsprops"
		err = f.K8s.CreateStorageClassWithParams(ctx, zfsStorageClass, "nasty.csi.io", map[string]string{
			"protocol":        "nfs",
			"server":          f.Config.NAStyHost,
			"pool":            f.Config.NAStyPool,
			"zfs.compression": "lz4",
			"zfs.atime":       "off",
			"zfs.recordsize":  "128K",
		})
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, zfsStorageClass)
		})

		By("Creating PVC")
		pvcName := "test-pvc-zfsprops"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: zfsStorageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating POD to trigger provisioning")
		podName := "test-pod-zfsprops"
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

		By("Waiting for PVC to be bound")
		err = f.K8s.WaitForPVCBound(ctx, pvcName, pvcTimeout)
		Expect(err).NotTo(HaveOccurred())

		By("Getting dataset path from PV")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		datasetPath, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(datasetPath).NotTo(BeEmpty())

		By("Verifying compression is set to lz4")
		compression, err := f.NASty.GetZFSProperty(ctx, datasetPath, "compression")
		Expect(err).NotTo(HaveOccurred())
		Expect(compression).To(Equal("LZ4"), "compression should be LZ4")

		By("Verifying atime is set to off")
		atime, err := f.NASty.GetZFSProperty(ctx, datasetPath, "atime")
		Expect(err).NotTo(HaveOccurred())
		Expect(atime).To(Equal("OFF"), "atime should be OFF")

		By("Verifying recordsize is set to 128K")
		recordsize, err := f.NASty.GetZFSProperty(ctx, datasetPath, "recordsize")
		Expect(err).NotTo(HaveOccurred())
		Expect(recordsize).To(Equal("128K"), "recordsize should be 128K")

		By("Verifying cluster_id user property is set")
		clusterID, err := f.NASty.GetDatasetProperty(ctx, datasetPath, "nasty-csi:cluster_id")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterID).To(Equal(f.Config.ClusterID), "Dataset should have nasty-csi:cluster_id matching configured cluster ID")
	})
})
