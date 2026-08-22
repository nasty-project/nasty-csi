package driver

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	nastyapi "github.com/nasty-project/nasty-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func identityTestRequest(protocol string, adopt bool) *csi.CreateVolumeRequest {
	params := map[string]string{
		"protocol":   protocol,
		"filesystem": "tank",
		"server":     "192.0.2.1",
	}
	if adopt {
		params["adoptExisting"] = VolumeContextValueTrue
	}
	capability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
	}
	if protocol == ProtocolISCSI || protocol == ProtocolNVMeOF {
		capability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	}
	return &csi.CreateVolumeRequest{
		Name:               "claim",
		Parameters:         params,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: MinVolumeSize},
		VolumeCapabilities: []*csi.VolumeCapability{capability},
	}
}

func TestExistingUnmanagedVolumeRequiresExplicitAdoption(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			for _, adopt := range []bool{false, true} {
				name := "reject"
				if adopt {
					name = "adopt"
				}
				t.Run(name, func(t *testing.T) {
					device := "/dev/mapper/claim"
					subvol := &nastyapi.Subvolume{
						Filesystem:  "tank",
						Name:        "claim",
						Path:        "/fs/tank/claim",
						BlockDevice: &device,
					}
					var written map[string]string
					client := &mockAPIClient{
						GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
							return subvol, nil
						},
						SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
							written = props
							return subvol, nil
						},
						ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
							return []nastyapi.NFSShare{{ID: "nfs-1", Path: subvol.Path}}, nil
						},
						ListSMBSharesFunc: func(context.Context) ([]nastyapi.SMBShare, error) {
							return []nastyapi.SMBShare{{ID: "smb-1", Name: "claim", Path: subvol.Path}}, nil
						},
						ListISCSITargetsFunc: func(context.Context) ([]nastyapi.ISCSITarget, error) {
							return []nastyapi.ISCSITarget{{
								ID: "iscsi-1", IQN: generateIQN("claim"),
								Luns: []nastyapi.ISCSILun{{BackstorePath: device}},
							}}, nil
						},
						ListNVMeOFSubsystemsFunc: func(context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
							return []nastyapi.NVMeOFSubsystem{{
								ID: "nvme-1", NQN: "nqn.backend.generated:claim",
								Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: device}},
							}}, nil
						},
					}
					service := NewControllerService(client, NewNodeRegistry(), "cluster-a")
					resp, err := service.createVolumeByProtocol(context.Background(), identityTestRequest(protocol, adopt), protocol)
					if !adopt {
						if status.Code(err) != codes.AlreadyExists {
							t.Fatalf("unmanaged create code = %v, want AlreadyExists (error: %v)", status.Code(err), err)
						}
						if written != nil {
							t.Fatal("unmanaged volume was mutated without adoptExisting")
						}
						return
					}
					if err != nil || resp == nil || resp.Volume.GetVolumeId() != "tank/claim" {
						t.Fatalf("explicit adoption failed: response=%v error=%v", resp, err)
					}
					invalidIdentity := written[propertyCSIRequestName] != "claim" || written[nastyapi.PropertyCSIVolumeName] != "claim" ||
						written[propertyIdentityVersion] != identityVersion2 || !uuidV4Regex.MatchString(written[propertyVolumeUUID])
					if invalidIdentity {
						t.Fatalf("adoption identity properties are incomplete: %#v", written)
					}
				})
			}
		})
	}
}

func TestExistingManagedIdentityGate(t *testing.T) {
	req := identityTestRequest(ProtocolNFS, false)
	const existingUUID = "12345678-1234-4123-8123-123456789abc"
	matching := &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", Properties: map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: "claim",
		nastyapi.PropertyProtocol:      ProtocolNFS,
		propertyCSIRequestName:         "claim",
		propertyVolumeUUID:             existingUUID,
		propertyIdentityVersion:        identityVersion2,
	}}
	decision, err := decideExistingVolumeIdentity(req, ProtocolNFS, "claim", matching)
	if err != nil {
		t.Fatalf("matching retry rejected: %v", err)
	}
	if decision.persist || decision.properties[propertyVolumeUUID] != existingUUID {
		t.Fatalf("matching retry did not preserve UUID: %#v", decision)
	}

	matching.Properties[propertyCSIRequestName] = "another-request"
	if _, err := decideExistingVolumeIdentity(req, ProtocolNFS, "claim", matching); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("mismatched managed request code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestLegacyNameReconciliationPreservesHandle(t *testing.T) {
	params := map[string]string{
		"protocol":        ProtocolNFS,
		"filesystem":      "tank",
		ParamNameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}",
		CSIPVCNamespace:   "team",
		CSIPVCName:        "data",
	}
	req := &csi.CreateVolumeRequest{
		Name:               "pvc-new-request",
		Parameters:         params,
		CapacityRange:      &csi.CapacityRange{RequiredBytes: MinVolumeSize},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}},
	}
	legacy := &nastyapi.Subvolume{
		Filesystem: "tank",
		Name:       "team-data",
		Path:       "/fs/tank/team-data",
		Properties: map[string]string{
			nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
			nastyapi.PropertyCSIVolumeName: "team-data",
			nastyapi.PropertyProtocol:      ProtocolNFS,
			nastyapi.PropertyPVCNamespace:  "team",
			nastyapi.PropertyPVCName:       "data",
		},
	}
	client := &mockAPIClient{
		GetSubvolumeFunc: func(_ context.Context, _, name string) (*nastyapi.Subvolume, error) {
			if name == legacy.Name {
				return legacy, nil
			}
			return nil, nastyapi.ErrDatasetNotFound
		},
		ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
			return []nastyapi.NFSShare{{ID: "share-1", Path: legacy.Path}}, nil
		},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	resp, err := service.createNFSVolume(context.Background(), req)
	if err != nil {
		t.Fatalf("legacy reconciliation failed: %v", err)
	}
	if resp.Volume.GetVolumeId() != "tank/team-data" {
		t.Fatalf("legacy handle = %q, want tank/team-data", resp.Volume.GetVolumeId())
	}
}

