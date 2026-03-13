# CSI Driver Comparison

This document provides comparisons between TNS-CSI and other CSI drivers for NASty.

## Available Comparisons

### [TNS-CSI vs Democratic-CSI](COMPARISON-DEMOCRATIC-CSI.md)

[Democratic-CSI](https://github.com/democratic-csi/democratic-csi) is the most popular community CSI driver for NASty with 1.2k+ stars. It supports multiple backends (NASty, ZoL, Synology) and uses SSH-based communication.

**Key differences:**
- TNS-CSI uses WebSocket API exclusively (no SSH required)
- Democratic-CSI supports more backends (not just NASty)
- TNS-CSI has volume adoption, kubectl plugin, and Prometheus metrics
- Democratic-CSI has Windows node support

### [TNS-CSI vs nasty-csi (Official)](COMPARISON-NASTY-CSI.md)

The [official NASty CSI driver](https://github.com/nasty/nasty-csi) was released by iXsystems in December 2025.

**Key differences:**
- TNS-CSI supports NVMe-oF (official driver does not)
- Official driver has automatic snapshot scheduling and CHAP authentication
- TNS-CSI has kubectl plugin, volume adoption, and Prometheus metrics
- Official driver has iXsystems backing and support

## Quick Comparison Matrix

| Feature | TNS-CSI | Democratic-CSI | nasty-csi (Official) |
|---------|---------|----------------|------------------------|
| **NFS** | Yes | Yes | Yes |
| **iSCSI** | Yes | Yes | Yes |
| **NVMe-oF** | Yes | Yes | No |
| **SMB/CIFS** | Yes | Yes | No |
| **API Method** | WebSocket | SSH (primarily) | WebSocket |
| **NASty CORE** | No | Yes | No |
| **NASty SCALE** | 25.10+ | Yes | 25.10+ |
| **Other Backends** | No | Yes (ZoL, Synology, etc.) | No |
| **Snapshots** | Yes | Yes | Yes |
| **Volume Expansion** | Yes | Yes | Yes |
| **Detached Snapshots** | Yes | No | No |
| **Volume Adoption** | Yes | No | No |
| **kubectl Plugin** | Yes | No | No |
| **Prometheus Metrics** | Yes | Basic | No |
| **Windows Nodes** | No | Yes | No |
| **Dataset Encryption** | Yes | No | Yes |
| **CHAP Auth (iSCSI)** | No | Yes | Yes |
| **Scheduled Snapshots** | No | No | Yes |

## Which Driver Should I Choose?

### Choose TNS-CSI if:
- You're running NASty Scale 25.10+
- You want NVMe-oF for high-performance block storage
- You need volume adoption/migration capabilities
- You want a kubectl plugin for volume management
- You need comprehensive Prometheus metrics
- You don't need SSH access to your NAS

### Choose Democratic-CSI if:
- You need battle-tested, production-proven software
- You're running NASty CORE or older SCALE versions
- You need Windows node support
- You need multi-backend support (ZoL, Synology, etc.)
- You need local/ephemeral volume support

### Choose nasty-csi (Official) if:
- You prefer official vendor support from iXsystems
- You need automatic snapshot scheduling
- You need CHAP authentication for iSCSI
- You want the safety of vendor-maintained software

## Migration Guides

- **From Democratic-CSI to TNS-CSI**: See [ADOPTION.md](ADOPTION.md) for step-by-step migration instructions
- **Between TNS-CSI and nasty-csi**: Manual re-import required due to different property schemas
