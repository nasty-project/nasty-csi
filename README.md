# NASty CSI Driver

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.26.0-00ADD8?logo=go)](https://go.dev/)
[![Go Report Card](https://goreportcard.com/badge/github.com/nasty-project/nasty-csi)](https://goreportcard.com/report/github.com/nasty-project/nasty-csi)
[![CI](https://github.com/nasty-project/nasty-csi/actions/workflows/ci.yml/badge.svg)](https://github.com/nasty-project/nasty-csi/actions/workflows/ci.yml)
[![Integration Tests](https://github.com/nasty-project/nasty-csi/actions/workflows/integration.yml/badge.svg)](https://github.com/nasty-project/nasty-csi/actions/workflows/integration.yml)
[![Distro Compatibility](https://github.com/nasty-project/nasty-csi/actions/workflows/distro-compatibility.yml/badge.svg)](https://github.com/nasty-project/nasty-csi/actions/workflows/distro-compatibility.yml)
[![GHCR](https://img.shields.io/badge/GHCR-nasty--csi-blue?logo=github)](https://github.com/nasty-project/nasty-csi/pkgs/container/nasty-csi)
[![Driver](https://img.shields.io/github/v/release/nasty-project/nasty-csi?filter=v*&label=driver&logo=github)](https://github.com/nasty-project/nasty-csi/releases/latest)
[![Plugin](https://img.shields.io/github/v/release/nasty-project/nasty-plugin?label=kubectl-nasty&logo=github)](https://github.com/nasty-project/nasty-plugin/releases/latest)

A Kubernetes CSI driver for [NASty](https://github.com/nasty-project/nasty) — a NAS appliance built on NixOS and bcachefs.

## Important Disclaimer

**This project is in early development phase and is NOT production-ready**
- Use of this software is entirely at your own risk
- Extensive testing and validation required before production use

## Overview

This CSI driver enables Kubernetes to provision and manage persistent volumes on NASty. It currently supports:

- **NFS** - Network File System for file-based storage
- **NVMe-oF** - NVMe over Fabrics for high-performance block storage
- **iSCSI** - Traditional block storage protocol with broad compatibility
- **SMB/CIFS** - Authenticated file sharing with Windows compatibility

## Dashboard and Observability

<img width="1380" height="914" alt="image" src="https://github.com/user-attachments/assets/5d2ce624-2031-442d-8f6f-5422bce9bab7" />

The driver includes two dashboard options and a pre-built Grafana dashboard:

- **In-cluster dashboard** — runs inside the controller pod (port 9090), enable with `controller.dashboard.enabled: true`
- **kubectl plugin dashboard** — runs locally via `kubectl nasty dashboard` (port 2137)
- **Grafana dashboard** — pre-built Prometheus dashboard, enable with `grafana.dashboards.enabled: true`

See [METRICS.md](docs/METRICS.md) for setup details.

## Protocol Selection Guide

- **NFS**: Shared file storage with ReadWriteMany support
- **NVMe-oF**: High-performance block storage with the lowest latency — ideal for databases
- **iSCSI**: Block storage with broad kernel and infrastructure support
- **SMB/CIFS**: Authenticated file sharing with Windows compatibility

## Features

- Dynamic volume provisioning and deletion
- Volume snapshots — create, delete, and restore (all protocols)
- Volume cloning from snapshots
- Online volume expansion (all protocols)
- Volume retention — optional `deleteStrategy: retain` to preserve data on PVC deletion
- Volume adoption — import orphaned volumes across clusters (see [Adoption Guide](docs/ADOPTION.md))
- Access modes — RWO, RWOP, and RWX
- Raw block RWX for KubeVirt live migration (NVMe-oF, iSCSI)
- Configurable mount options via StorageClass
- WebSocket connection resilience with automatic reconnection

## kubectl Plugin

A companion kubectl plugin for managing volumes from the command line is available at [nasty-project/nasty-plugin](https://github.com/nasty-project/nasty-plugin):

```bash
# Install via krew
kubectl krew install nasty

# Or download from GitHub releases
```

**Key Commands:**
| Command | Description |
|---------|-------------|
| `kubectl nasty summary` | Dashboard overview of all resources |
| `kubectl nasty list` | List all managed volumes |
| `kubectl nasty list-snapshots` | List snapshots with source volumes |
| `kubectl nasty health` | Check health of all volumes |
| `kubectl nasty troubleshoot <pvc>` | Diagnose PVC issues |
| `kubectl nasty cleanup` | Delete orphaned volumes |
| `kubectl nasty dashboard` | Start web dashboard on http://localhost:2137 |

The plugin auto-discovers credentials from the installed driver, so it works out of the box on clusters with nasty-csi installed.

## Kubernetes Distribution Compatibility

This driver is tested and verified to work on **6 Kubernetes distributions** with NFS, NVMe-oF, iSCSI, and SMB protocols:

| Distribution | NFS | NVMe-oF | iSCSI | SMB | Description |
|--------------|:---:|:-------:|:-----:|:---:|-------------|
| K3s | ✅ | ✅ | ✅ | ✅ | Lightweight Kubernetes by Rancher |
| K0s | ✅ | ✅ | ✅ | ✅ | Zero-friction Kubernetes by Mirantis |
| KubeSolo | ✅ | ✅ | ✅ | ✅ | Single-node Kubernetes |
| Minikube | ✅ | ✅ | ✅ | ✅ | Local Kubernetes for development |
| Talos | ✅ | ✅ | ✅ | ✅ | Secure, immutable Kubernetes OS |
| MicroK8s | ✅ | ✅ | ✅ | ✅ | Lightweight Kubernetes by Canonical |

Compatibility tests run weekly and on-demand. See [Distro Compatibility Tests](docs/DISTRO-COMPATIBILITY.md) for details.

**OpenShift** is also confirmed to work by community users. Set `openshift.enabled=true` in Helm values to create the required SecurityContextConstraints. See [DEPLOYMENT.md](docs/DEPLOYMENT.md#openshift) for details.

## Prerequisites

- Kubernetes 1.27+ (earlier versions may work but are not tested)
- A running NASty server with API access
- For NFS: NFS client utilities on all nodes (`nfs-common` on Debian/Ubuntu, `nfs-utils` on RHEL/CentOS)
- For NVMe-oF:
  - `nvme-cli` package installed on all Kubernetes nodes
  - Kernel modules: `nvme-tcp`, `nvme-fabrics`
  - Network connectivity from Kubernetes nodes to NASty on port 4420
- For iSCSI:
  - `open-iscsi` package installed on all Kubernetes nodes (`iscsid` service running)
  - Network connectivity from Kubernetes nodes to NASty on port 3260
- For SMB:
  - `cifs-utils` package installed on all Kubernetes nodes
  - Kubernetes Secret with SMB credentials (username/password)
  - Network connectivity from Kubernetes nodes to NASty on port 445

## Quick Start

See [DEPLOYMENT.md](docs/DEPLOYMENT.md) for detailed installation and configuration instructions.

### Installation via Helm (Recommended)

The NASty CSI Driver is published to both Docker Hub and GitHub Container Registry as OCI artifacts:

**Always use a specific version in production.** See [docs/VERSIONING.md](docs/VERSIONING.md) for details.

```bash
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.4 \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-nfs \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nfs \
  --set storageClasses[0].filesystem="YOUR-FILESYSTEM" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

**NVMe-oF:**
```bash
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.4 \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-nvmeof \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=nvmeof \
  --set storageClasses[0].filesystem="YOUR-FILESYSTEM" \
  --set storageClasses[0].server="YOUR-NASTY-IP" \
  --set storageClasses[0].transport=tcp \
  --set storageClasses[0].port=4420
```

**iSCSI:**
```bash
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.4 \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-iscsi \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=iscsi \
  --set storageClasses[0].filesystem="YOUR-FILESYSTEM" \
  --set storageClasses[0].server="YOUR-NASTY-IP"
```

**SMB:**
```bash
helm install nasty-csi oci://ghcr.io/nasty-project/charts/nasty-csi-driver \
  --version 0.0.4 \
  --namespace kube-system \
  --create-namespace \
  --set nasty.url="wss://YOUR-NASTY-IP/api/current" \
  --set nasty.apiKey="YOUR-API-KEY" \
  --set storageClasses[0].name=nasty-csi-smb \
  --set storageClasses[0].enabled=true \
  --set storageClasses[0].protocol=smb \
  --set storageClasses[0].filesystem="YOUR-FILESYSTEM" \
  --set storageClasses[0].server="YOUR-NASTY-IP" \
  --set storageClasses[0].smbCredentialsSecret.name=smb-credentials \
  --set storageClasses[0].smbCredentialsSecret.namespace=kube-system
```

See the [Helm chart repository](https://github.com/nasty-project/nasty-chart) for detailed configuration options.

## Configuration

### Command-Line Flags

- `--endpoint` - CSI endpoint (default: `unix:///var/lib/kubelet/plugins/nasty.csi.io/csi.sock`)
- `--node-id` - Node identifier (typically the node name)
- `--driver-name` - CSI driver name (default: `nasty.csi.io`)
- `--api-url` - NASty API WebSocket URL (e.g., `wss://YOUR-NASTY-IP/api/current`)
- `--api-key` - NASty API key
- `--max-concurrent-nvme-connects` - Maximum concurrent NVMe-oF connect operations per node (default: `5`)

### Storage Class Parameters

**NFS Volumes:**
```yaml
parameters:
  protocol: nfs
  server: YOUR-NASTY-IP
  filesystem: YOUR-FILESYSTEM
```

**NVMe-oF Volumes:**
```yaml
parameters:
  protocol: nvmeof
  server: YOUR-NASTY-IP
  filesystem: YOUR-FILESYSTEM
  fsType: ext4  # or xfs
```

**Optional parameters** (all protocols):

| Parameter | Description |
|-----------|-------------|
| `compression` | Compression algorithm (`lz4`, `zstd`, `none`) |
| `foregroundTarget` | Device group label for foreground writes |
| `backgroundTarget` | Device group label for background moves |
| `promoteTarget` | Device group label for read promotion (cache tier) |
| `metadataTarget` | Device group label for metadata/btree writes |
| `dataReplicas` | Number of data replicas (e.g., `"1"` for expendable data) |
| `deleteStrategy` | `delete` (default) or `retain` |
| `encryption` | `true` to require encrypted filesystem |
| `markAdoptable` | `true` to allow cross-cluster volume adoption |

## Testing

This driver is tested against a **real NASty server** with actual bcachefs storage — not mocks or simulators:

- **QEMU VMs on GitHub-hosted runners** provision a fresh k3s cluster per test run
- **Real NASty server** with bcachefs pools, NFS/SMB shares, NVMe-oF subsystems, and iSCSI targets
- **Full protocol stack** — actual NFS mounts, NVMe-oF TCP connections, iSCSI sessions, and SMB shares

### Automated Test Suite

Every commit triggers integration tests across all four protocols:

**Core Functionality:**
- Volume provisioning, deletion, and expansion
- Snapshot creation, restoration, and cloning
- Volume adoption for disaster recovery and GitOps workflows
- StatefulSet volume management
- Data persistence across pod restarts

**Stress & Reliability:**
- Concurrent volume creation
- WebSocket connection resilience
- Orphaned resource detection and cleanup

**CSI Specification Compliance:**
- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) v5.4.0 sanity suite

## Project Status

**⚠️ This project is in early development and is not production-ready.**

Core features (provisioning, snapshots, expansion, adoption) are functional and tested across all four protocols. Use in development and evaluation environments. Production deployments should proceed with caution — edge cases may exist.

## Troubleshooting

See [DEPLOYMENT.md](docs/DEPLOYMENT.md#troubleshooting) for detailed troubleshooting steps.

**Common Issues:**

1. **Pods stuck in ContainerCreating**:
   - For NFS: Check that NFS client utilities are installed on nodes
   - For NVMe-oF: Check that nvme-cli is installed and kernel modules are loaded (`nvme-tcp`, `nvme-fabrics`)
   - For SMB: Check that cifs-utils is installed and credentials Secret exists
2. **Failed to create volume**: Verify NASty API credentials and network connectivity
3. **Mount failed**: Ensure the corresponding service is running on NASty and the port is reachable

**View Logs:**

```bash
# Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=controller

# Node logs
kubectl logs -n kube-system -l app.kubernetes.io/name=nasty-csi-driver,app.kubernetes.io/component=node

# Check version
kubectl logs -n kube-system deployment/nasty-csi-controller 2>&1 | head -1
```

## Documentation

- [Features Documentation](docs/FEATURES.md) - Comprehensive feature support reference
- [Deployment Guide](docs/DEPLOYMENT.md) - Detailed installation and configuration
- [kubectl Plugin](docs/KUBECTL-PLUGIN.md) - Command-line tool for volume management
- [Quick Start - NFS](docs/QUICKSTART.md) - Get started with NFS volumes
- [Quick Start - NVMe-oF](docs/QUICKSTART-NVMEOF.md) - Get started with NVMe-oF volumes
- [Quick Start - iSCSI](docs/QUICKSTART-ISCSI.md) - Get started with iSCSI volumes
- [Quick Start - SMB](docs/QUICKSTART-SMB.md) - Get started with SMB volumes
- [Snapshots Guide](docs/SNAPSHOTS.md) - Volume snapshots and cloning
- [Versioning](docs/VERSIONING.md) - Version management and checking installed version
- [Distro Compatibility](docs/DISTRO-COMPATIBILITY.md) - Kubernetes distribution compatibility testing
- [Metrics Guide](docs/METRICS.md) - Prometheus metrics and monitoring
- [Kind Setup](docs/KIND.md) - Local development with Kind
- [Comparison with Democratic-CSI](docs/COMPARISON-DEMOCRATIC-CSI.md) - vs democratic-csi

## Volume Adoption

The driver supports **cross-cluster volume adoption** — importing existing nasty-csi managed volumes into a new Kubernetes cluster. This is useful for:
- Disaster recovery scenarios
- Cluster migrations
- Re-importing retained volumes after upgrades

Volumes are adoptable if they have proper `nasty-csi:*` xattr properties set. See [Volume Adoption](docs/FEATURES.md#volume-adoption-cross-cluster) in the Features documentation for details.

## Development

### Prerequisites

- Go 1.26+
- Docker (for building images)
- Kubernetes cluster for testing

### Building

```bash
make build
```

### Testing

```bash
# Unit tests
make test

# CSI sanity tests
make test-sanity

# E2E tests (requires NASty server and Kubernetes cluster)
ginkgo -v --timeout=55m ./tests/e2e/nfs/...
ginkgo -v --timeout=90m ./tests/e2e/nvmeof/...
ginkgo -v --timeout=90m ./tests/e2e/iscsi/...
ginkgo -v --timeout=55m ./tests/e2e/smb/...
```

See [docs/TESTING.md](docs/TESTING.md) for details.

### Building Container Image

```bash
make docker-build
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

This project is licensed under the GNU General Public License v3.0 (GPL-3.0) - see the LICENSE file for details.