func TestIdentityPropertyFailureIsCreateFailure(t *testing.T) {
	for _, adoption := range []bool{false, true} {
		name := "new volume"
		if adoption {
			name = "explicit adoption"
		}
		t.Run(name, func(t *testing.T) {
			req := identityTestRequest(ProtocolNFS, adoption)
			subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", Path: "/fs/tank/claim", Created: true}
			endpointCalls := 0
			client := &mockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					if adoption {
						return subvol, nil
					}
					return nil, nastyapi.ErrDatasetNotFound
				},
				CreateSubvolumeFunc: func(context.Context, nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return subvol, nil
				},
				CreateNFSShareFunc: func(context.Context, nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					endpointCalls++
					return &nastyapi.NFSShare{ID: "share-1", Path: subvol.Path}, nil
				},
				ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
					return []nastyapi.NFSShare{{ID: "share-1", Path: subvol.Path}}, nil
				},
				SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
					return nil, errors.New("xattr write failed")
				},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			if resp, err := service.createNFSVolume(context.Background(), req); err == nil || resp != nil {
				t.Fatalf("property failure returned success: response=%v error=%v", resp, err)
			}
			if endpointCalls != 0 {
				t.Fatalf("property failure allowed %d endpoint operations", endpointCalls)
			}
		})
	}
}

func TestAmbiguousIdentityWriteAllowsSafeRetryBeforeEndpoint(t *testing.T) {
	req := identityTestRequest(ProtocolNFS, false)
	var subvol *nastyapi.Subvolume
	writes := 0
	endpointCreates := 0
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			if subvol == nil {
				return nil, nastyapi.ErrDatasetNotFound
			}
			return subvol, nil
		},
		CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
			subvol = &nastyapi.Subvolume{
				Filesystem: params.Filesystem,
				Name:       params.Name,
				Path:       "/fs/tank/claim",
				Created:    true,
				Properties: map[string]string{},
			}
			return subvol, nil
		},
		SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
			writes++
			for key, value := range props {
				subvol.Properties[key] = value
			}
			if writes == 1 {
				return nil, errors.New("connection lost after commit")
			}
			return subvol, nil
		},
		DeleteSubvolumeFunc: func(context.Context, string, string) error {
			t.Fatal("ambiguous committed identity must not be rolled back")
			return errors.New("unexpected rollback")
		},
		ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) { return nil, nil },
		CreateNFSShareFunc: func(_ context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
			endpointCreates++
			return &nastyapi.NFSShare{ID: "share-1", Path: params.Path}, nil
		},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	if resp, err := service.createNFSVolume(context.Background(), req); status.Code(err) != codes.Aborted || resp != nil {
		t.Fatalf("ambiguous first write response=%v code=%v error=%v", resp, status.Code(err), err)
	}
	if endpointCreates != 0 {
		t.Fatal("endpoint was created after an ambiguous property write")
	}
	resp, err := service.createNFSVolume(context.Background(), req)
	if err != nil || resp == nil {
		t.Fatalf("identical retry failed: response=%v error=%v", resp, err)
	}
	if endpointCreates != 1 || writes != 1 {
		t.Fatalf("retry operations: endpointCreates=%d propertyWrites=%d, want 1 and 1", endpointCreates, writes)
	}
}

func TestLegacyPVCIdentityRequiresCompleteProof(t *testing.T) {
	props := map[string]string{
		nastyapi.PropertyCSIVolumeName: "legacy-backend",
		nastyapi.PropertyPVCName:       "data",
		nastyapi.PropertyPVCNamespace:  "team",
	}
	if legacyPVCIdentityMatches("new-request", nil, props) {
		t.Fatal("legacy identity matched without raw request name or PVC metadata")
	}
	if legacyPVCIdentityMatches("new-request", map[string]string{CSIPVCName: "data"}, props) {
		t.Fatal("legacy identity matched partial request PVC metadata")
	}
	if !legacyPVCIdentityMatches("new-request", map[string]string{CSIPVCName: "data", CSIPVCNamespace: "team"}, props) {
		t.Fatal("complete matching PVC identity was rejected")
	}
	props[nastyapi.PropertyCSIVolumeName] = "new-request"
	if !legacyPVCIdentityMatches("new-request", nil, props) {
		t.Fatal("raw legacy CSI request name proof was rejected")
	}
}

func TestV2PVCIdentityAdoptionUpdatesRequestAndPreservesUUID(t *testing.T) {
	const volumeUUID = "12345678-1234-4123-8123-123456789abc"
	subvol := nastyapi.Subvolume{
		Filesystem: "tank",
		Name:       "legacy-backend",
		Path:       "/fs/tank/legacy-backend",
		Properties: map[string]string{
			nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
			nastyapi.PropertyCSIVolumeName: "legacy-backend",
			nastyapi.PropertyProtocol:      ProtocolNFS,
			nastyapi.PropertyPVCName:       "data",
			nastyapi.PropertyPVCNamespace:  "team",
			nastyapi.PropertyAdoptable:     VolumeContextValueTrue,
			propertyCSIRequestName:         "pvc-old-uid",
			propertyVolumeUUID:             volumeUUID,
			propertyIdentityVersion:        identityVersion2,
		},
	}
	var written map[string]string
	client := &mockAPIClient{
		FindSubvolumeByCSIVolumeNameFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			return nil, nastyapi.ErrDatasetNotFound
		},
		FindSubvolumesByPropertyFunc: func(context.Context, string, string, string) ([]nastyapi.Subvolume, error) {
			return []nastyapi.Subvolume{subvol}, nil
		},
		SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
			written = props
			return &subvol, nil
		},
		ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
			return []nastyapi.NFSShare{{ID: "share-1", Path: subvol.Path}}, nil
		},
	}
	req := identityTestRequest(ProtocolNFS, false)
	req.Name = "pvc-new-uid"
	req.Parameters[CSIPVCName] = "data"
	req.Parameters[CSIPVCNamespace] = "team"
	service := NewControllerService(client, NewNodeRegistry(), "")
	resp, adopted, err := service.checkAndAdoptVolume(context.Background(), req, req.Parameters, ProtocolNFS)
	if err != nil || !adopted || resp == nil {
		t.Fatalf("v2 PVC adoption failed: adopted=%v response=%v error=%v", adopted, resp, err)
	}
	if written[propertyCSIRequestName] != req.Name || written[propertyVolumeUUID] != volumeUUID {
		t.Fatalf("adoption identity = %#v, want request %q and UUID %q", written, req.Name, volumeUUID)
	}
}

