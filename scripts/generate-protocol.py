#!/usr/bin/env python3
"""Generate bindings using pinned protoc and the Go module's protobuf runtime.

The toolchain lock is adopted from the reference desktop core. Generated files
are checked in; --check verifies them without rewriting source files.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
LOCK = json.loads((ROOT / "proto/toolchain.json").read_text())
TOOLS = ROOT / "_build/protobuf-tools"

def protoc():
    arch = {"aarch64":"arm64", "AMD64":"x86_64"}.get(platform.machine(), platform.machine())
    key = f"{sys.platform}-{arch}"
    record = LOCK["protoc_archives"][key]
    TOOLS.mkdir(parents=True, exist_ok=True)
    archive = TOOLS / record["name"]
    if not archive.exists() or hashlib.sha256(archive.read_bytes()).hexdigest() != record["sha256"]:
        url = f"https://github.com/protocolbuffers/protobuf/releases/download/v{LOCK['protoc_version']}/{record['name']}"
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(64 * 1024 * 1024 + 1)
        if hashlib.sha256(data).hexdigest() != record["sha256"]:
            raise RuntimeError("protoc archive checksum mismatch")
        archive.write_bytes(data)
    directory = TOOLS / f"protoc-{key}"
    with zipfile.ZipFile(archive) as zipped:
        for entry in zipped.namelist():
            if not (directory / entry).resolve().is_relative_to(directory.resolve()):
                raise RuntimeError("unsafe archive path")
        zipped.extractall(directory)
    binary = directory / "bin" / ("protoc.exe" if sys.platform == "win32" else "protoc")
    binary.chmod(0o755)
    if subprocess.check_output([binary, "--version"], text=True).strip() != "libprotoc " + LOCK["protoc_version"]:
        raise RuntimeError("wrong protoc version")
    return binary

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--swift", action="store_true", help="also generate pinned SwiftProtobuf bindings")
    args = parser.parse_args()
    compiler = protoc()
    plugin = TOOLS / ("protoc-gen-go.exe" if sys.platform == "win32" else "protoc-gen-go")
    subprocess.run(["go", "build", "-mod=readonly", "-o", plugin, "google.golang.org/protobuf/cmd/protoc-gen-go"], cwd=ROOT, check=True)
    with tempfile.TemporaryDirectory(prefix="libbox-proto-") as temp:
        dest = Path(temp)
        for name in ["go", "csharp", "java", "python", "descriptor"]:
            (dest/name).mkdir()
        subprocess.run([compiler, f"-I{ROOT/'proto'}", f"--plugin=protoc-gen-go={plugin}", f"--go_out={dest/'go'}", "--go_opt=module=github.com/HyperPteLtd/libbox", f"--csharp_out={dest/'csharp'}", f"--java_out=lite:{dest/'java'}", f"--python_out={dest/'python'}", f"--descriptor_set_out={dest/'descriptor/control.pb'}", "--include_imports", ROOT/'proto/libbox/v1/control.proto'], check=True)
        outputs = {}
        if args.swift:
            swift_plugin = ROOT / '_build/swift-protobuf/.build/release/protoc-gen-swift'
            if not swift_plugin.is_file():
                raise RuntimeError('Run scripts/install-swift-protobuf.sh first')
            (dest/'swift').mkdir()
            subprocess.run([compiler, f"-I{ROOT/'proto'}", f"--plugin=protoc-gen-swift={swift_plugin}", f"--swift_out={dest/'swift'}", '--swift_opt=Visibility=Public', ROOT/'proto/libbox/v1/control.proto'], check=True)
        destinations = {"go": ROOT, "csharp": ROOT/'examples/windows/Generated', "java": ROOT/'examples/android/app/src/main/java', "python": ROOT/'tools/protocol', "descriptor": ROOT/'proto'}
        if args.swift:
            destinations['swift'] = ROOT/'examples/apple/Generated'
        for kind, target in destinations.items():
            for source in (dest/kind).rglob('*'):
                if source.is_file():
                    outputs[target/source.relative_to(dest/kind)] = source.read_bytes()
        for path, data in outputs.items():
            if args.check:
                if not path.exists() or path.read_bytes() != data:
                    raise RuntimeError(f"stale generated binding: {path.relative_to(ROOT)}")
            else:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data)
    print("Protocol bindings verified" if args.check else "Protocol bindings generated")

if __name__ == '__main__':
    main()
