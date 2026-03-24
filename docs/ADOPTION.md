# Volume Adoption

Volume adoption allows a new Kubernetes cluster to take ownership of existing subvolumes on NASty. This is useful when you rebuild or replace your cluster but want to keep using the data already on NASty.

## How It Works

Every CSI-created subvolume has xattr metadata (`managed_by`, `csi_volume_name`, `protocol`) stored directly on the bcachefs subvolume. This metadata survives cluster teardowns — it lives on NASty, not in Kubernetes.

When a new cluster creates a PVC that matches an existing subvolume's `csi_volume_name`, the CSI driver:

1. Finds the existing subvolume by scanning xattr properties
2. Verifies it's marked as `adoptable`
3. Re-creates the protocol share (NFS export, SMB share, iSCSI target, or NVMe-oF subsystem)
4. Returns the volume to Kubernetes as if it were freshly created

Data is never copied or moved — the new cluster simply gets a share pointing to the existing subvolume.

## Setup

### 1. Mark volumes as adoptable

In your StorageClass, set `markAdoptable: "true"`. This tells the CSI driver to write an `adoptable=true` xattr on every volume it creates:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-csi-nfs
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  filesystem: first
  server: nasty.example.com
  markAdoptable: "true"
```

Existing volumes that were created without `markAdoptable` can be marked manually on NASty:

```bash
setfattr -n user.nasty-csi:adoptable -v true /fs/first/<subvolume-name>
```

### 2. Enable adoption in the new cluster

In your new cluster's StorageClass, set `adoptExisting: "true"`:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nasty-csi-nfs
provisioner: nasty.csi.io
parameters:
  protocol: nfs
  filesystem: first
  server: nasty.example.com
  adoptExisting: "true"
```

### 3. Create PVCs with the same names

When the CSI driver processes a `CreateVolume` request, it checks if a subvolume with a matching `csi_volume_name` already exists and is marked adoptable. If found, it adopts the volume instead of creating a new one.

For StatefulSet volumes (which have deterministic names like `data-myapp-0`), this happens automatically — the new StatefulSet creates PVCs with the same names as before.

For standalone PVCs, you may need to pre-create PVs with the correct volume handles pointing to the existing subvolumes.

## What gets re-created

Adoption only re-creates the protocol share. The subvolume and its data remain untouched:

| Protocol | What's re-created |
|----------|-------------------|
| NFS | Export in `/etc/exports.d/` |
| SMB | Share config in `/etc/samba/nasty.d/` |
| iSCSI | LIO target + LUN in configfs |
| NVMe-oF | nvmet subsystem + namespace in configfs |

## Metadata stored on subvolumes

These xattrs are written by the CSI driver and used during adoption:

| Property | Purpose |
|----------|---------|
| `nasty-csi:managed_by` | Ownership marker ("nasty-csi") |
| `nasty-csi:csi_volume_name` | PVC name for matching |
| `nasty-csi:protocol` | Which share type to re-create |
| `nasty-csi:adoptable` | Must be "true" for adoption |
| `nasty-csi:capacity_bytes` | Original requested capacity |
| `nasty-csi:delete_strategy` | Preserved across adoption |

View them on NASty with:

```bash
getfattr -d -m "nasty-csi" /fs/first/<subvolume-name>
```
