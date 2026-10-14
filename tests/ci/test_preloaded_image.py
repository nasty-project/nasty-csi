"""Archive/preload regressions; fake tools only, no cluster or registry access."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / ".github/actions/qemu-vm/prepare-test-image.sh"


class PreloadTests(unittest.TestCase):
    def run_fixture(self, cached=False, corrupt=False, unpinned=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = b'{"schemaVersion":2}\n'
            digest = hashlib.sha256(raw).hexdigest()
            reference = root / "image.txt"
            reference.write_text("docker.io/library/busybox" + (":1.37.0" if unpinned else "@sha256:" + digest) + "\n")
            archive = root / "image.tar"
            if cached:
                archive.write_bytes(b"wrong\n" if corrupt else raw)
            # Copy emits the fixture index; inspect returns the saved index bytes.
            (root / "skopeo").write_text('#!/bin/sh\nif [ "$1" = copy ]; then\n  echo copy >> "$COPY_LOG"\n  for arg do last="$arg"; done\n  printf \'{"schemaVersion":2}\\n\' > "${last#oci-archive:}"\nelse\n  cat "${3#oci-archive:}"\nfi\n')
            (root / "timeout").write_text('#!/bin/sh\nshift\nexec "$@"\n')
            for name in ["skopeo", "timeout"]:
                (root / name).chmod(0o700)
            log = root / "copies"
            result = subprocess.run(["bash", str(SCRIPT), str(reference), str(archive)],
                                    env={**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "COPY_LOG": str(log)},
                                    capture_output=True, text=True, check=False)
            return result, log.exists()

    def test_cold_archive_is_copied_and_verified(self):
        result, copied = self.run_fixture()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(copied)

    def test_cache_hit_needs_no_registry_copy(self):
        result, copied = self.run_fixture(cached=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(copied)

    def test_corrupt_cache_fails_closed(self):
        result, copied = self.run_fixture(cached=True, corrupt=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(copied)

    def test_mutable_reference_is_rejected(self):
        result, copied = self.run_fixture(unpinned=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(copied)

    def test_all_e2e_workloads_use_shared_pin(self):
        for path in (ROOT / "tests/e2e").rglob("*.go"):
            self.assertNotIn("busybox:latest", path.read_text(), str(path))
            self.assertNotIn("busybox:stable", path.read_text(), str(path))
        action = (ROOT / ".github/actions/qemu-vm/action.yml").read_text()
        self.assertIn("--image-pull-policy=Never", action)
        self.assertIn("--base-name '${IMAGE%@*}'", action)
        self.assertIn("$3 == digest", action)
        framework = (ROOT / "tests/e2e/framework/kubernetes.go").read_text()
        self.assertIn("imagePullPolicy: IfNotPresent", framework)
