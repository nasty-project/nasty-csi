// Package e2e contains end-to-end tests for the NASty CSI driver.
package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	// Cross-protocol tests need both NFS and NVMe-oF
	err := framework.SetupSuite("both")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