func TestCreatedEndpointMustMatchSelectedBackendResource(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			device := "/dev/mapper/claim"
			client := &mockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return nil, nastyapi.ErrDatasetNotFound
				},
				CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{
						Filesystem: params.Filesystem, Name: params.Name, Path: "/fs/tank/claim",
						BlockDevice: &device, Created: true,
					}, nil
				},
				CreateNFSShareFunc: func(context.Context, nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					return &nastyapi.NFSShare{Path: "/fs/tank/wrong"}, nil
				},
				CreateSMBShareFunc: func(_ context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
					return &nastyapi.SMBShare{Name: "wrong", Path: params.Path}, nil
				},
				CreateISCSITargetFunc: func(_ context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
					return &nastyapi.ISCSITarget{IQN: generateIQN("wrong"), Luns: []nastyapi.ISCSILun{{BackstorePath: params.DevicePath}}}, nil
				},
				CreateNVMeOFSubsystemFunc: func(_ context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error) {
					return &nastyapi.NVMeOFSubsystem{NQN: "nqn.backend:wrong", Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: params.DevicePath}}}, nil
				},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			_, err := service.createVolumeByProtocol(context.Background(), identityTestRequest(protocol, false), protocol)
			if status.Code(err) != codes.Internal {
				t.Fatalf("malformed created endpoint code=%v, want Internal (error: %v)", status.Code(err), err)
			}
		})
	}
}

func TestVolumeCloneGetsFreshUUIDAndPreservesItOnRetry(t *testing.T) {
	const sourceUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	capacity := uint64(MinVolumeSize)
	source := &nastyapi.Subvolume{
		Filesystem:    "tank",
		Name:          "source",
		SubvolumeType: subvolumeTypeFilesystem,
		QuotaBytes:    &capacity,
		Properties: map[string]string{
			nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
			nastyapi.PropertyCSIVolumeName: "source",
			nastyapi.PropertyProtocol:      ProtocolNFS,
			propertyCSIRequestName:         "source-request",
			propertyVolumeUUID:             sourceUUID,
			propertyIdentityVersion:        identityVersion2,
		},
	}
	var destination *nastyapi.Subvolume
	cloneCalls := 0
	var shares []nastyapi.NFSShare
	client := &mockAPIClient{
		GetSubvolumeFunc: func(_ context.Context, _, name string) (*nastyapi.Subvolume, error) {
			switch name {
			case source.Name:
				return source, nil
			case "clone":
				if destination != nil {
					return destination, nil
				}
			}
			return nil, nastyapi.ErrDatasetNotFound
		},
		CloneSubvolumeFunc: func(_ context.Context, _, _, newName string) (*nastyapi.Subvolume, error) {
			cloneCalls++
			if destination == nil {
				destination = &nastyapi.Subvolume{
					Filesystem:    "tank",
					Name:          newName,
					SubvolumeType: subvolumeTypeFilesystem,
					Path:          "/fs/tank/clone",
					QuotaBytes:    &capacity,
					Properties:    copyProperties(source.Properties),
				}
			}
			response := *destination
			response.Created = cloneCalls == 1
			return &response, nil
		},
		ResizeSubvolumeFunc: func(context.Context, string, string, uint64) (*nastyapi.Subvolume, error) {
			return destination, nil
		},
		SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
			destination.Properties = copyProperties(props)
			return destination, nil
		},
		ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) { return shares, nil },
		CreateNFSShareFunc: func(_ context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
			share := nastyapi.NFSShare{ID: "share-1", Path: params.Path}
			shares = append(shares, share)
			return &share, nil
		},
	}
	req := &csi.CreateVolumeRequest{
		Name: "clone",
		Parameters: map[string]string{
			"filesystem": "tank",
			"protocol":   ProtocolNFS,
		},
		CapacityRange:      &csi.CapacityRange{RequiredBytes: MinVolumeSize},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	if _, err := service.createVolumeFromVolume(context.Background(), req, "tank/source"); err != nil {
		t.Fatalf("volume clone failed: %v", err)
	}
	cloneUUID := destination.Properties[propertyVolumeUUID]
	if cloneUUID == "" || cloneUUID == sourceUUID {
		t.Fatalf("new volume clone UUID = %q, must differ from source UUID %q", cloneUUID, sourceUUID)
	}
	if _, err := service.createVolumeFromVolume(context.Background(), req, "tank/source"); err != nil {
		t.Fatalf("volume clone retry failed: %v", err)
	}
	if retryUUID := destination.Properties[propertyVolumeUUID]; retryUUID != cloneUUID {
		t.Fatalf("volume clone retry UUID = %q, want %q", retryUUID, cloneUUID)
	}
}

