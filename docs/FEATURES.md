# NASty CSI Driver - Feature Support Documentation

**⚠️ EARLY DEVELOPMENT - NOT PRODUCTION READY**

This document provides a comprehensive overview of currently implemented and tested features in the NASty CSI Driver.

## Overview

The NASty CSI Driver is a Kubernetes Container Storage Interface (CSI) driver that enables dynamic provisioning and management of persistent storage volumes on NASty systems. This driver is in active development with core features implemented and undergoing testing.

## Supported Storage Protocols

### NFS (Network File System)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteMany (RWX), ReadWriteOnce (RWO), ReadWriteOncePod (RWOP)
- **Use Case**: Shared filesystem storage, multi-pod access
- **Mount Protocol**: NFSv4.2 with nolock option
- **NASty Requirements**: 
  - NASty Scale 25.10+
  - NFS service enabled
  - Accessible NFS ports (111, 2049)

### NVMe-oF (NVMe over Fabrics - TCP)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteOnce (RWO), ReadWriteOncePod (RWOP)
- **Use Case**: High-performance block storage, low-latency workloads
- **Transport**: TCP (nvme-tcp)
- **NASty Requirements**:
  - NASty Scale 25.10+ (NVMe-oF feature introduced in this version)
  - Static IP address configured (DHCP not supported)
  - Pre-configured NVMe-oF port with TCP transport (default: 4420)
- **Architecture**: Dedicated subsystem model (1 subsystem per volume)

### iSCSI (Internet Small Computer Systems Interface)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteOnce (RWO), ReadWriteOncePod (RWOP)
- **Use Case**: Traditional block storage, broad compatibility
- **Transport**: TCP (default port: 3260)
- **NASty Requirements**:
  - NASty Scale 25.10+
  - iSCSI service enabled
  - Pre-configured iSCSI portal
- **Architecture**: Dedicated target model (1 target per volume with 1 extent)
- **Node Requirements**: `open-iscsi` package installed on Kubernetes nodes

### SMB/CIFS (Server Message Block)
- **Status**: ✅ Functional, testing in progress
- **Access Modes**: ReadWriteMany (RWX), ReadWriteOnce (RWO), ReadWriteOncePod (RWOP)
- **Use Case**: Authenticated file sharing, Windows-compatible storage
- **Mount Protocol**: CIFS (SMB 3.0 default)
- **NASty Requirements**:
  - NASty Scale 25.10+
  - SMB service enabled
  - SMB user account configured
- **Node Requirements**: `cifs-utils` package installed on Kubernetes nodes
- **Authentication**: Username/password via Kubernetes Secret (nodeStageSecretRef)

### Protocol Selection Guide

**File Storage (NFS vs SMB)**:
- **NFS**: Best for Linux-native workloads, simpler setup, no authentication required
- **SMB**: Best when you need user-level authentication or Windows compatibility

**Block Storage (NVMe-oF vs iSCSI)**:
- **NVMe-oF**: Higher performance, lower latency, better for modern NVMe SSDs
- **iSCSI**: Broader compatibility, works with any storage, well-established protocol

## Core CSI Features

### Volume Lifecycle Management

#### Dynamic Provisioning
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Automatic creation of storage volumes when PVCs are created
- **Implementation**:
  - NFS: Creates subvolume and NFS share automatically
  - NVMe-oF: Creates block subvolume (sparse image + loop device), dedicated subsystem, and namespace
  - iSCSI: Creates block subvolume (sparse image + loop device), dedicated target, extent, and target-extent mapping
  - SMB: Creates subvolume and SMB share automatically
- **Parameters**:
  - `protocol`: nfs, nvmeof, iscsi, or smb
  - `pool`: Pool name
  - `server`: NASty IP/hostname
  - `port`: (NVMe-oF/iSCSI) Target port number

#### Volume Deletion
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Automatic cleanup when PVCs with reclaimPolicy: Delete are removed
- **Implementation**:
  - NFS: Removes NFS share and deletes subvolume
  - NVMe-oF: Removes namespace, subsystem, and deletes block subvolume
  - iSCSI: Removes target-extent, extent, target, and deletes block subvolume
  - SMB: Removes SMB share and deletes subvolume
  - Idempotent operations (safe to retry)
  - Supports `deleteStrategy` parameter for volume retention (see below)

#### Delete Strategy (Volume Retention)
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Control whether volumes are actually deleted or retained when a PVC is deleted
- **Parameter**: `deleteStrategy` in StorageClass parameters
- **Values**:
  - `delete` (default): Volume is deleted when PVC is deleted
  - `retain`: Volume is kept on NASty when PVC is deleted (useful for data protection)
- **Use Case**: Protect important data from accidental deletion while still using `reclaimPolicy: Delete`

