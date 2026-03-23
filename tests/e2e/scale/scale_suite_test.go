// Package scale contains E2E tests that validate CSI operations work correctly
// when NASty has a large number of non-CSI-managed subvolumes, block subvolumes,
// snapshots, and NFS shares. This simulates a real-world environment where users
// have pre-existing data on the same pool used by the CSI driver.
//
// This test suite is intended to be run manually via the "Scale Tests" workflow,
// not as part of regular CI. It populates NASty with noise data, runs CSI
// operations, verifies they work correctly, and cleans up.
package scale

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
	nastyapi "github.com/nasty-project/nasty-go"
)

const (
	defaultNoiseDatasetCount     = 30
	defaultNoiseBlockSubvolCount = 10
	snapshotsPerDataset          = 2
	nfsShareCount                = 5
)

var (
	noiseVerifier *framework.NAStyVerifier
	noiseParent   string // Parent subvolume path for all noise data
	noisePool     string

	// Actual counts (may be overridden by env vars).
	actualDatasetCount     int
	actualBlockSubvolCount int
)

func TestScale(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Scale E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("nfs")
	Expect(err).NotTo(HaveOccurred(), "Failed to set up CSI driver")

	createNoiseData()
})

var _ = AfterSuite(func() {
	cleanupNoiseData()
	framework.TeardownSuite()
})

// getEnvInt reads an integer from an environment variable, returning defaultVal if unset.
func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}

// parsePoolName extracts the pool name (first component) from a path like "pool/subvol/child".