func TestConfirmedIdentityFailureRollsBackNewSubvolumeAndRetrySucceeds(t *testing.T) {
	for _, protocol := range []string{ProtocolNFS, ProtocolSMB, ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			device := "/dev/mapper/claim"
			var subvol *nastyapi.Subvolume
			propertyWrites := 0
			rollbacks := 0
			endpointCreates := 0
			client := &mockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					if subvol == nil {
						return nil, nastyapi.ErrDatasetNotFound
					}
					return subvol, nil
				},
				CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
					subvol = &nastyapi.Subvolume{
						Filesystem:  params.Filesystem,
						Name:        params.Name,
						Path:        "/fs/tank/claim",
						BlockDevice: &device,
						Created:     true,
						Properties:  map[string]string{},
					}
					return subvol, nil
				},
				SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
					propertyWrites++
					if propertyWrites == 1 {
						return nil, errors.New("confirmed xattr failure")
					}
					return subvol, nil
				},
				DeleteSubvolumeFunc: func(context.Context, string, string) error {
					rollbacks++
					subvol = nil
					return nil
				},
				ListNFSSharesFunc:        func(context.Context) ([]nastyapi.NFSShare, error) { return nil, nil },
				ListSMBSharesFunc:        func(context.Context) ([]nastyapi.SMBShare, error) { return nil, nil },
				ListISCSITargetsFunc:     func(context.Context) ([]nastyapi.ISCSITarget, error) { return nil, nil },
				ListNVMeOFSubsystemsFunc: func(context.Context) ([]nastyapi.NVMeOFSubsystem, error) { return nil, nil },
				CreateNFSShareFunc: func(_ context.Context, params nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
					endpointCreates++
					return &nastyapi.NFSShare{ID: "nfs", Path: params.Path}, nil
				},
				CreateSMBShareFunc: func(_ context.Context, params nastyapi.SMBShareCreateParams) (*nastyapi.SMBShare, error) {
					endpointCreates++
					return &nastyapi.SMBShare{ID: "smb", Name: params.Name, Path: params.Path}, nil
				},
				CreateISCSITargetFunc: func(_ context.Context, params nastyapi.ISCSITargetCreateParams) (*nastyapi.ISCSITarget, error) {
					endpointCreates++
					return &nastyapi.ISCSITarget{ID: "iscsi", IQN: generateIQN(params.Name), Luns: []nastyapi.ISCSILun{{BackstorePath: params.DevicePath}}}, nil
				},
				CreateNVMeOFSubsystemFunc: func(_ context.Context, params nastyapi.NVMeOFCreateParams) (*nastyapi.NVMeOFSubsystem, error) {
					endpointCreates++
					return &nastyapi.NVMeOFSubsystem{ID: "nvme", NQN: "nqn.backend:" + params.Name, Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: params.DevicePath}}}, nil
				},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			if resp, err := service.createVolumeByProtocol(context.Background(), identityTestRequest(protocol, false), protocol); status.Code(err) != codes.Internal || resp != nil {
				t.Fatalf("confirmed failure response=%v code=%v error=%v", resp, status.Code(err), err)
			}
			if rollbacks != 1 || subvol != nil || endpointCreates != 0 {
				t.Fatalf("after failure: rollbacks=%d subvolume=%v endpointCreates=%d", rollbacks, subvol, endpointCreates)
			}
			resp, err := service.createVolumeByProtocol(context.Background(), identityTestRequest(protocol, false), protocol)
			if err != nil || resp == nil {
				t.Fatalf("retry failed: response=%v error=%v", resp, err)
			}
			if endpointCreates != 1 || rollbacks != 1 {
				t.Fatalf("after retry: endpointCreates=%d rollbacks=%d", endpointCreates, rollbacks)
			}
		})
	}
}

func TestVolumeCloneRecoversCopiedSourceIdentityAfterBackendValidation(t *testing.T) {
	for _, preexisting := range []bool{true, false} {
		name := "interrupted"
		if !preexisting {
			name = "concurrent-created-false"
		}
		t.Run(name, func(t *testing.T) {
			const sourceUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			capacity := uint64(MinVolumeSize)
			source := &nastyapi.Subvolume{
				Filesystem: "tank", Name: "source", SubvolumeType: subvolumeTypeFilesystem, QuotaBytes: &capacity,
				Properties: map[string]string{
					nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
					nastyapi.PropertyCSIVolumeName: "source",
					nastyapi.PropertyProtocol:      ProtocolNFS,
					propertyCSIRequestName:         "source-request",
					propertyVolumeUUID:             sourceUUID,
					propertyIdentityVersion:        identityVersion2,
				},
			}
			var destination *nastyapi.Subvolume
			if preexisting {
				destination = &nastyapi.Subvolume{
					Filesystem: "tank", Name: "clone", SubvolumeType: subvolumeTypeFilesystem,
					Path: "/fs/tank/clone", QuotaBytes: &capacity,
					Properties: copyProperties(source.Properties),
				}
			}
			cloneCalls := 0
			client := &mockAPIClient{
				GetSubvolumeFunc: func(_ context.Context, _, volumeName string) (*nastyapi.Subvolume, error) {
					if volumeName == source.Name {
						return source, nil
					}
					if volumeName == "clone" && destination != nil {
						return destination, nil
					}
					return nil, nastyapi.ErrDatasetNotFound
				},
				CloneSubvolumeFunc: func(context.Context, string, string, string) (*nastyapi.Subvolume, error) {
					cloneCalls++
					if destination == nil {
						destination = &nastyapi.Subvolume{
							Filesystem: "tank", Name: "clone", SubvolumeType: subvolumeTypeFilesystem,
							Path: "/fs/tank/clone", QuotaBytes: &capacity,
							Properties: copyProperties(source.Properties),
						}
					}
					response := *destination
					response.Created = false
					return &response, nil
				},
				ResizeSubvolumeFunc: func(context.Context, string, string, uint64) (*nastyapi.Subvolume, error) { return destination, nil },
				SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
					destination.Properties = copyProperties(props)
					return destination, nil
				},
				ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
					return []nastyapi.NFSShare{{ID: "share", Path: "/fs/tank/clone"}}, nil
				},
			}
			req := &csi.CreateVolumeRequest{
				Name:               "clone",
				Parameters:         map[string]string{"filesystem": "tank", "protocol": ProtocolNFS},
				CapacityRange:      &csi.CapacityRange{RequiredBytes: MinVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			if _, err := service.createVolumeFromVolume(context.Background(), req, "tank/source"); err != nil {
				t.Fatalf("clone recovery failed: %v", err)
			}
			if cloneCalls != 1 {
				t.Fatalf("clone backend calls=%d, want 1 source-validation call", cloneCalls)
			}
			if got := destination.Properties[propertyVolumeUUID]; got == "" || got == sourceUUID {
				t.Fatalf("recovered destination UUID=%q, must be fresh from source %q", got, sourceUUID)
			}
		})
	}
}