**Example StorageClass with Delete Strategy:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-retained
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  deleteStrategy: "retain"  # Volumes kept on NASty when PVC deleted
allowVolumeExpansion: true
reclaimPolicy: Delete
```

#### Volume Attachment/Detachment
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Attach volumes to nodes and detach when no longer needed
- **Implementation**:
  - NFS: Handled by NFSv4 protocol
  - NVMe-oF: Uses nvme-cli for discovery, connect, and disconnect operations
  - iSCSI: Uses open-iscsi for target discovery, login, and logout operations
  - SMB: CIFS mount with credentials file

#### Volume Mounting/Unmounting
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Mount volumes into pod containers at specified paths
- **Implementation**:
  - NFS: Standard NFSv4.2 mount with optimized options
  - NVMe-oF: Block device formatting (ext4/xfs) and filesystem mount
  - iSCSI: Block device formatting (ext4/xfs) and filesystem mount
  - SMB: CIFS mount with configurable SMB version and options
  - Proper cleanup on unmount

### Configurable Mount Options
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Customize mount options via StorageClass `mountOptions` field
- **Behavior**: User-specified options are merged with sensible defaults, with user options taking precedence for conflicting keys

**Default Mount Options:**
| Protocol | Platform | Defaults |
|----------|----------|----------|
| NFS | Linux | `vers=4.2`, `nolock` |
| NFS | macOS | `vers=4`, `nolock` |
| NVMe-oF | Linux | `noatime` |
| iSCSI | Linux | `noatime`, `_netdev` |
| SMB | Linux | `vers=3.0`, `file_mode=0777`, `dir_mode=0777` |

**Example StorageClass with Custom Mount Options:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-custom
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
mountOptions:
  - hard
  - nointr
  - rsize=1048576
  - wsize=1048576
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**NVMe-oF Mount Options Example:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nvmeof-custom
provisioner: nasty.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: nasty.local
  subsystemNQN: nqn.2025-01.com.nasty:csi
mountOptions:
  - discard
  - data=ordered
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### Volume Expansion
- **Status**: ✅ Fully implemented and functional
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Dynamically resize volumes without downtime
- **Requirements**: StorageClass must have `allowVolumeExpansion: true` (enabled by default in Helm chart)
- **Limitations**:
  - Only expansion supported (shrinking not possible)
  - Volume must not be in use during expansion for some operations
- **Implementation**:
  - NFS: Expands subvolume quota
  - NVMe-oF: Expands block subvolume size and resizes filesystem
  - iSCSI: Expands block subvolume size and resizes filesystem
  - SMB: Expands subvolume quota

**Example:**
```bash
kubectl patch pvc my-pvc -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'
```

### Volume Snapshots
- **Status**: ✅ Implemented, testing in progress
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Create point-in-time copies of volumes using bcachefs snapshots
- **Features**:
  - Near-instant snapshot creation
  - Space-efficient (copy-on-write)
  - Snapshots are independent first-class subvolumes -- they survive parent deletion
  - Snapshot deletion with proper cleanup
  - List snapshots
- **Requirements**:
  - Kubernetes Snapshot CRDs (v1 API)
  - External snapshot controller
  - CSI snapshotter sidecar (included in Helm chart)

**Key Operations:**
- Create snapshot: bcachefs snapshot created instantly
- Delete snapshot: Snapshot removed
- Idempotent operations

### Volume Cloning (Restore from Snapshot)
- **Status**: ✅ Implemented, testing in progress
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Create new volumes from existing snapshots
- **Features**:
  - Instant clone creation via `bcachefs subvolume snapshot` (writable COW clone, O(1))
  - Space-efficient (shares blocks with source until modified)
  - Full read/write access to cloned volume
  - Clones are always independent -- no promote or detach needed
- **Limitations**:
  - Cannot clone across protocols (NFS snapshot -> NFS volume only)
  - Must restore to same or larger size
  - Same pool required

**Example:**
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: restored-pvc
spec:
  storageClassName: nasty-nfs
  dataSource:
    name: my-snapshot
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
```

### Volume Health Monitoring
- **Status**: ✅ Implemented
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Description**: Report volume health status to Kubernetes via CSI `ControllerGetVolume` capability
- **CSI Capability**: `GET_VOLUME` - enables Kubernetes to query volume health
- **Features**:
  - Reports `VolumeCondition` with `Abnormal` flag and descriptive `Message`
  - Health checks performed on-demand when Kubernetes queries volume status
  - Protocol-specific validation of underlying storage resources

**Health Checks Performed:**

| Protocol | Check | Abnormal If |
|----------|-------|-------------|
| NFS | Subvolume exists | Subvolume not found or inaccessible |
| NFS | NFS share enabled | Share disabled or missing |
| NVMe-oF | Block subvolume exists | Block subvolume not found |
| NVMe-oF | Subsystem exists | Subsystem missing |
| NVMe-oF | Namespace exists | Namespace not found in subsystem |
| iSCSI | Block subvolume exists | Block subvolume not found |
| iSCSI | Target exists | Target missing |
| iSCSI | Extent exists | Extent not found |
| SMB | Subvolume exists | Subvolume not found or inaccessible |
| SMB | SMB share enabled | Share disabled or missing |

**Return Values:**
- `Abnormal: false` - Volume is healthy, all checks passed
- `Abnormal: true` - Volume has issues, `Message` contains details

**Use Cases:**
- Kubernetes can detect storage issues before pods fail
- Operators can monitor volume health via CSI events
- Automated alerting on storage problems

**Note:** This is a controller-side capability. Kubernetes periodically queries volume health for volumes with `GET_VOLUME` capability enabled.

### Raw Block Volumes with RWX (KubeVirt Live Migration)
- **Status**: ✅ Implemented (since v0.15.2)
- **Protocols**: NVMe-oF, iSCSI
- **Description**: Block storage volumes support `ReadWriteMany` access mode with `volumeMode: Block`, enabling KubeVirt live migration

KubeVirt requires RWX block volumes to live migrate VMs between worker nodes. Both NVMe-oF and iSCSI support this mode — the CSI driver exposes the raw block device without mounting a filesystem, and KubeVirt's QEMU driver handles concurrent access.

**PVC for KubeVirt:**
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: vm-disk
spec:
  accessModes:
    - ReadWriteMany
  volumeMode: Block
  resources:
    requests:
      storage: 20Gi
  storageClassName: nasty-csi-nvmeof  # or nasty-csi-iscsi
