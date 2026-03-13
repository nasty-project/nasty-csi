# CSI Sanity Tests

This directory contains CSI specification compliance tests using the [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) framework.

## Overview

CSI sanity tests validate that the driver correctly implements the CSI specification. These tests are protocol-agnostic and focus on the CSI API rather than storage implementation details.

## Current Status

**⚠️ Work in Progress**: The sanity test infrastructure is being implemented in phases.

### Phase 1: Foundation (Current)
- ✅ Added `csi-test/v5` dependency
- ✅ Created mock TrueNAS client for testing
- ✅ Set up test directory structure
- 🔄 **In Progress**: Refactoring driver for dependency injection

### Phase 2: Implementation (Next)
- ⏳ Modify driver to accept client interface (not concrete type)
- ⏳ Implement full sanity test suite
- ⏳ Add CI/CD integration

### Phase 3: Expansion (Future)
- ⏳ Add Node service tests (requires mount mocking)
- ⏳ Test both NFS and NVMe-oF volume capabilities
- ⏳ Expand snapshot test coverage

## Architecture

### Mock Client (`mock_client.go`)
A lightweight mock implementation of the TrueNAS API client that:
- Simulates dataset creation/deletion
- Mocks NFS share management
- Simulates NVMe-oF target operations
- Tracks API calls for debugging

### Test Suite (`sanity_test.go`)
CSI specification compliance tests that validate:
- **Identity Service**: Plugin info, capabilities, health probes
- **Controller Service**: Volume lifecycle, snapshots, expansion
- **Node Service**: (Future) Volume staging and publishing

## Why Refactoring is Needed

The current driver creates a `nastyapi.Client` directly in `NewDriver()`:

```go
// Current implementation
apiClient, err := nastyapi.NewClient(cfg.APIURL, cfg.APIKey)
```

For testability, we need:

```go
// Refactored implementation with interface
type TNSClient interface {
    CreateDataset(...) (string, error)
    DeleteDataset(...) error
    CreateNFSShare(...) (int, error)
    // ... other methods
}

func NewDriverWithClient(cfg Config, client TNSClient) (*Driver, error)
```

This allows injecting `MockClient` during tests while using `nastyapi.Client` in production.

## Running Tests (After Refactoring)

### Local Testing
```bash
# Run all sanity tests
make test-sanity

# Run specific test categories
go test -v ./tests/sanity -run TestSanityIdentity
go test -v ./tests/sanity -run TestSanityController
```

### CI/CD Integration
Tests will run automatically in GitHub Actions:
```bash
./tests/sanity/test-sanity.sh
```

## Test Configuration

### Volume Parameters
Tests use these default parameters:
- **Protocol**: NFS (simpler to mock than NVMe-oF)
- **Pool**: `tank` (standard mock pool name)
- **Size**: 1GB (minimum test volume size)

### Paths
- **Staging Path**: `/tmp/csi-sanity-staging`
- **Target Path**: `/tmp/csi-sanity-target`
- **Socket**: `unix:///tmp/csi-sanity.sock`

## What Tests Validate

### Identity Service ✅
- ✅ GetPluginInfo returns correct name and version
- ✅ GetPluginCapabilities advertises controller service
- ✅ Probe returns ready status

### Controller Service 🔄
- 🔄 CreateVolume succeeds with valid parameters
- 🔄 DeleteVolume cleans up resources
- 🔄 ControllerGetCapabilities returns expected capabilities
- 🔄 ValidateVolumeCapabilities accepts valid capabilities
- 🔄 ListVolumes returns created volumes
- 🔄 GetCapacity reports available storage
- 🔄 ControllerExpandVolume increases volume size
- 🔄 CreateSnapshot/DeleteSnapshot manage snapshots
- 🔄 ListSnapshots returns created snapshots

### Node Service ⏳
- ⏳ NodeStageVolume prepares volume for use
- ⏳ NodePublishVolume makes volume available to pod
- ⏳ NodeUnpublishVolume removes volume from pod
- ⏳ NodeUnstageVolume cleans up staged volume
- ⏳ NodeGetCapabilities returns expected capabilities

## Complementary Testing

Sanity tests **complement** but don't **replace** other test types:

| Test Type | Purpose | Real TrueNAS | Real Kubernetes |
|-----------|---------|--------------|-----------------|
| **Unit Tests** | Component logic | ❌ | ❌ |
| **Sanity Tests** | CSI spec compliance | ❌ | ❌ |
| **Integration Tests** | End-to-end workflows | ✅ | ✅ |

All three are necessary for comprehensive validation.

## Debugging

### View Mock Client Calls
The mock client logs all API calls:
```go
mockClient := NewMockClient()
// ... perform operations ...
log := mockClient.GetCallLog()
fmt.Printf("API calls: %v\n", log)
```

### Verbose Test Output
```bash
go test -v -count=1 ./tests/sanity
```

### Sanity Test Logs
The csi-test framework provides detailed logs for failures:
```
--- FAIL: TestSanity/CreateVolume (0.05s)
    sanity.go:42: CreateVolume failed: rpc error: code = InvalidArgument desc = missing required parameter: pool
```

## References

- [CSI Specification](https://github.com/container-storage-interface/spec)
- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test)
- [CSI Sanity Documentation](https://github.com/kubernetes-csi/csi-test/tree/master/pkg/sanity)

## Next Steps

1. **Refactor driver** to use client interface
2. **Enable sanity tests** with mock client
3. **Add to CI/CD** pipeline
4. **Expand coverage** to Node service
5. **Document findings** and fix any spec violations
