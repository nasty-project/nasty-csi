// Package snapclone contains E2E stress tests for snapshot/clone lifecycle.
// These tests exercise complex dependency graphs across NFS, SMB, NVMe-oF, and iSCSI protocols.
package snapclone

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nasty-project/nasty-csi/tests/e2e/framework"
)

func TestSnapclone(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Snapshot/Clone Stress E2E Suite")
}

var _ = BeforeSuite(func() {
	err := framework.SetupSuite("all")
	Expect(err).NotTo(HaveOccurred(), "Failed to setup suite")
})

var _ = AfterSuite(func() {
	framework.TeardownSuite()
})