```

**StorageProfile for CDI:**

KubeVirt's [Containerized Data Importer (CDI)](https://github.com/kubevirt/containerized-data-importer) uses StorageProfiles to determine the default access mode and volume mode for DataVolumes. Configure a StorageProfile for your storage class:

```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: StorageProfile
metadata:
  name: nasty-csi-nvmeof  # must match the StorageClass name
spec:
  claimPropertySets:
    - accessModes:
        - ReadWriteMany
      volumeMode: Block
```

Create one for each block protocol storage class you use with KubeVirt (e.g., `nasty-csi-nvmeof`, `nasty-csi-iscsi`).

See the [KubeVirt live migration documentation](https://kubevirt.io/user-guide/compute/live_migration/#limitations) for more details on requirements.

## Infrastructure Features

### WebSocket API Client
- **Status**: ✅ Stable and functional
- **Description**: Resilient WebSocket client for NASty API communication
- **Features**:
  - Automatic reconnection with exponential backoff
  - Ping/pong heartbeat (30-second intervals)
  - Read/write deadline management
  - Connection state tracking
  - Graceful error handling
- **Endpoints**: 
  - `wss://` for HTTPS (recommended)
  - `ws://` for HTTP (development only)

### Connection Resilience
- **Status**: ✅ Implemented and tested
- **Description**: Automatic recovery from network disruptions
- **Features**:
  - Exponential backoff for reconnections (1s → 2s → 4s → ... max 30s)
  - Operation retries during connectivity issues
  - State preservation across reconnections
  - Connection health monitoring
- **Testing**: Validated with manual connection disruption tests

### High Availability (Controller)
- **Status**: ✅ Supported
- **Description**: Multiple controller replicas for redundancy
- **Implementation**: Kubernetes leader election
- **Default**: Single controller (can be increased via Helm chart)

## Observability Features

### Metrics (Prometheus)
- **Status**: ✅ Fully implemented
- **Endpoint**: `/metrics` on port 8080 (configurable)
- **Available Metrics**:

#### CSI Operation Metrics
- `nasty_csi_operations_total`: Counter of CSI operations by method and status
- `nasty_csi_operations_duration_seconds`: Histogram of operation durations

#### Volume Operation Metrics
- `nasty_volume_operations_total`: Counter by protocol, operation, and status
- `nasty_volume_operations_duration_seconds`: Histogram of volume operation durations
- `nasty_volume_capacity_bytes`: Gauge of provisioned volume sizes

#### WebSocket Metrics
- `nasty_websocket_connected`: Connection status gauge (1=connected, 0=disconnected)
- `nasty_websocket_reconnects_total`: Counter of reconnection attempts
- `nasty_websocket_messages_total`: Counter by direction (sent/received)
- `nasty_websocket_message_duration_seconds`: Histogram of API call durations
- `nasty_websocket_connection_duration_seconds`: Current connection duration

### ServiceMonitor Support
- **Status**: ✅ Implemented
- **Description**: Automatic Prometheus Operator integration
- **Configuration**: Optional, enabled via Helm chart values

### Logging
- **Status**: ✅ Comprehensive logging
- **Levels**: Standard klog verbosity levels (--v=1 to --v=10)
- **Default**: v=2 (info level)
- **Components**:
  - Controller logs: Volume operations, API interactions
  - Node logs: Mount/unmount operations, device management
  - Structured logging with context

### In-Cluster Web Dashboard
- **Status**: ✅ Fully implemented
- **Port**: 9090 (configurable)
- **Enable**: `controller.dashboard.enabled: true` in Helm values
- **Features**:
  - Live volume inventory with protocol, capacity, and health status
  - Volume health checks (verifies datasets, shares, subsystems, targets exist)
  - Kubernetes enrichment (PV/PVC binding, namespace, attached pods)
  - Snapshot and clone tracking
  - Unmanaged volume discovery (non-CSI volumes on same pool)
  - Parsed Prometheus metrics summary
  - JSON API endpoints for programmatic access
- **Access**: `kubectl port-forward svc/nasty-csi-driver-dashboard 9090:9090`, or configure Ingress

### kubectl Plugin Dashboard
- **Status**: ✅ Fully implemented
- **Port**: 2137 (default, configurable via `--port`)
- **Command**: `kubectl nasty-csi dashboard`
- **Description**: Local dashboard that connects directly to NASty via WebSocket. Same UI as the in-cluster dashboard but runs on your machine. Auto-discovers credentials from installed driver.

### Grafana Dashboard
- **Status**: ✅ Fully implemented
- **Enable**: `grafana.dashboards.enabled: true` in Helm values
- **Description**: Pre-built Grafana dashboard (`nasty-csi-overview.json`) provisioned via ConfigMap with sidecar auto-discovery
- **Panels**: WebSocket health, operation counts by protocol, operations by type (create/delete/expand), message throughput, per-protocol breakdown (NFS, NVMe-oF, iSCSI, SMB)

## Deployment Features

### Helm Chart
- **Status**: ✅ Production-ready chart
- **Registry**:
  - Docker Hub (recommended): `oci://registry-1.docker.io/bfenski/nasty-csi-driver`
  - GitHub Container Registry: `oci://ghcr.io/fenio/nasty-csi-driver`
- **Features**:
  - Configurable resource limits
  - Multiple storage class support (NFS, NVMe-oF, iSCSI, SMB)
  - ServiceMonitor for Prometheus
  - RBAC configuration
  - Customizable mount options
  - Volume expansion enabled by default

