package driver

import (
	"context"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nasty-project/nasty-csi/pkg/mount"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// checkSingleWriterPublication runs under the volume lifecycle lock. Production
// uses the host mount table, so exclusivity survives a driver restart. Test mode
// substitutes successful publications because it does not create real mounts.
func (s *NodeService) checkSingleWriterPublication(ctx context.Context, req *csi.NodePublishVolumeRequest) error {
	if req.GetVolumeCapability().GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER {
		return nil
	}
	var refs []string
	if s.testMode {
		if value, ok := s.testPublications.Load(req.GetVolumeId()); ok {
			targets, ok := value.(map[string]bool)
			if !ok {
				return status.Error(codes.Internal, "Invalid test publication state")
			}
			for target := range targets {
				refs = append(refs, target)
			}
		}
	} else {
		if req.GetStagingTargetPath() == "" {
			return status.Error(codes.InvalidArgument, "Staging target path is required")
		}
		getRefs := s.getMountRefsFn
		if getRefs == nil {
			getRefs = mount.GetBindMountRefs
		}
		var err error
		refs, err = getRefs(ctx, req.GetStagingTargetPath())
		if err != nil {
			return status.Errorf(codes.Internal, "Failed to check existing volume publications: %v", err)
		}
	}
	for _, ref := range refs {
		if filepath.Clean(ref) != filepath.Clean(req.GetTargetPath()) {
			return status.Error(codes.FailedPrecondition, "Single-node-single-writer volume is already published at a different target path")
		}
	}
	return nil
}
