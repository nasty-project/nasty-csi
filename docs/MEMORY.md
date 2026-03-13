# Memory Usage Comparison: tns-csi vs democratic-csi

This document compares memory usage between tns-csi and democratic-csi drivers based on real-world measurements from a 3-node Kubernetes cluster running both drivers simultaneously.

## Test Environment

- **Cluster**: 3-node k3s cluster
- **TrueNAS**: TrueNAS Scale 25.10
- **democratic-csi**: Separate deployments for NFS and iSCSI protocols
- **tns-csi**: Single unified deployment handling all protocols

## Total Memory Usage

| Component | democratic-csi | tns-csi | Reduction |
|-----------|----------------|---------|-----------|
| Controller | 238 Mi | 190 Mi | 1.3x |
| Node (×3) | 677 Mi | 138 Mi | 4.9x |
| **Total** | **915 Mi** | **328 Mi** | **2.8x** |

> democratic-csi requires separate deployments for NFS and iSCSI, so totals include both.

## Driver-Only Memory (Excluding CSI Sidecars)

When comparing just the driver containers (excluding standard CSI sidecars like provisioner, attacher, resizer):

### Controller

| Driver | Containers | Memory |
|--------|------------|--------|
| democratic-csi (iSCSI) | csi-driver + csi-proxy | 69 Mi |
| democratic-csi (NFS) | csi-driver + csi-proxy | 68 Mi |
| **democratic-csi total** | | **137 Mi** |
| **tns-csi** | nasty-csi-plugin | **4 Mi** |

**Controller driver reduction: 34x**

### Node (per node)

| Driver | Containers | Memory |
|--------|------------|--------|
| democratic-csi (iSCSI) | csi-driver + csi-proxy | ~72 Mi |
| democratic-csi (NFS) | csi-driver + csi-proxy | ~126 Mi |
| **democratic-csi total** | | **~198 Mi** |
| **tns-csi** | nasty-csi-plugin | **~25 Mi** |

**Node driver reduction: 8x**

### Driver-Only Summary

| | democratic-csi | tns-csi | Reduction |
|--|----------------|---------|-----------|
| Controller | 137 Mi | 4 Mi | 34x |
| Nodes (3 total) | 594 Mi | 75 Mi | 8x |
| **Total** | **731 Mi** | **79 Mi** | **9x** |

## Container Count

| Component | democratic-csi | tns-csi |
|-----------|----------------|---------|
| Controller containers | 6 per protocol | 4 total |
| Node containers | 4 per protocol | 2 total |
| Protocols requiring separate deployment | Yes (NFS, iSCSI) | No (unified) |

democratic-csi requires a `csi-proxy` sidecar because the Node.js driver cannot directly expose a gRPC socket. tns-csi's Go binary handles this natively.

## Why the Difference?

### Runtime Overhead

| Aspect | democratic-csi | tns-csi |
|--------|----------------|---------|
| Language | Node.js | Go |
| Runtime | V8 JavaScript engine | Native binary |
| Base memory | ~65-120 Mi | ~4-25 Mi |

Node.js has significant baseline memory overhead due to:
- V8 JavaScript engine
- JIT compilation
- Garbage collector memory pools
- Node.js runtime libraries

Go compiles to a native binary with minimal runtime overhead.

### Architecture

| Aspect | democratic-csi | tns-csi |
|--------|----------------|---------|
| Protocol handling | Separate deployment per protocol | Single unified deployment |
| gRPC exposure | Requires csi-proxy sidecar | Native gRPC support |
| TrueNAS communication | SSH + WebSocket | WebSocket only |

## Scaling Considerations

Memory savings scale with cluster size:

| Cluster Size | democratic-csi | tns-csi | Savings |
|--------------|----------------|---------|---------|
| 3 nodes | 915 Mi | 328 Mi | 587 Mi |
| 10 nodes | 2,218 Mi | 488 Mi | 1,730 Mi |
| 50 nodes | 10,138 Mi | 1,288 Mi | 8,850 Mi |

*Calculated assuming linear node scaling with fixed controller overhead.*

## Conclusion

tns-csi provides significant memory savings compared to democratic-csi:

- **9x less memory** for driver components alone
- **2.8x less memory** including all CSI sidecars
- **Fewer containers** to manage and monitor
- **Single deployment** for all storage protocols

For resource-constrained environments or large clusters, these savings can be substantial.