### Storage Classes
- **Status**: ✅ Flexible configuration
- **Support**: Multiple storage classes per driver installation
- **Parameters**:
  - Common: `protocol`, `pool`, `server`, `deleteStrategy`, `parentDataset`
  - Adoption: `markAdoptable`, `adoptExisting` (see "Volume Adoption" section)
  - NFS-specific: `path`
  - NVMe-oF specific: `subsystemNQN`, `fsType`, `transport`, `port`
  - SMB-specific: `smbCredentialsSecret` (name/namespace for nodeStageSecretRef)
  - Filesystem properties: See "Configurable Filesystem Properties" section below
- **Mount Options**: Configurable via StorageClass `mountOptions` field (see "Configurable Mount Options" above)

### Configurable Filesystem Properties
- **Status**: ✅ Implemented
- **Description**: Configure subvolume properties via StorageClass parameters
- **Prefix**: All filesystem properties use the `zfs.` prefix in StorageClass parameters (legacy naming, applies to bcachefs)

#### NFS (Subvolume) Properties
| Parameter | Description | Valid Values |
|-----------|-------------|--------------|
| `zfs.compression` | Compression algorithm | `off`, `lz4`, `gzip`, `gzip-1` to `gzip-9`, `zstd`, `zstd-1` to `zstd-19`, `lzjb`, `zle` |
| `zfs.dedup` | Deduplication | `off`, `on`, `verify`, `sha256`, `sha512` |
| `zfs.atime` | Access time updates | `on`, `off` |
| `zfs.sync` | Synchronous writes | `standard`, `always`, `disabled` |
| `zfs.recordsize` | Record size | `512`, `1K`, `2K`, `4K`, `8K`, `16K`, `32K`, `64K`, `128K`, `256K`, `512K`, `1M` |
| `zfs.copies` | Number of data copies | `1`, `2`, `3` |
| `zfs.snapdir` | Snapshot directory visibility | `hidden`, `visible` |
| `zfs.readonly` | Read-only mode | `on`, `off` |
| `zfs.exec` | Executable files | `on`, `off` |
| `zfs.aclmode` | ACL mode | `passthrough`, `restricted`, `discard`, `groupmask` |
| `zfs.acltype` | ACL type | `off`, `nfsv4`, `posix` |
| `zfs.casesensitivity` | Case sensitivity (creation only) | `sensitive`, `insensitive`, `mixed` |

#### NVMe-oF (Block Subvolume) Properties
| Parameter | Description | Valid Values |
|-----------|-------------|--------------|
| `zfs.compression` | Compression algorithm | `off`, `lz4`, `gzip`, `gzip-1` to `gzip-9`, `zstd`, `zstd-1` to `zstd-19`, `lzjb`, `zle` |
| `zfs.dedup` | Deduplication | `off`, `on`, `verify`, `sha256`, `sha512` |
| `zfs.sync` | Synchronous writes | `standard`, `always`, `disabled` |
| `zfs.copies` | Number of data copies | `1`, `2`, `3` |
| `zfs.readonly` | Read-only mode | `on`, `off` |
| `zfs.sparse` | Thin provisioning | `true`, `false` |
| `zfs.volblocksize` | Volume block size | `512`, `1K`, `2K`, `4K`, `8K`, `16K`, `32K`, `64K`, `128K` |

**Example StorageClass with Filesystem Properties:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-compressed
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  # Filesystem properties
  zfs.compression: "lz4"
  zfs.atime: "off"
  zfs.recordsize: "128K"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**Example NVMe-oF StorageClass with Filesystem Properties:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nvmeof-compressed
provisioner: nasty.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: nasty.local
  transport: tcp
  port: "4420"
  # Filesystem properties
  zfs.compression: "lz4"
  zfs.sparse: "true"
  zfs.volblocksize: "16K"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### Native Encryption
- **Status**: ✅ Implemented
- **Description**: Enable native encryption for subvolumes at creation time
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB

bcachefs native encryption provides transparent, at-rest encryption for your volumes. Once enabled, all data written to the volume is automatically encrypted using AES-256-GCM (default) or other supported algorithms.

#### StorageClass Parameters

| Parameter | Description | Default | Required |
|-----------|-------------|---------|----------|
| `encryption` | Enable encryption | `"false"` | Yes (to enable) |
| `encryptionAlgorithm` | Encryption algorithm | `"AES-256-GCM"` | No |
| `encryptionGenerateKey` | Auto-generate encryption key | `"false"` | One of key source options |

**Supported Algorithms:**
- `AES-256-GCM` (recommended, default)
- `AES-128-CCM`
- `AES-192-CCM`
- `AES-256-CCM`
- `AES-128-GCM`
- `AES-192-GCM`

#### Key Management Options

1. **Auto-generate Key** (simplest): NASty generates and manages the encryption key
   ```yaml
   encryption: "true"
   encryptionGenerateKey: "true"
   ```

2. **Passphrase**: Provide a passphrase via Kubernetes Secret (min 8 characters)
   ```yaml
   encryption: "true"
   csi.storage.k8s.io/provisioner-secret-name: encryption-secret
   csi.storage.k8s.io/provisioner-secret-namespace: kube-system
   ```

   Secret contents:
   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: encryption-secret
     namespace: kube-system
   type: Opaque
   stringData:
     encryptionPassphrase: "my-secret-passphrase"
   ```

3. **Hex Key**: Provide a 256-bit hex-encoded key via Kubernetes Secret (64 hex chars)
   ```yaml
   encryption: "true"
   csi.storage.k8s.io/provisioner-secret-name: encryption-secret
   csi.storage.k8s.io/provisioner-secret-namespace: kube-system
   ```

   Secret contents:
   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: encryption-secret
     namespace: kube-system
   type: Opaque
   stringData:
     encryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
   ```

**Example: Encrypted NFS StorageClass (Auto-Generated Key):**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-encrypted
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  encryption: "true"
  encryptionGenerateKey: "true"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

