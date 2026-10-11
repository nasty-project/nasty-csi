# Deletion ownership and recovery

An existing subvolume without `nasty-csi:managed_by=nasty-csi` is **not** proof
that deletion already completed. DeleteVolume returns `FailedPrecondition` and
leaves its storage and presentation intact. A query failure returns `Unavailable`
for retry. Only a confirmed not-found response is idempotent success.

Name/protocol conflicts and unknown delete strategies also stop deletion. Legacy
managed volumes remain accepted without requiring the newer identity-version
properties. Do not change `retain` to `delete` merely to clear a failing request.

For missing or inconsistent metadata:

1. Retain the PV/volume handle, original PVC/CSI request identity and storage
   inventory. Check whether any VM, export or another cluster uses the object.
2. Verify the subvolume, sharing protocol, presentation and intended retention
   policy belong to the original request. Back up important data first.
3. Use the existing explicit `adoptExisting=true` CreateVolume workflow only for
   a verified object and its original identity. It is not an automatic DeleteVolume
   repair and can create/recover presentation objects. Review its outcome before
   retrying deletion. Otherwise stop and repair metadata with operator assistance.

Never bulk-label unmanaged subvolumes as CSI-owned or delete them to resolve an
error. There is no automatic ownership adoption in the deletion path.

Creation-time atomic metadata is not provided by this change: a process failure
between backend creation and the subsequent identity write can still leave an
object requiring explicit recovery. This guard prioritizes preserving that data
over silently reporting successful deletion.
