"""Compare statement coverage per package using same-platform Go profiles."""

import argparse
from collections import defaultdict
from pathlib import Path


def package_coverage(text):
    totals = defaultdict(lambda: [0, 0])
    for line in text.splitlines()[1:]:
        location, statements, count = line.rsplit(" ", 2)
        package = location.split(":", 1)[0].rsplit("/", 1)[0]
        statements, count = int(statements), int(count)
        totals[package][0] += statements
        if count > 0:
            totals[package][1] += statements
    return {package: 100 * covered / total for package, (total, covered) in totals.items() if total}


def validate(current, baseline, tolerance=0.5):
    if not current or not baseline:
        raise ValueError("empty coverage cannot establish a ratchet")
    for package, previous in baseline.items():
        actual = current.get(package)
        if actual is None or actual + tolerance < previous:
            raise ValueError(f"coverage decreased for {package}: {previous:.2f}% -> {actual}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("current")
    parser.add_argument("baseline")
    args = parser.parse_args()
    current = package_coverage(Path(args.current).read_text())
    baseline = package_coverage(Path(args.baseline).read_text())
    validate(current, baseline)
    for package, coverage in sorted(current.items()):
        print(f"{package}: {coverage:.2f}%")


if __name__ == "__main__":
    main()
