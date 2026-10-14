package framework

import (
	_ "embed"
	"strings"
)

//go:embed test-image.txt
var testImageReference string

// TestImage is the digest-pinned workload image also preloaded by CI.
func TestImage() string {
	return strings.TrimSpace(testImageReference)
}