**Example: Encrypted NVMe-oF StorageClass (Passphrase from Secret):**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nvmeof-encrypted
provisioner: nasty.csi.io
parameters:
  protocol: nvmeof
  pool: tank
  server: nasty.local
  transport: tcp
  port: "4420"
  encryption: "true"
  encryptionAlgorithm: "AES-256-GCM"
  csi.storage.k8s.io/provisioner-secret-name: encryption-secret
  csi.storage.k8s.io/provisioner-secret-namespace: kube-system
allowVolumeExpansion: true
reclaimPolicy: Delete
---
apiVersion: v1
kind: Secret
metadata:
  name: encryption-secret
  namespace: kube-system
type: Opaque
stringData:
  encryptionPassphrase: "my-secret-passphrase-at-least-8-chars"
```

#### Important Notes

- **Key Recovery**: If using passphrase or hex key, you are responsible for key backup. Losing the key means losing access to encrypted data.
- **Auto-Generated Keys**: When using `encryptionGenerateKey: "true"`, NASty manages the key. The key is stored on NASty and is accessible to NASty administrators.
- **Existing Volumes**: Encryption can only be set at volume creation time. Existing unencrypted volumes cannot be encrypted.
- **Snapshots**: Snapshots of encrypted volumes inherit the encryption settings.
- **Performance**: Encryption has minimal performance impact with modern CPUs (AES-NI acceleration).

### Volume Metadata
- **Status**: ✅ Implemented
- **Description**: All volumes are tagged with xattr properties for reliable identification and cross-cluster adoption

The driver stores metadata as xattr properties on each volume's subvolume. This enables:
- Reliable volume identification without searching by name/path
- Cross-cluster volume adoption
- Ownership verification before deletion

#### Core Properties (All Volumes)

| Property | Description | Example |
|----------|-------------|---------|
| `nasty-csi:managed_by` | Ownership marker | `"nasty-csi"` |
| `nasty-csi:csi_volume_name` | CSI volume identifier | `"pvc-abc123"` |
| `nasty-csi:protocol` | Storage protocol | `"nfs"`, `"nvmeof"`, `"iscsi"`, or `"smb"` |
| `nasty-csi:capacity_bytes` | Volume size in bytes | `"10737418240"` |
| `nasty-csi:created_at` | Creation timestamp (RFC3339) | `"2024-01-15T10:30:00Z"` |
| `nasty-csi:delete_strategy` | Retain/delete policy | `"delete"` or `"retain"` |

#### Adoption Properties (For Cross-Cluster Adoption)

| Property | Description | Example |
|----------|-------------|---------|
| `nasty-csi:adoptable` | Whether volume can be adopted | `"true"` |
| `nasty-csi:pvc_name` | Original PVC name | `"my-data"` |
| `nasty-csi:pvc_namespace` | Original namespace | `"default"` |
| `nasty-csi:storage_class` | Original StorageClass | `"nasty-nfs"` |
| `nasty-csi:cluster_id` | Source cluster identifier | `"prod-cluster-1"` |

Shares are looked up by name/path -- there are no per-share ID xattr properties.

**Viewing Volume Properties:**
```bash
# On NASty, view xattr properties for a volume
getfattr -d /mnt/pool/csi/pvc-12345678
```

### Volume Adoption (Cross-Cluster)
- **Status**: ✅ Fully Implemented
- **Description**: Import existing nasty-csi managed volumes into a new Kubernetes cluster
- **Use Cases**:
  - GitOps recovery - recreate cluster from same Git repo, volumes are automatically adopted
  - Disaster recovery - restore volumes to a new cluster
  - Cluster migration - move workloads between clusters
  - Upgrade recovery - re-import retained volumes after breaking upgrades
  - **Migration from democratic-csi** - move volumes without data loss

**For step-by-step migration and adoption instructions, see [ADOPTION.md](ADOPTION.md).**

#### Automatic Adoption (GitOps)

**New in v0.8.0**: Volumes can be automatically adopted when a PVC with the same name is created.

**StorageClass Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `markAdoptable` | `bool` | `false` | Mark new volumes as adoptable (`nasty-csi:adoptable=true`) |
| `adoptExisting` | `bool` | `false` | Automatically adopt any managed volume with matching name |

**Adoption Behavior Matrix:**

| Volume has `adoptable=true` | StorageClass `adoptExisting` | Result |
|:---------------------------:|:---------------------------:|--------|
| Yes | No | Volume is adopted |
| No | Yes | Volume is adopted |
| Yes | Yes | Volume is adopted |
| No | No | Volume is NOT adopted (new volume created) |

**Example StorageClass for GitOps:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-gitops
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  parentDataset: csi
  server: nasty.local
  markAdoptable: "true"    # New volumes can be adopted by future clusters
  adoptExisting: "true"    # Adopt existing volumes with matching names
reclaimPolicy: Retain      # Keep volumes on PVC deletion
allowVolumeExpansion: true
```

**Adoption Process:**
1. When `CreateVolume` is called, the driver searches for an existing volume by CSI name
2. If found, it checks adoption eligibility (adoptable property or adoptExisting parameter)
3. If eligible, it re-creates any missing NASty resources (NFS share, NVMe-oF subsystem/namespace, or iSCSI target/extent)
4. Volume capacity is expanded if requested size is larger than existing size
5. Volume is returned as if newly created, but data is preserved

**Capacity Handling:**
- If requested capacity > existing capacity: volume is expanded
- If requested capacity ≤ existing capacity: existing capacity is used

#### Adoption Requirements