func TestNewCloneConfirmedPropertyFailureRollsBackBeforeEndpoint(t *testing.T) {
	capacity := uint64(MinVolumeSize)
	source := &nastyapi.Subvolume{
		Filesystem: "tank", Name: "source", SubvolumeType: subvolumeTypeFilesystem, QuotaBytes: &capacity,
		Properties: map[string]string{
			nastyapi.PropertyManagedBy: nastyapi.ManagedByValue,
			nastyapi.PropertyProtocol:  ProtocolNFS,
			propertyCSIRequestName:     "source-request",
			propertyVolumeUUID:         "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			propertyIdentityVersion:    identityVersion2,
		},
	}
	var destination *nastyapi.Subvolume
	rollbacks := 0
	endpointCreates := 0
	client := &mockAPIClient{
		GetSubvolumeFunc: func(_ context.Context, _, name string) (*nastyapi.Subvolume, error) {
			if name == source.Name {
				return source, nil
			}
			if destination != nil && name == destination.Name {
				return destination, nil
			}
			return nil, nastyapi.ErrDatasetNotFound
		},
		CloneSubvolumeFunc: func(context.Context, string, string, string) (*nastyapi.Subvolume, error) {
			destination = &nastyapi.Subvolume{
				Filesystem: "tank", Name: "clone", SubvolumeType: subvolumeTypeFilesystem,
				Path: "/fs/tank/clone", QuotaBytes: &capacity,
				Properties: copyProperties(source.Properties),
			}
			response := *destination
			response.Created = true
			return &response, nil
		},
		ResizeSubvolumeFunc: func(context.Context, string, string, uint64) (*nastyapi.Subvolume, error) { return destination, nil },
		SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
			return nil, errors.New("confirmed clone xattr failure")
		},
		DeleteSubvolumeFunc: func(context.Context, string, string) error {
			rollbacks++
			destination = nil
			return nil
		},
		CreateNFSShareFunc: func(context.Context, nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
			endpointCreates++
			return nil, errors.New("unexpected endpoint creation")
		},
	}
	req := &csi.CreateVolumeRequest{
		Name:               "clone",
		Parameters:         map[string]string{"filesystem": "tank", "protocol": ProtocolNFS},
		CapacityRange:      &csi.CapacityRange{RequiredBytes: MinVolumeSize},
		VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	if resp, err := service.createVolumeFromVolume(context.Background(), req, "tank/source"); status.Code(err) != codes.Internal || resp != nil {
		t.Fatalf("clone property failure response=%v code=%v error=%v", resp, status.Code(err), err)
	}
	if rollbacks != 1 || destination != nil || endpointCreates != 0 {
		t.Fatalf("clone rollback state: rollbacks=%d destination=%v endpointCreates=%d", rollbacks, destination, endpointCreates)
	}
}

func TestAdoptionValidationPrecedesIdentityMutation(t *testing.T) {
	const oldRequest = "pvc-old"
	baseProps := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: "backend",
		nastyapi.PropertyProtocol:      ProtocolNFS,
		propertyCSIRequestName:         oldRequest,
		propertyVolumeUUID:             "12345678-1234-4123-8123-123456789abc",
		propertyIdentityVersion:        identityVersion2,
	}
	t.Run("filesystem list failure", func(t *testing.T) {
		subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "backend", Path: "/fs/tank/backend", Properties: copyProperties(baseProps)}
		client := &mockAPIClient{
			ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) { return nil, errors.New("list failed") },
			SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
				t.Fatal("identity must not change before endpoint listing succeeds")
				return nil, errors.New("unexpected property write")
			},
		}
		req := identityTestRequest(ProtocolNFS, true)
		req.Name = "pvc-new"
		service := NewControllerService(client, NewNodeRegistry(), "")
		if _, err := service.adoptNFSVolume(context.Background(), req, subvol, req.Parameters); err == nil {
			t.Fatal("adoption succeeded despite endpoint list failure")
		}
		if subvol.Properties[propertyCSIRequestName] != oldRequest {
			t.Fatal("filesystem adoption changed identity before validation")
		}
	})

	t.Run("block wrong device", func(t *testing.T) {
		device := "/dev/mapper/backend"
		props := copyProperties(baseProps)
		props[nastyapi.PropertyProtocol] = ProtocolISCSI
		subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "backend", BlockDevice: &device, Properties: props}
		client := &mockAPIClient{
			ListISCSITargetsFunc: func(context.Context) ([]nastyapi.ISCSITarget, error) {
				return []nastyapi.ISCSITarget{{IQN: generateIQN("backend"), Luns: []nastyapi.ISCSILun{{BackstorePath: "/dev/wrong"}}}}, nil
			},
			SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
				t.Fatal("identity must not change before block endpoint validation")
				return nil, errors.New("unexpected property write")
			},
		}
		req := identityTestRequest(ProtocolISCSI, true)
		req.Name = "pvc-new"
		service := NewControllerService(client, NewNodeRegistry(), "")
		if _, err := service.adoptISCSIVolume(context.Background(), req, subvol, req.Parameters); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("wrong-device adoption code=%v, want FailedPrecondition", status.Code(err))
		}
		if subvol.Properties[propertyCSIRequestName] != oldRequest {
			t.Fatal("block adoption changed identity before validation")
		}
	})
}