// parseSubvolName extracts the subvolume name (rest after first /) from a path like "pool/subvol/child".
func parseSubvolName(path string) string {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// createNoiseData populates NASty with non-CSI-managed resources.
// All noise data goes under a single parent subvolume for easy cleanup.
func createNoiseData() {
	cfg, err := framework.NewConfig()
	Expect(err).NotTo(HaveOccurred())

	noisePool = cfg.NAStyPool
	actualDatasetCount = getEnvInt("NOISE_DATASET_COUNT", defaultNoiseDatasetCount)
	actualBlockSubvolCount = getEnvInt("NOISE_BLOCK_SUBVOL_COUNT", defaultNoiseBlockSubvolCount)

	noiseVerifier, err = framework.NewNAStyVerifier(cfg.NAStyHost, cfg.NAStyAPIKey)
	Expect(err).NotTo(HaveOccurred(), "Failed to connect to NASty for noise creation")

	client := noiseVerifier.Client()
	ctx := context.Background()

	noiseParent = fmt.Sprintf("%s/e2e-noise-%d", noisePool, time.Now().UnixNano())
	klog.Infof("Creating noise data under %s (%d subvolumes, %d block subvolumes)",
		noiseParent, actualDatasetCount, actualBlockSubvolCount)

	// Parent subvolume
	_, err = client.CreateSubvolume(ctx, nastyapi.SubvolumeCreateParams{
		Pool:          noisePool,
		Name:          parseSubvolName(noiseParent),
		SubvolumeType: "filesystem",
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to create noise parent subvolume")

	// Filesystem subvolumes with snapshots
	fsParentName := parseSubvolName(noiseParent) + "/datasets"
	_, err = client.CreateSubvolume(ctx, nastyapi.SubvolumeCreateParams{
		Pool:          noisePool,
		Name:          fsParentName,
		SubvolumeType: "filesystem",
	})
	Expect(err).NotTo(HaveOccurred())

	klog.Infof("Creating %d noise filesystem subvolumes with %d snapshots each",
		actualDatasetCount, snapshotsPerDataset)
	for i := 1; i <= actualDatasetCount; i++ {
		dsSubvolName := fmt.Sprintf("%s/ds-%03d", fsParentName, i)
		_, dsErr := client.CreateSubvolume(ctx, nastyapi.SubvolumeCreateParams{
			Pool:          noisePool,
			Name:          dsSubvolName,
			SubvolumeType: "filesystem",
		})
		Expect(dsErr).NotTo(HaveOccurred(), "Failed to create noise subvolume %s", dsSubvolName)

		for j := 1; j <= snapshotsPerDataset; j++ {
			_, snapErr := client.CreateSnapshot(ctx, nastyapi.SnapshotCreateParams{
				Pool:      noisePool,
				Subvolume: dsSubvolName,
				Name:      fmt.Sprintf("snap-%03d", j),
			})
			Expect(snapErr).NotTo(HaveOccurred(), "Failed to create snapshot for %s", dsSubvolName)
		}
	}

	// NFS shares on first N subvolumes
	klog.Infof("Creating %d noise NFS shares", nfsShareCount)
	enabled := true
	for i := 1; i <= nfsShareCount; i++ {
		sharePath := fmt.Sprintf("/mnt/%s/%s/ds-%03d", noisePool, fsParentName, i)
		_, shareErr := client.CreateNFSShare(ctx, nastyapi.NFSShareCreateParams{
			Path:    sharePath,
			Enabled: &enabled,
			Comment: fmt.Sprintf("e2e-noise-share-%d", i),
		})
		Expect(shareErr).NotTo(HaveOccurred(), "Failed to create noise NFS share for %s", sharePath)
	}

	// Block subvolumes with snapshots
	zvolParentName := parseSubvolName(noiseParent) + "/zvols"
	_, err = client.CreateSubvolume(ctx, nastyapi.SubvolumeCreateParams{
		Pool:          noisePool,
		Name:          zvolParentName,
		SubvolumeType: "filesystem",
	})
	Expect(err).NotTo(HaveOccurred())

	klog.Infof("Creating %d noise block subvolumes with %d snapshots each", actualBlockSubvolCount, snapshotsPerDataset)
	volsize := uint64(1073741824) // 1 GiB
	for i := 1; i <= actualBlockSubvolCount; i++ {
		zvolSubvolName := fmt.Sprintf("%s/zvol-%03d", zvolParentName, i)
		_, zvolErr := client.CreateSubvolume(ctx, nastyapi.SubvolumeCreateParams{
			Pool:          noisePool,
			Name:          zvolSubvolName,
			SubvolumeType: "block",
			VolsizeBytes:  &volsize,
		})
		Expect(zvolErr).NotTo(HaveOccurred(), "Failed to create noise block subvolume %s", zvolSubvolName)

		for j := 1; j <= snapshotsPerDataset; j++ {
			_, snapErr := client.CreateSnapshot(ctx, nastyapi.SnapshotCreateParams{
				Pool:      noisePool,
				Subvolume: zvolSubvolName,
				Name:      fmt.Sprintf("snap-%03d", j),
			})
			Expect(snapErr).NotTo(HaveOccurred(), "Failed to create snapshot for %s", zvolSubvolName)
		}
	}

	totalSnapshots := (actualDatasetCount + actualBlockSubvolCount) * snapshotsPerDataset
	totalResources := actualDatasetCount + actualBlockSubvolCount + totalSnapshots + nfsShareCount
	klog.Infof("Noise data creation complete: %d resources (%d subvolumes, %d block subvolumes, %d snapshots, %d NFS shares)",
		totalResources, actualDatasetCount, actualBlockSubvolCount, totalSnapshots, nfsShareCount)
}

// cleanupNoiseData removes all noise data. NFS shares first, then recursive subvolume delete.
func cleanupNoiseData() {
	if noiseVerifier == nil || noiseParent == "" {
		return
	}
	defer noiseVerifier.Close()

	ctx := context.Background()
	klog.Infof("Cleaning up noise data under %s", noiseParent)

	// Delete NFS shares first (they reference subvolume paths)
	fsParentName := parseSubvolName(noiseParent) + "/datasets"
	for i := 1; i <= nfsShareCount; i++ {
		sharePath := fmt.Sprintf("/mnt/%s/%s/ds-%03d", noisePool, fsParentName, i)
		if err := noiseVerifier.DeleteNFSShare(ctx, sharePath); err != nil {
			klog.Warningf("Failed to delete noise NFS share %s: %v", sharePath, err)
		}
	}

	// Delete parent subvolume recursively (handles all children, snapshots)
	if err := noiseVerifier.Client().DeleteSubvolume(ctx, noisePool, parseSubvolName(noiseParent)); err != nil {
		klog.Errorf("Failed to delete noise parent subvolume %s: %v", noiseParent, err)
	} else {
		klog.Infof("Noise data cleanup complete")
	}
}
