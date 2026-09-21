#!/usr/bin/env python3
"""Gate Go statement coverage for blocks intersecting a production-code diff.

Uses only Python's standard library and Git. Tests, benchmarks, and scripts
are excluded; all other changed Go files, including untracked files, count.
A block's statements are counted once, even when several changed lines touch it.
"""

import argparse
from collections import defaultdict
from pathlib import Path
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(["git", *args], text=True)


def production(path):
    return path.endswith(".go") and not path.endswith("_test.go") and not path.startswith("scripts/")


def changed_lines(diff):
    lines = set()
    for start, count in re.findall(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", diff, re.M):
        start, count = int(start), int(count) if count else 1
        lines.update(range(start, start + count))
    return lines


def read_profile(profile, module):
    blocks = defaultdict(dict)
    for line in profile.splitlines():
        if line.startswith("mode:"):
            continue
        match = re.fullmatch(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)", line)
        if not match:
            raise ValueError(f"Invalid coverage record: {line}")
        path, start, col, end, endcol, statements, count = match.groups()
        prefix = module + "/"
        if not path.startswith(prefix):
            continue
        key = tuple(map(int, (start, col, end, endcol, statements)))
        path = path[len(prefix):]
        blocks[path][key] = blocks[path].get(key, False) or int(count) > 0
    return blocks


def measure(blocks, changed):
    covered = total = 0
    missing = []
    for (start, _, end, _, statements), hit in sorted(blocks.items()):
        if statements and any(line in changed for line in range(start, end + 1)):
            total += statements
            if hit:
                covered += statements
            else:
                missing.append(f"{start}-{end}")
    return covered, total, missing


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="Base commit for the full contribution")
    parser.add_argument("--profile", required=True, type=Path)
    parser.add_argument("--minimum", type=float, default=95)
    args = parser.parse_args()
    if not 0 <= args.minimum <= 100:
        parser.error("--minimum must be between 0 and 100")
    module = re.search(r"^module\s+(\S+)", Path("go.mod").read_text(), re.M).group(1)
    blocks = read_profile(args.profile.read_text(), module)
    tracked = set(filter(None, git("diff", "--name-only", "--diff-filter=ACMR", args.base, "--", "*.go").splitlines()))
    untracked = set(filter(None, git("ls-files", "--others", "--exclude-standard", "--", "*.go").splitlines()))
    covered = total = 0
    incomplete = False
    for path in sorted(filter(production, tracked | untracked)):
        source = Path(path).read_text()
        if path in untracked:
            changed = set(range(1, len(source.splitlines()) + 1))
        else:
            changed = changed_lines(git("diff", "--no-ext-diff", "--unified=0", args.base, "--", path))
        if not changed:
            continue
        if path not in blocks and re.search(r"^func\s", source, re.M):
            print(f"{path}: missing from coverage profile", file=sys.stderr)
            incomplete = True
            continue
        hit, count, missing = measure(blocks.get(path, {}), changed)
        covered += hit
        total += count
        if count:
            print(f"{path}: {hit}/{count} ({100 * hit / count:.2f}%)")
        if missing:
            print(f"  uncovered lines: {', '.join(missing)}")
    if incomplete:
        return 1
    if total == 0:
        print("No changed executable Go statements found.")
        return 0
    print(f"TOTAL: {covered}/{total} ({100 * covered / total:.2f}%); minimum {args.minimum:g}%")
    return int(100 * covered < args.minimum * total)


if __name__ == "__main__":
    sys.exit(main())