func TestSelectNVMeSubsystemDisambiguatesSuffixCandidates(t *testing.T) {
	device := "/dev/mapper/claim"
	wrong := nastyapi.NVMeOFSubsystem{
		ID: "tenant", NQN: "nqn.backend:tenant:claim",
		Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: "/dev/tenant"}},
	}
	right := nastyapi.NVMeOFSubsystem{
		ID: "claim", NQN: "nqn.backend:claim",
		Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: device}},
	}
	for _, subsystems := range [][]nastyapi.NVMeOFSubsystem{{wrong, right}, {right, wrong}} {
		selected, err := selectNVMeSubsystem(subsystems, "nqn.requested:claim", "claim", device)
		if err != nil || selected == nil || selected.ID != right.ID {
			t.Fatalf("suffix disambiguation selected=%v error=%v", selected, err)
		}
	}
	tenantDevice := "/dev/mapper/tenant-claim"
	tenant := nastyapi.NVMeOFSubsystem{
		ID: "tenant-full", NQN: "nqn.backend:tenant:claim",
		Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: tenantDevice}},
	}
	selected, err := selectNVMeSubsystem([]nastyapi.NVMeOFSubsystem{right, tenant}, "nqn.requested:tenant:claim", "tenant:claim", tenantDevice)
	if err != nil || selected == nil || selected.ID != tenant.ID {
		t.Fatalf("colon backend name selection=%v error=%v", selected, err)
	}
	ambiguous := []nastyapi.NVMeOFSubsystem{right, {ID: "duplicate", NQN: "nqn.other:claim", Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: device}}}}
	if _, err := selectNVMeSubsystem(ambiguous, "nqn.requested:claim", "claim", device); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("ambiguous subsystem code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestWrongBlockEndpointAssociationIsRejected(t *testing.T) {
	device := "/dev/mapper/claim"
	props := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: "claim",
		nastyapi.PropertyProtocol:      ProtocolISCSI,
		propertyCSIRequestName:         "claim",
		propertyVolumeUUID:             "12345678-1234-4123-8123-123456789abc",
		propertyIdentityVersion:        identityVersion2,
	}
	for _, protocol := range []string{ProtocolISCSI, ProtocolNVMeOF} {
		t.Run(protocol, func(t *testing.T) {
			protocolProps := make(map[string]string, len(props))
			for key, value := range props {
				protocolProps[key] = value
			}
			protocolProps[nastyapi.PropertyProtocol] = protocol
			client := &mockAPIClient{
				GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
					return &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", BlockDevice: &device, Properties: protocolProps}, nil
				},
				ListISCSITargetsFunc: func(context.Context) ([]nastyapi.ISCSITarget, error) {
					return []nastyapi.ISCSITarget{{IQN: generateIQN("claim"), Luns: []nastyapi.ISCSILun{{BackstorePath: "/dev/wrong"}}}}, nil
				},
				ListNVMeOFSubsystemsFunc: func(context.Context) ([]nastyapi.NVMeOFSubsystem, error) {
					return []nastyapi.NVMeOFSubsystem{{NQN: generateNQN(defaultNQNPrefix, "claim"), Namespaces: []nastyapi.NVMeOFNamespace{{DevicePath: "/dev/wrong"}}}}, nil
				},
			}
			service := NewControllerService(client, NewNodeRegistry(), "")
			_, err := service.createVolumeByProtocol(context.Background(), identityTestRequest(protocol, false), protocol)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("wrong-device association code = %v, want FailedPrecondition (error: %v)", status.Code(err), err)
			}
		})
	}
}

func TestUnknownIdentityWriteDoesNotRollback(t *testing.T) {
	req := identityTestRequest(ProtocolNFS, false)
	subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", Path: "/fs/tank/claim", Created: true}
	reads := 0
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			reads++
			if reads == 1 {
				return nil, nastyapi.ErrDatasetNotFound
			}
			return nil, errors.New("verification timed out")
		},
		CreateSubvolumeFunc: func(context.Context, nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
			return subvol, nil
		},
		SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
			return nil, errors.New("write timed out")
		},
		DeleteSubvolumeFunc: func(context.Context, string, string) error {
			t.Fatal("unknown identity state must not be rolled back")
			return nil
		},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	if _, err := service.createNFSVolume(context.Background(), req); status.Code(err) != codes.Aborted {
		t.Fatalf("unknown identity write code=%v, want Aborted (error: %v)", status.Code(err), err)
	}
}

func TestRollbackUsesDetachedContext(t *testing.T) {
	subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", Properties: map[string]string{}}
	deleted := false
	client := &mockAPIClient{
		GetSubvolumeFunc: func(ctx context.Context, _, _ string) (*nastyapi.Subvolume, error) {
			if ctx.Err() != nil {
				t.Fatalf("rollback read inherited canceled context: %v", ctx.Err())
			}
			return subvol, nil
		},
		DeleteSubvolumeFunc: func(ctx context.Context, _, _ string) error {
			if ctx.Err() != nil {
				t.Fatalf("rollback delete inherited canceled context: %v", ctx.Err())
			}
			deleted = true
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewControllerService(client, NewNodeRegistry(), "")
	operationErr := status.Error(codes.Internal, "confirmed failure")
	if err := service.rollbackCreatedSubvolume(ctx, subvol, operationErr); status.Code(err) != codes.Internal {
		t.Fatalf("rollback result code=%v, want Internal (error: %v)", status.Code(err), err)
	}
	if !deleted {
		t.Fatal("detached rollback did not delete the uncommitted subvolume")
	}
}

func TestRollbackPreservesConcurrentlyCommittedIdentity(t *testing.T) {
	subvol := &nastyapi.Subvolume{Filesystem: "tank", Name: "claim", Properties: map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: "claim",
		nastyapi.PropertyProtocol:      ProtocolNFS,
		propertyIdentityVersion:        identityVersion2,
		propertyCSIRequestName:         "claim",
		propertyVolumeUUID:             "12345678-1234-4123-8123-123456789abc",
	}}
	client := &mockAPIClient{
		GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) { return subvol, nil },
		DeleteSubvolumeFunc: func(context.Context, string, string) error {
			t.Fatal("rollback deleted a concurrently committed identity")
			return nil
		},
	}
	service := NewControllerService(client, NewNodeRegistry(), "")
	if err := service.rollbackCreatedSubvolume(context.Background(), subvol, status.Error(codes.Internal, "confirmed failure")); status.Code(err) != codes.Aborted {
		t.Fatalf("concurrent identity result code=%v, want Aborted (error: %v)", status.Code(err), err)
	}
}

