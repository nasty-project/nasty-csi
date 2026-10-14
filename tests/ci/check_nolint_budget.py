"""An explicit total suppression budget that can only shrink against main."""

import argparse
import json
from pathlib import Path
import re
import subprocess


def count_suppressions(text):
    directives = re.findall(r"//nolint(?::([\w,]+))?(?=\s|$)", text)
    if any(not names or "all" in names.split(",") for names in directives):
        raise ValueError("blanket lint suppressions are not allowed")
    return sum(len(names.split(",")) if names else 1 for names in directives)


def validate(actual, budget, baseline=None):
    if not isinstance(budget, int) or isinstance(budget, bool) or budget < 0:
        raise ValueError("invalid suppression budget")
    if actual != budget:
        raise ValueError(f"suppression count {actual} differs from budget {budget}; remove additions or lower the budget with removals")
    if baseline is not None and budget > baseline:
        raise ValueError(f"suppression budget grew from {baseline} to {budget}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--baseline-ref")
    args = parser.parse_args()
    files = subprocess.check_output(["git", "ls-files", "-z", "*.go"]).decode().split("\0")
    actual = sum(count_suppressions(Path(path).read_text()) for path in files if path)
    budget = json.loads(Path("nolint-budget.json").read_text())["total"]
    baseline = None
    if args.baseline_ref:
        # Missing baseline file is allowed only for this gate's first addition;
        # an invalid/missing ref or malformed existing file must fail.
        subprocess.run(["git", "rev-parse", "--verify", args.baseline_ref], check=True, stdout=subprocess.DEVNULL)
        listed = subprocess.check_output(["git", "ls-tree", "--name-only", args.baseline_ref, "nolint-budget.json"])
        if listed:
            baseline = json.loads(subprocess.check_output(["git", "show", f"{args.baseline_ref}:nolint-budget.json"]))["total"]
    validate(actual, budget, baseline)
    print(f"Suppression budget: {actual}/{budget}")


if __name__ == "__main__":
    main()