A volume is adoptable if it has:
1. `nasty-csi:managed_by` = `"nasty-csi"` (ownership marker)
2. `nasty-csi:protocol` set to `"nfs"`, `"nvmeof"`, `"iscsi"`, or `"smb"`

#### Manual Adoption Workflow

**Note:** Subvolume paths in examples use `{pool}/{parentDataset}/{volume}` format (e.g., `tank/csi/my-volume`).
Your actual paths depend on StorageClass configuration:
- `pool` parameter sets the pool (e.g., `tank`)
- `parentDataset` parameter sets an optional parent subvolume (e.g., `csi`)
- If `parentDataset` is not set, volumes are created directly under the pool

1. **Identify adoptable volumes** on NASty:
   ```bash
   # List all nasty-csi managed subvolumes (adjust path to match your pool/parentDataset)
   bcachefs subvolume list /mnt/pool
   ```

2. **Extract volume information**:
   ```bash
   # Get all properties for a specific volume
   getfattr -d /mnt/pool/my-volume
   ```

3. **Re-create NFS share or NVMe-oF namespace** if missing (NASty UI or API)

4. **Create static PV** in Kubernetes referencing the existing volume:
   ```yaml
   apiVersion: v1
   kind: PersistentVolume
   metadata:
     name: adopted-volume
   spec:
     capacity:
       storage: 10Gi  # Match nasty-csi:capacity_bytes
     accessModes:
       - ReadWriteMany
     persistentVolumeReclaimPolicy: Retain
     storageClassName: nasty-nfs
     csi:
       driver: nasty.csi.io
       volumeHandle: my-volume  # Must match nasty-csi:csi_volume_name
       volumeAttributes:
         protocol: nfs
         server: nasty.local
         share: /mnt/tank/my-volume  # Must match nasty-csi:nfs_share_path
   ```

5. **Create PVC** bound to the static PV:
   ```yaml
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: adopted-pvc
   spec:
     accessModes:
       - ReadWriteMany
     resources:
       requests:
         storage: 10Gi
     storageClassName: nasty-nfs
     volumeName: adopted-volume  # Bind to static PV
   ```

#### Automated Adoption CLI

The kubectl plugin provides CLI tooling for volume discovery and adoption:
```bash
# Discover orphaned volumes (volumes on NASty without matching PVCs)
kubectl nasty-csi list-orphaned

# Generate PV manifest to adopt a specific volume
kubectl nasty-csi adopt <subvolume-path> -o yaml > pv.yaml
kubectl apply -f pv.yaml

# Mark volumes as adoptable for future cluster recreation
kubectl nasty-csi mark-adoptable --all
```

See [kubectl Plugin Documentation](KUBECTL-PLUGIN.md) for full details on adoption workflows.

### Volume Name Templating
- **Status**: ✅ Implemented
- **Description**: Customize volume/subvolume names on NASty using Go templates
- **Protocols**: NFS, NVMe-oF, iSCSI, SMB
- **Use Cases**:
  - Use meaningful names instead of auto-generated PV UUIDs
  - Include namespace/PVC name in subvolume names for easier identification
  - Organize volumes with consistent naming patterns

#### Template Variables
| Variable | Description | Example Value |
|----------|-------------|---------------|
| `.PVCName` | PVC name | `postgres-data` |
| `.PVCNamespace` | PVC namespace | `production` |
| `.PVName` | PV name (CSI volume name) | `pvc-abc123-def456` |

#### StorageClass Parameters
| Parameter | Description | Example |
|-----------|-------------|---------|
| `nameTemplate` | Go template for full name | `{{ .PVCNamespace }}-{{ .PVCName }}` |
| `namePrefix` | Simple prefix | `prod-` |
| `nameSuffix` | Simple suffix | `-data` |
| `commentTemplate` | Go template for subvolume comment (visible in NASty UI) | `{{ .PVCNamespace }}/{{ .PVCName }}` |

**Note**: `nameTemplate` takes precedence over `namePrefix`/`nameSuffix` if both are specified.

#### Name Sanitization
Volume names are automatically sanitized for bcachefs compatibility:
- Invalid characters replaced with hyphens
- Leading/trailing hyphens removed
- Multiple consecutive hyphens collapsed
- Truncated to 63 characters (K8s label compatibility)

**Example StorageClass with Name Template:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-named
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  # Volume name templating
  nameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

With this StorageClass, a PVC named `postgres-data` in namespace `production` would create a subvolume named `tank/production-postgres-data` instead of `tank/pvc-abc123-def456-789...`.

**Example with Comment Template:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-commented
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  # Subvolume comment visible in NASty UI
  commentTemplate: "{{ .PVCNamespace }}/{{ .PVCName }}"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

With this StorageClass, subvolumes will have a comment like `production/postgres-data` visible in the NASty web UI, making it easy to identify which PVC a subvolume belongs to. Unlike volume names, comments are free-form text -- no sanitization or length limits are applied.

**Example with Simple Prefix/Suffix:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs-prefixed
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
  namePrefix: "k8s-"
  nameSuffix: "-vol"
