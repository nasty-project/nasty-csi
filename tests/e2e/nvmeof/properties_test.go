package nvmeof_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

var _ = Describe("NVMe-oF Storage Properties", func() {
	var f *framework.Framework
	var ctx context.Context
	var err error

	const (
		pvcTimeout = 360 * time.Second
		podTimeout = 360 * time.Second
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

	It("should create volume with custom storage properties", func() {
		By("Creating StorageClass with storage properties for NVMe-oF")
		storageClass := "nasty-csi-nvmeof-props"
		err = f.K8s.CreateStorageClassWithParamsAndBindingMode(ctx, storageClass, "nasty.csi.io", map[string]string{
			"protocol":    "nvmeof",
			"server":      f.Config.NAStyHost,
			"pool":        f.Config.NAStyPool,
			"transport":   "tcp",
			"port":        "4420",
			"fsType":      "ext4",
			"compression": "lz4",
		}, "Immediate")
		Expect(err).NotTo(HaveOccurred())
		f.Cleanup.Add(func() error {
			return f.K8s.DeleteStorageClass(ctx, storageClass)
		})

		By("Creating PVC")
		pvcName := "test-pvc-nvmeof-props"
		pvc, err := f.CreatePVC(ctx, framework.PVCOptions{
			Name:             pvcName,
			StorageClassName: storageClass,
			Size:             "1Gi",
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pvc).NotTo(BeNil())
		f.Cleanup.Add(func() error {
			return f.K8s.DeletePVC(ctx, pvcName)
		})

		By("Creating POD to trigger provisioning")
		podName := "test-pod-nvmeof-props"
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

		By("Getting subvolume path from PV")
		pvName, err := f.K8s.GetPVName(ctx, pvcName)
		Expect(err).NotTo(HaveOccurred())
		datasetPath, err := f.K8s.GetVolumeHandle(ctx, pvName)
		Expect(err).NotTo(HaveOccurred())
		Expect(datasetPath).NotTo(BeEmpty())

		By("Verifying compression is set to lz4")
		compression, err := f.NASty.GetSubvolumeProperty(ctx, datasetPath, "compression")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.ToLower(compression)).To(Equal("lz4"), "compression should be lz4")

		By("Verifying cluster_id user property is set")
		clusterID, err := f.NASty.GetDatasetProperty(ctx, datasetPath, "nasty-csi:cluster_id")
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterID).To(Equal(f.Config.ClusterID), "Subvolume should have nasty-csi:cluster_id matching configured cluster ID")
	})
})
