#!/usr/bin/env python3
"""Verify default feature profiles against the pinned sing-box release lists."""
import argparse
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def tags(value):
    result = value.strip().split(",")
    if not all(result) or len(result) != len(set(result)):
        raise RuntimeError(f"Empty or duplicate build tags: {value}")
    return set(result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream-dir", type=Path, help="already downloaded pinned source tree")
    args = parser.parse_args()
    upstream = args.upstream_dir
    if upstream is None:
        record = json.loads(subprocess.check_output(
            ["go", "mod", "download", "-json", "github.com/sagernet/sing-box"], cwd=ROOT, text=True))
        if record.get("Error") or not record.get("Dir"):
            raise RuntimeError("Cannot read the pinned sing-box source")
        upstream = Path(record["Dir"])
    release = upstream / "release"
    standard = tags((release / "DEFAULT_BUILD_TAGS").read_text())
    portable = tags((release / "DEFAULT_BUILD_TAGS_OTHERS").read_text())
    windows = tags((release / "DEFAULT_BUILD_TAGS_WINDOWS").read_text())
    expected = {"cli": portable, "android": standard, "apple": standard,
                "darwin": standard, "linux": standard | {"with_purego"}, "windows": windows}
    for profile, want in expected.items():
        actual = tags(subprocess.check_output(
            ["bash", ROOT / "scripts/build-tags.sh", profile], cwd=ROOT, text=True))
        if actual != want:
            raise RuntimeError(f"{profile}: missing {want-actual}, extra {actual-want}")
        if profile in ("linux", "windows", "darwin"):
            environment = dict(os.environ, GOOS=profile)
            native = tags(subprocess.check_output(
                ["bash", ROOT / "scripts/build-tags.sh", "native"], cwd=ROOT, env=environment, text=True))
            if native != actual:
                raise RuntimeError(f"native profile differs on {profile}")
        print(f"{profile}: {len(actual)} tags match the release feature set")


if __name__ == "__main__":
    main()