allowVolumeExpansion: true
reclaimPolicy: Delete
```

### RBAC
- **Status**: ✅ Complete RBAC configuration
- **Components**:
  - ServiceAccounts for controller and node components
  - ClusterRoles with minimal required permissions
  - ClusterRoleBindings

## Testing Infrastructure

### CI/CD Pipeline
- **Status**: ✅ Fully automated
- **Platform**: GitHub Actions with self-hosted runner
- **Workflows**:
  - CI (lint, build, unit tests)
  - Integration tests (NFS, NVMe-oF, iSCSI, and SMB)
  - Release automation
  - Dashboard generation

### Integration Tests
- **Status**: ✅ Comprehensive test suite
- **Infrastructure**: Self-hosted (k3s + real NASty)
- **Test Scenarios**:
  - Basic volume provisioning and deletion (NFS, NVMe-oF, iSCSI, SMB)
  - Volume expansion (NFS, NVMe-oF, iSCSI, SMB)
  - Concurrent volume operations
  - StatefulSet workloads
  - Snapshot creation and restoration (NFS, NVMe-oF, iSCSI, SMB)
  - Volume adoption (GitOps workflows)
  - Connection resilience
  - Orphaned resource cleanup
  - Persistence testing
- **Execution**: Automatic on every push to main branch and pull requests

### Sanity Tests
- **Status**: ✅ CSI spec compliance testing
- **Framework**: csi-sanity test suite
- **Coverage**: Basic CSI operations validation

### Test Dashboard
- **Status**: ✅ Live dashboard
- **URL**: https://fenio.github.io/nasty-csi/dashboard/
- **Features**: Test results history, trend analysis

## Security Features

### API Authentication
- **Status**: ✅ Secure API key authentication
- **Storage**: Kubernetes Secrets
- **Support**: NASty API key authentication

### TLS Support
- **Status**: ✅ Supported
- **WebSocket**: WSS (WebSocket Secure) protocol
- **Recommended**: Always use `wss://` in production

### RBAC
- **Status**: ✅ Minimal privilege principle
- **Configuration**: Separate service accounts for controller and node components

## Kubernetes Feature Support

### Access Modes
- **NFS**:
  - ✅ ReadWriteMany (RWX) - Multiple pods on multiple nodes
  - ✅ ReadWriteOnce (RWO) - Single pod access
  - ✅ ReadWriteOncePod (RWOP) - Single pod access with stricter enforcement
- **NVMe-oF**:
  - ✅ ReadWriteOnce (RWO) - Block storage limitation
  - ✅ ReadWriteOncePod (RWOP) - Single pod access with stricter enforcement
- **iSCSI**:
  - ✅ ReadWriteOnce (RWO) - Block storage limitation
  - ✅ ReadWriteOncePod (RWOP) - Single pod access with stricter enforcement
- **SMB**:
  - ✅ ReadWriteMany (RWX) - Multiple pods on multiple nodes
  - ✅ ReadWriteOnce (RWO) - Single pod access
  - ✅ ReadWriteOncePod (RWOP) - Single pod access with stricter enforcement

### Volume Binding Modes
- ✅ Immediate - Volume provisioned immediately when PVC created
- ✅ WaitForFirstConsumer - Volume provisioned when pod scheduled

### Reclaim Policies
- ✅ Delete - Volume deleted when PVC removed (default)
- ✅ Retain - Volume kept on NASty after PVC deletion

### Storage Classes
- ✅ Multiple storage classes per driver
- ✅ Default storage class support
- ✅ Custom parameters per class

## Platform Support

### Kubernetes Distributions
- ✅ **Tested**: k3s (self-hosted CI/CD)
- ✅ **Supported**: Standard Kubernetes 1.27+
- ⚠️ **Should Work**: 
  - kind (local development)
  - K0s, K3s, RKE2
  - Managed Kubernetes (EKS, GKE, AKS) - untested
- **Note**: Earlier Kubernetes versions (< 1.27) may work but are not tested

### Operating Systems
- ✅ **Linux**: Primary platform
  - Ubuntu 22.04+ (tested)
  - Debian-based distributions
  - RHEL/CentOS-based distributions
- ❌ **Windows**: Not supported (Linux-focused driver)
- ❌ **macOS**: Not supported as node OS (development on macOS works)

### Architectures
- ✅ **amd64** (x86_64): Fully supported
- ✅ **arm64**: Fully supported (tested on Apple Silicon via UTM)

### Container Runtimes
- ✅ containerd (primary)
- ✅ CRI-O
- ⚠️ Docker (should work, not extensively tested)

## NASty Version Support

### Minimum Versions
- **NFS Support**: NASty Scale 25.10+
- **NVMe-oF Support**: NASty Scale 25.10+ (feature introduced in this version)

### API Compatibility
- **WebSocket API**: v2.0 (current endpoint: `/api/current`)
- **Authentication**: API key-based

### Required NASty Configuration

#### For NFS
- NFS service enabled
- Network access from Kubernetes nodes
- Pool with available space

#### For SMB
- SMB service enabled
- SMB user account configured (Credentials > Local Users)
- Network access from Kubernetes nodes (port 445)
- Pool with available space

#### For NVMe-oF
- **Static IP address** (DHCP not supported)
- **Pre-configured NVMe-oF port** with TCP transport (default: 4420)
- Accessible from Kubernetes nodes
- NVMe-oF service enabled

## Known Limitations

### General
- **Production Readiness**: Early development, not production-ready
- **Testing Coverage**: Core features functional, extensive validation needed
- **Error Handling**: Improving, some edge cases may not be covered

### Protocol-Specific

#### NFS
- Network latency affects performance
- NFSv4.2 required (older versions not tested)
- Firewall rules must allow NFS ports

