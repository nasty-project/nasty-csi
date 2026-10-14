#!/usr/bin/env bash
# Build/validate a reusable OCI archive without losing the pinned index digest.
set -euo pipefail

reference_file=$1
archive=$2
image=$(tr -d '\n\r' < "$reference_file")
if [[ ! "$image" =~ ^docker\.io/library/busybox@sha256:[a-f0-9]{64}$ ]]; then
  echo "Invalid pinned workload image: $image" >&2
  exit 1
fi

if [[ ! -s "$archive" ]]; then
  # Copy the complete multiarch index so the import retains the exact pod ref.
  # A single-platform/docker-save archive would lose that index digest.
  timeout 5m skopeo copy --all --preserve-digests --retry-times 3 \
    "docker://$image" "oci-archive:$archive"
fi

actual=$(skopeo inspect --raw "oci-archive:$archive" | sha256sum | cut -d ' ' -f1)
if [[ "sha256:$actual" != "${image##*@}" ]]; then
  echo "Workload image archive does not match pinned digest" >&2
  exit 1
fi
