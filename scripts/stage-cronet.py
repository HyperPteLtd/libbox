#!/usr/bin/env python3
"""Bundle the purego Naive runtime selected by go.mod next to the native SDK."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def module_directory(module):
    record = json.loads(subprocess.check_output(
        ["go", "mod", "download", "-json", module], cwd=ROOT, text=True))
    if record.get("Error") or not record.get("Dir"):
        raise RuntimeError(f"Cannot download pinned {module}: {record.get('Error')}")
    if record.get("Replace"):
        raise RuntimeError(f"Refusing replacement runtime for {module}")
    return Path(record["Dir"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("target", choices=["linux_amd64", "linux_arm64", "linux_386",
        "linux_arm", "linux_loong64", "linux_riscv64", "linux_mipsle", "linux_mips64le",
        "linux_amd64_musl", "linux_arm64_musl", "linux_386_musl", "linux_arm_musl",
        "linux_loong64_musl", "linux_riscv64_musl", "linux_mipsle_musl",
        "windows_amd64", "windows_arm64"])
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    filename = "libcronet.dll" if args.target.startswith("windows_") else "libcronet.so"
    source = module_directory("github.com/sagernet/cronet-go/lib/" + args.target) / filename
    license_file = module_directory("github.com/sagernet/cronet-go") / "LICENSE"
    if not source.is_file() or not license_file.is_file():
        raise RuntimeError(f"Pinned Cronet runtime or license is missing for {args.target}")
    args.output.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, args.output / filename)
    shutil.copy2(license_file, args.output / "cronet-LICENSE")
    print(args.output / filename)


if __name__ == "__main__":
    main()