#### NVMe-oF
- Requires NASty Scale 25.10+ (not available on NASty CORE)
- Static IP mandatory (DHCP interfaces not shown in configuration)
- Subsystem must be pre-configured (driver doesn't create subsystems)
- Block storage only (ReadWriteOnce access mode)
- TCP transport only (RDMA not implemented)

### Snapshots
- Cross-protocol cloning not supported (NFS ↔ NVMe-oF ↔ iSCSI ↔ SMB)
- Cross-pool cloning not supported
- Restored volumes must be same size or larger

### Volume Expansion
- Shrinking not supported
- Some operations may require volume to be unmounted

## Roadmap / Future Considerations

### Under Consideration (Not Committed)
- **Multi-pool Support**: Advanced scheduling across multiple NASty pools
- **Topology Awareness**: Multi-zone deployments
- **Volume Migration**: Move volumes between protocols/pools
- **Quota Management**: Advanced quota and reservation features

### Not Planned
- **Windows Node Support**: Linux-focused driver (SMB support is for Linux CIFS clients)
- **Legacy Protocol Support**: Focus on modern protocols only

## Performance Characteristics

### NFS
- **Throughput**: Network-limited, typically 1-10 Gbps depending on network
- **Latency**: ~1-5ms additional latency vs local storage
- **IOPS**: Moderate (1000-10000 IOPS typical)
- **Best For**: Shared file storage, read-heavy workloads, multi-pod access

### NVMe-oF
- **Throughput**: Higher than NFS, can approach local NVMe speeds
- **Latency**: Lower than NFS (~100-500µs additional latency)
- **IOPS**: High (10000-100000+ IOPS depending on storage)
- **Best For**: Databases, high-performance applications, latency-sensitive workloads

### Snapshots
- **Creation Time**: Near-instant regardless of volume size
- **Space Overhead**: Minimal until data diverges
- **Restore Time**: Instant (clone operation)

## Documentation

### Available Documentation
- ✅ README.md - Project overview and quick start
- ✅ DEPLOYMENT.md - Detailed deployment guide
- ✅ KUBECTL-PLUGIN.md - kubectl plugin for volume management
- ✅ QUICKSTART.md - NFS quick start guide
- ✅ QUICKSTART-NVMEOF.md - NVMe-oF setup guide
- ✅ QUICKSTART-SMB.md - SMB/CIFS setup guide
- ✅ SNAPSHOTS.md - Snapshot and cloning guide
- ✅ ADOPTION.md - Volume adoption and migration guide (including democratic-csi migration)
- ✅ METRICS.md - Prometheus metrics documentation
- ✅ TESTING.md - Comprehensive testing guide and infrastructure details
- ✅ FEATURES.md - This document
- ✅ CONTRIBUTING.md - Contribution guidelines
- ✅ CONNECTION_RESILIENCE_TEST.md - Connection testing guide

### Helm Chart Documentation
- ✅ charts/nasty-csi-driver/README.md - Complete Helm configuration reference
- ✅ charts/nasty-csi-driver/values.yaml - Documented default values

## Testing Infrastructure

### Real Hardware, Real Tests

All features are tested on **real infrastructure** - not mocks or simulators:

**Test Environment:**
- ✅ Self-hosted GitHub Actions runner (dedicated Akamai/Linode infrastructure)
- ✅ Real Kubernetes clusters (k3s) provisioned for each test run
- ✅ Real NASty Scale 25.10+ server with actual storage pools
- ✅ Real protocol operations (NFS mounts, NVMe-oF connections, SMB shares, actual I/O)

**CSI Specification Compliance:**
- ✅ Passes [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) v5.4.0 sanity tests
- ✅ Full CSI specification compliance verified

**Integration Test Coverage:**
- ✅ Basic volume operations (NFS, NVMe-oF & iSCSI)
- ✅ Volume expansion testing
- ✅ Snapshot creation and restoration
- ✅ Volume adoption (GitOps workflows)
- ✅ StatefulSet volume management (3 replica testing)
- ✅ Data persistence across pod restarts
- ✅ Concurrent volume creation (5 simultaneous volumes)
- ✅ Connection resilience (WebSocket reconnection)
- ✅ Orphaned resource detection and cleanup

**Test Results:**
- View live dashboard: [Test Dashboard](https://fenio.github.io/nasty-csi/dashboard/)
- CI status: [![Integration Tests](https://github.com/nasty-project/nasty-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/nasty-project/nasty-csi/actions/workflows/integration.yml)

See [TESTING.md](TESTING.md) for comprehensive testing documentation.

## Getting Started

### Minimum Requirements
1. Kubernetes cluster 1.27+
2. NASty Scale 25.10+
3. NASty API key
4. Helm 3.0+
5. Protocol-specific tools on nodes: NFS client (NFS), nvme-cli (NVMe-oF), open-iscsi (iSCSI), or cifs-utils (SMB)

### Quick Install (NFS)
```bash
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="nasty-csi-nfs" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="nfs" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

### Quick Install (NVMe-oF)
```bash
# Pre-requisite: Configure NVMe-oF port in NASty first!
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="nasty-csi-nvmeof" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="nvmeof" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

### Quick Install (iSCSI)
```bash
# Pre-requisite: Configure iSCSI portal in NASty and install open-iscsi on nodes!
helm install nasty-csi oci://registry-1.docker.io/bfenski/nasty-csi-driver \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP:443/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name="nasty-csi-iscsi" \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol="iscsi" \
  --set storageClasses[0].pool="YOUR-POOL-NAME" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

## Support and Community

### Reporting Issues
- GitHub Issues: https://github.com/nasty-project/nasty-csi/issues
- Include: Kubernetes version, NASty version, logs, reproduction steps

### Contributing
- See CONTRIBUTING.md for guidelines
- Pull requests welcome
- Focus areas: Testing, documentation, bug fixes

### Status Updates
- Test Dashboard: https://fenio.github.io/nasty-csi/dashboard/
- GitHub Actions: https://github.com/nasty-project/nasty-csi/actions

---

**Last Updated**: 2026-01-29
**Driver Version**: v0.17.3
**Kubernetes Version Tested**: 1.27+
**Go Version**: 1.26.0+