func TestSchemaV1CopiedCloneIdentityGetsFreshV2Identity(t *testing.T) {
	source := map[string]string{
		nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
		nastyapi.PropertyCSIVolumeName: "source-request",
		nastyapi.PropertyProtocol:      ProtocolNFS,
		nastyapi.PropertyCapacityBytes: "1073741824",
		nastyapi.PropertyCreatedAt:     "2026-08-21T00:00:00Z",
	}
	destination := &nastyapi.Subvolume{
		Filesystem: "tank",
		Name:       "clone",
		Properties: copyProperties(source),
	}
	req := identityTestRequest(ProtocolNFS, false)
	req.Name = "clone"
	freshIdentity, err := newVolumeIdentityDecision(req, "clone", ProtocolNFS)
	if err != nil {
		t.Fatalf("failed to prepare fresh clone identity: %v", err)
	}
	decision, err := classifyCloneIdentity(req, ProtocolNFS, "clone", destination, source, false, false, freshIdentity)
	if err != nil {
		t.Fatalf("schema-v1 interrupted clone was rejected after provenance validation: %v", err)
	}
	if !decision.persist || decision.properties[propertyIdentityVersion] != identityVersion2 ||
		decision.properties[propertyCSIRequestName] != "clone" ||
		!uuidV4Regex.MatchString(decision.properties[propertyVolumeUUID]) {

		t.Fatalf("schema-v1 clone decision is incomplete: %#v", decision)
	}
}

func TestSchemaV1InterruptedCloneRecoveryAfterBackendValidation(t *testing.T) {
	for _, cloneKind := range []string{"volume", "snapshot"} {
		t.Run(cloneKind, func(t *testing.T) {
			capacity := uint64(MinVolumeSize)
			source := &nastyapi.Subvolume{
				Filesystem: "tank", Name: "source", SubvolumeType: subvolumeTypeFilesystem, QuotaBytes: &capacity,
				Properties: map[string]string{
					nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
					nastyapi.PropertyCSIVolumeName: "source",
					nastyapi.PropertyProtocol:      ProtocolNFS,
					nastyapi.PropertyCapacityBytes: "1073741824",
					nastyapi.PropertyCreatedAt:     "2026-08-21T00:00:00Z",
				},
			}
			destination := &nastyapi.Subvolume{
				Filesystem: "tank", Name: "clone", SubvolumeType: subvolumeTypeFilesystem,
				Path: "/fs/tank/clone", QuotaBytes: &capacity, Properties: copyProperties(source.Properties),
			}
			cloneCalls := 0
			client := &mockAPIClient{
				GetSubvolumeFunc: func(_ context.Context, _, name string) (*nastyapi.Subvolume, error) {
					switch name {
					case source.Name:
						return source, nil
					case destination.Name:
						return destination, nil
					default:
						return nil, nastyapi.ErrDatasetNotFound
					}
				},
				CloneSubvolumeFunc: func(context.Context, string, string, string) (*nastyapi.Subvolume, error) {
					cloneCalls++
					response := *destination
					response.Created = false
					return &response, nil
				},
				CloneSnapshotFunc: func(context.Context, nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
					cloneCalls++
					response := *destination
					response.Created = false
					return &response, nil
				},
				ResizeSubvolumeFunc: func(context.Context, string, string, uint64) (*nastyapi.Subvolume, error) {
					return destination, nil
				},
				SetSubvolumePropertiesFunc: func(_ context.Context, _, _ string, props map[string]string) (*nastyapi.Subvolume, error) {
					destination.Properties = copyProperties(props)
					return destination, nil
				},
				ListNFSSharesFunc: func(context.Context) ([]nastyapi.NFSShare, error) {
					return []nastyapi.NFSShare{{ID: "share", Path: destination.Path}}, nil
				},
			}
			req := identityTestRequest(ProtocolNFS, false)
			req.Name = "clone"
			service := NewControllerService(client, NewNodeRegistry(), "")
			var err error
			if cloneKind == "volume" {
				_, err = service.createVolumeFromVolume(context.Background(), req, "tank/source")
			} else {
				_, err = service.createVolumeFromSnapshot(context.Background(), req, "nfs:tank/source@snapshot")
			}
			if err != nil {
				t.Fatalf("schema-v1 interrupted %s clone recovery failed: %v", cloneKind, err)
			}
			if cloneCalls != 1 {
				t.Fatalf("backend source-validation calls=%d, want 1", cloneCalls)
			}
			if destination.Properties[propertyIdentityVersion] != identityVersion2 ||
				destination.Properties[propertyCSIRequestName] != req.Name ||
				!uuidV4Regex.MatchString(destination.Properties[propertyVolumeUUID]) {

				t.Fatalf("recovered schema-v1 destination identity is incomplete: %#v", destination.Properties)
			}
		})
	}
}

func TestNewBlockValidationFailureRollsBack(t *testing.T) {
	for _, protocol := range []string{ProtocolISCSI, ProtocolNVMeOF} {
		for _, failure := range []string{"missing-device", "invalid-filesystem"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				var subvol *nastyapi.Subvolume
				device := "/dev/mapper/claim"
				xfs := fsTypeXFS
				uuid := "filesystem-uuid"
				client := &mockAPIClient{
					GetSubvolumeFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
						if subvol == nil {
							return nil, nastyapi.ErrDatasetNotFound
						}
						return subvol, nil
					},
					CreateSubvolumeFunc: func(_ context.Context, params nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
						subvol = &nastyapi.Subvolume{Filesystem: params.Filesystem, Name: params.Name, Created: true, Properties: map[string]string{}}
						if failure == "invalid-filesystem" {
							subvol.BlockDevice = &device
							subvol.BlockFilesystem = &xfs
							subvol.BlockFilesystemUUID = &uuid
						}
						return subvol, nil
					},
					DeleteSubvolumeFunc: func(context.Context, string, string) error {
						subvol = nil
						return nil
					},
				}
				req := identityTestRequest(protocol, false)
				if failure == "invalid-filesystem" {
					req.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fsTypeExt4}}
				}
				service := NewControllerService(client, NewNodeRegistry(), "")
				if _, err := service.createVolumeByProtocol(context.Background(), req, protocol); err == nil {
					t.Fatal("block validation failure returned success")
				}
				if subvol != nil {
					t.Fatal("new invalid block subvolume was not rolled back")
				}
			})
		}
	}
}

