# E2E workload image

`tests/e2e/framework/test-image.txt` is the shared BusyBox 1.37.0 multiarch
index pin for Go E2E pods, StatefulSets and multi-protocol mount pods. The default
workloads use `IfNotPresent`, not a mutable `latest` image with an implicit
`Always` pull policy.

The shared QEMU action used by all five integration jobs:

1. Restores an OCI archive cached by the exact image-reference file hash.
2. On a cold cache, downloads the pinned index and its platforms once using
   Skopeo, preserving digests, with bounded retries and a five-minute timeout.
3. Verifies the archive index against the pin even on cache hits.
4. Imports into each VM's K3S containerd store, registering the exact index
   reference. Non-native platforms are not unpacked during import.
5. Runs a short, storage-free pod with `imagePullPolicy: Never` to prove the
   imported image is usable without a registry request before storage tests.

Preflight failure aborts setup with pod diagnostics instead of letting an entire
storage suite stall on image pulls. Cold-cache downloads still need registry
availability; this is not a promise of completely offline VM provisioning.
Sidecars, snapshot controller and K3S dependencies are unchanged. Custom workload
image overrides and non-QEMU runs are not automatically preloaded.

To update BusyBox, resolve and review the new multiarch index digest, update the
one reference file, and run the framework/Python regressions and a QEMU integration
run. Changing the pin automatically selects a new archive cache key. The older
manual manifests under `tests/integration/manifests` are not used by these Go
E2E jobs and are outside this change.
