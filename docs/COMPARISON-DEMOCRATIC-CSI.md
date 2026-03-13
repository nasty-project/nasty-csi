# TNS-CSI vs Democratic-CSI

[Democratic-CSI](https://github.com/democratic-csi/democratic-csi) is the most popular community CSI driver for TrueNAS with 1.2k+ stars.

**Last Updated**: January 2026

## Overview

| Aspect | TNS-CSI | Democratic-CSI |
|--------|---------|----------------|
| **Maturity** | Early development | Mature, established |
| **Language** | Go | JavaScript (Node.js) |
| **License** | GPL-3.0 | MIT |
| **TrueNAS Version** | Scale 25.10+ only | FreeNAS/TrueNAS (multiple versions) |
| **API Connection** | WebSocket API only (no SSH) | SSH-based or HTTP API (experimental) |

## Protocol Support

| Protocol | TNS-CSI | Democratic-CSI |
|----------|---------|----------------|
| **NFS** | Yes | Yes |
| **iSCSI** | Yes | Yes |
| **NVMe-oF** | Yes | Yes (zfs-generic-nvmeof driver) |
| **SMB/CIFS** | Yes | Yes |

## Key Differences

### Architecture Philosophy

**TNS-CSI:**
- Supports all four major storage protocols (NFS, iSCSI, NVMe-oF, SMB)
- WebSocket-based API communication (no SSH required)
- Single-purpose: TrueNAS Scale 25.10+ only
- Native Go implementation with minimal dependencies

**Democratic-CSI:**
- Multi-backend support (TrueNAS, ZoL, Synology, ObjectiveFS, etc.)
- Primarily SSH-based with experimental API-only drivers (`freenas-api-*`)
- Broader compatibility with older TrueNAS/FreeNAS versions
- iSCSI as the primary block storage protocol
- Node.js implementation with extensive driver ecosystem

### Backend Support

**TNS-CSI:**
- TrueNAS Scale 25.10+ (exclusively)

**Democratic-CSI:**
- FreeNAS / TrueNAS (CORE and SCALE)
- ZFS on Linux (Ubuntu, etc.)
- Synology (experimental)
- ObjectiveFS
- Lustre (client mode)
- Local hostpath provisioning
- NFS/SMB client modes
- Node-local ZFS (dataset/zvol)

## Feature Comparison

| Feature | TNS-CSI | Democratic-CSI |
|---------|---------|----------------|
| Dynamic provisioning | Yes | Yes |
| Volume expansion | Yes | Yes |
| Snapshots | Yes | Yes |
| Cloning | Yes | Yes |
| Detached snapshots | Yes | No |
| RWX (ReadWriteMany) | Yes (NFS) | Yes |
| Volume health monitoring | Yes (GET_VOLUME) | No |
| Volume name templating | Yes | Yes |
| Delete strategy (retention) | Yes | No |
| Configurable mount options | Yes | Yes |
| ZFS property configuration | Yes | Limited |
| Windows nodes | No | Yes (v1.7.0+) |
| Multipath | NVMe-native, iSCSI | iSCSI multipath |
| Local ephemeral volumes | No | Yes |
| Prometheus metrics | Yes | No (basic) |
| kubectl plugin | Yes | No |
| Volume adoption | Yes | No |
| Dataset encryption | Yes | No |

## Configuration Complexity

**TNS-CSI:**
- Simpler configuration (fewer options)
- Helm chart or kubectl manifests
- No SSH setup required
- API key authentication only

**Democratic-CSI:**
- More complex configuration with many options
- Requires SSH setup and potentially sudo configuration for most drivers
- Experimental `freenas-api-*` drivers work without SSH (SCALE 21.08+)
- Helm chart with extensive example values
- May require shell configuration on TrueNAS

## When to Choose Each

### Choose TNS-CSI if:

- You're running TrueNAS Scale 25.10+
- You want all three protocols (NFS, iSCSI, NVMe-oF) from a single driver
- You prefer a simpler, focused driver with fewer moving parts
- You don't want to configure SSH access to your NAS
- You need volume health monitoring (ControllerGetVolume)
- You want comprehensive Prometheus metrics
- You need volume adoption/migration features
- You want a kubectl plugin for volume management
- You prefer native Go implementation

### Choose Democratic-CSI if:

- You need production-ready, battle-tested software
- You're running older TrueNAS/FreeNAS versions or TrueNAS CORE
- You need Windows node support
- You want multi-backend flexibility (ZoL, Synology, ObjectiveFS, etc.)
- You need local/ephemeral volume support
- You need Nomad or Docker Swarm support

## NVMe-oF vs iSCSI: When to Use Which?

TNS-CSI supports both NVMe-oF and iSCSI for block storage. Here's when to choose each:

### Choose NVMe-oF when:
- You have fast networks (10GbE+) and want maximum performance
- You're running modern NVMe SSDs and want to utilize their parallel I/O
- You want lower protocol overhead and latency
- Your environment supports nvme-tcp kernel module

### Choose iSCSI when:
- You need broad compatibility with existing infrastructure
- Your network is slower (1GbE) where protocol overhead matters less
- You're more familiar with iSCSI administration
- You need CHAP authentication (note: TNS-CSI doesn't support CHAP yet)

## Related Links

- [TNS-CSI GitHub](https://github.com/nasty-project/nasty-csi)
- [Democratic-CSI GitHub](https://github.com/democratic-csi/democratic-csi)
- [Democratic-CSI Helm Charts](https://github.com/democratic-csi/charts)