func TestNewClonesRollbackOnCapacityAndIdentityReadFailure(t *testing.T) {
	for _, cloneKind := range []string{"volume", "snapshot"} {
		for _, failure := range []string{"capacity", "identity-read"} {
			t.Run(cloneKind+"/"+failure, func(t *testing.T) {
				sourceCapacity := uint64(MinVolumeSize)
				destinationCapacity := sourceCapacity
				if failure == "capacity" {
					destinationCapacity *= 2
				}
				source := &nastyapi.Subvolume{Filesystem: "tank", Name: "source", SubvolumeType: subvolumeTypeFilesystem, QuotaBytes: &sourceCapacity, Properties: map[string]string{
					nastyapi.PropertyManagedBy:     nastyapi.ManagedByValue,
					nastyapi.PropertyCSIVolumeName: "source",
					nastyapi.PropertyProtocol:      ProtocolNFS,
					propertyIdentityVersion:        identityVersion2,
					propertyCSIRequestName:         "source",
					propertyVolumeUUID:             "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				}}
				var destination *nastyapi.Subvolume
				postCloneReads := 0
				rollbacks := 0
				client := &mockAPIClient{
					GetSubvolumeFunc: func(_ context.Context, _, name string) (*nastyapi.Subvolume, error) {
						if name == source.Name {
							return source, nil
						}
						if destination == nil {
							return nil, nastyapi.ErrDatasetNotFound
						}
						postCloneReads++
						if failure == "identity-read" && postCloneReads == 2 {
							return nil, errors.New("post-clone read failed")
						}
						return destination, nil
					},
					CloneSubvolumeFunc: func(_ context.Context, _, _, newName string) (*nastyapi.Subvolume, error) {
						destination = &nastyapi.Subvolume{Filesystem: "tank", Name: newName, SubvolumeType: subvolumeTypeFilesystem, Created: true, QuotaBytes: &destinationCapacity, Properties: map[string]string{}}
						return destination, nil
					},
					CloneSnapshotFunc: func(_ context.Context, params nastyapi.SnapshotCloneParams) (*nastyapi.Subvolume, error) {
						destination = &nastyapi.Subvolume{Filesystem: "tank", Name: params.NewName, SubvolumeType: subvolumeTypeFilesystem, Created: true, QuotaBytes: &destinationCapacity, Properties: map[string]string{}}
						return destination, nil
					},
					ResizeSubvolumeFunc: func(context.Context, string, string, uint64) (*nastyapi.Subvolume, error) { return destination, nil },
					DeleteSubvolumeFunc: func(context.Context, string, string) error {
						rollbacks++
						destination = nil
						return nil
					},
					CreateNFSShareFunc: func(context.Context, nastyapi.NFSShareCreateParams) (*nastyapi.NFSShare, error) {
						t.Fatal("clone validation failure reached endpoint creation")
						return nil, errors.New("unexpected endpoint creation")
					},
				}
				req := identityTestRequest(ProtocolNFS, false)
				req.Name = "clone"
				req.CapacityRange.LimitBytes = MinVolumeSize
				service := NewControllerService(client, NewNodeRegistry(), "")
				var err error
				if cloneKind == "volume" {
					_, err = service.createVolumeFromVolume(context.Background(), req, "tank/source")
				} else {
					_, err = service.createVolumeFromSnapshot(context.Background(), req, "nfs:tank/source@snapshot")
				}
				if err == nil || rollbacks != 1 || destination != nil {
					t.Fatalf("clone failure cleanup: error=%v rollbacks=%d destination=%v", err, rollbacks, destination)
				}
			})
		}
	}
}

func TestAmbiguousPVCAdoptionFailsClosed(t *testing.T) {
	props := map[string]string{
		nastyapi.PropertyPVCName:      "data",
		nastyapi.PropertyPVCNamespace: "team",
	}
	client := &mockAPIClient{
		FindSubvolumeByCSIVolumeNameFunc: func(context.Context, string, string) (*nastyapi.Subvolume, error) {
			return nil, nastyapi.ErrDatasetNotFound
		},
		FindSubvolumesByPropertyFunc: func(context.Context, string, string, string) ([]nastyapi.Subvolume, error) {
			return []nastyapi.Subvolume{
				{Filesystem: "tank", Name: "first", Properties: copyProperties(props)},
				{Filesystem: "tank", Name: "second", Properties: copyProperties(props)},
			}, nil
		},
		SetSubvolumePropertiesFunc: func(context.Context, string, string, map[string]string) (*nastyapi.Subvolume, error) {
			t.Fatal("ambiguous PVC adoption mutated a candidate")
			return nil, errors.New("unexpected mutation")
		},
		CreateSubvolumeFunc: func(context.Context, nastyapi.SubvolumeCreateParams) (*nastyapi.Subvolume, error) {
			t.Fatal("ambiguous PVC adoption fell through to creation")
			return nil, errors.New("unexpected creation")
		},
	}
	req := identityTestRequest(ProtocolNFS, false)
	req.Parameters[CSIPVCName] = "data"
	req.Parameters[CSIPVCNamespace] = "team"
	service := NewControllerService(client, NewNodeRegistry(), "")
	if _, handled, err := service.checkAndAdoptVolume(context.Background(), req, req.Parameters, ProtocolNFS); !handled || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("ambiguous PVC adoption handled=%v code=%v error=%v", handled, status.Code(err), err)
	}
}
