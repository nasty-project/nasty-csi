# Clone Operations Guide

This document explains clone operations in the NASty CSI driver.

## How Cloning Works

bcachefs clones are writable COW (Copy-on-Write) snapshots created with `bcachefs subvolume snapshot` (without the `-r` flag). They are:

- **Instant** -- O(1) creation regardless of data size
- **Space-efficient** -- shares blocks with the source until modified
- **Fully independent** -- no dependency tracking, no promote, no detach
- **Writable** -- immediately usable as a regular volume

There is only one clone mode. The simplicity is the point.

## CSI Operations

### Create Volume from Snapshot (VolumeSnapshot source)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
spec:
  dataSource:
    kind: VolumeSnapshot
    name: my-snapshot
    apiGroup: snapshot.storage.k8s.io
```

The driver creates a writable clone of the snapshot subvolume. Both the snapshot and the clone can be deleted in any order.

### Create Volume from Volume (PVC source)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
spec:
  dataSource:
    kind: PersistentVolumeClaim
    name: source-pvc
```

The driver creates a temporary snapshot of the source volume, then clones it. The temporary snapshot is cleaned up automatically.

## StorageClass Example

No special parameters are needed for cloning. Any StorageClass works:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-nfs
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  pool: tank
  server: nasty.local
```

## Cleanup

Both source and clone can be deleted in any order. There are no dependency constraints -- bcachefs snapshots and clones are independent first-class subvolumes.

## See Also

- [SNAPSHOTS.md](SNAPSHOTS.md) - Snapshot usage guide
