"""Release must not bypass the pinned SDK/toolchain and validated safety gates."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ReleaseWorkflowTests(unittest.TestCase):
    def test_tool_versions_match_ci(self):
        release = (ROOT / ".github/workflows/release.yml").read_text()
        ci = (ROOT / ".github/workflows/ci.yml").read_text()
        for name in ["GO_VERSION", "GOLANGCI_LINT_VERSION"]:
            pattern = rf"^  {name}: (.+)$"
            self.assertEqual(re.search(pattern, release, re.MULTILINE).group(1),
                             re.search(pattern, ci, re.MULTILINE).group(1))

    def test_sdk_is_resolved_from_modules_not_a_checkout(self):
        release = (ROOT / ".github/workflows/release.yml").read_text()
        self.assertNotIn("repository: nasty-project/nasty-go", release)
        self.assertIn("{{if .Replace}}", release)
        self.assertIn("go mod verify", release)
        self.assertIn("go mod tidy -diff", release)

    def test_validation_precedes_registry_login_and_publication(self):
        release = (ROOT / ".github/workflows/release.yml").read_text()
        publication = release.index("- name: Log in to Docker Hub")
        for gate in ["git merge-base --is-ancestor", "make test-unit", "go test -race ./tests/e2e/framework",
                     "unittest discover -s tests/ci", "make test-sanity", "check_nolint_budget.py",
                     "govulncheck@v1.9.0", "- name: Run linter"]:
            self.assertLess(release.index(gate), publication, gate)
