#!/usr/bin/env python3
"""Remove this checkout's generated build outputs and local tool caches."""
from pathlib import Path
import os
import shutil

ROOT = Path(__file__).resolve().parents[1]
OUTPUTS = (
    "dist", "_build", "build", "tools/bin",
    "examples/android/.gradle", "examples/android/.kotlin",
    "examples/android/build", "examples/android/app/build",
    "examples/apple/LibboxHost.xcodeproj",
    "examples/windows/bin", "examples/windows/obj",
    "examples/windows/Tests/bin", "examples/windows/Tests/obj",
)


def remove(relative):
    path = ROOT / relative
    # Never follow a symlink in a parent component outside this checkout.
    if not path.parent.resolve().is_relative_to(ROOT):
        raise RuntimeError(f"Output parent escapes checkout: {relative}")
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.is_dir():
        shutil.rmtree(path)
    else:
        return
    print(f"Removed {relative}")


def main():
    for relative in OUTPUTS:
        remove(relative)
    for directory, children, _ in os.walk(ROOT, followlinks=False):
        children[:] = [name for name in children if name != ".git"]
        if "__pycache__" in children:
            remove((Path(directory) / "__pycache__").relative_to(ROOT))
            children.remove("__pycache__")


if __name__ == "__main__":
    main()
