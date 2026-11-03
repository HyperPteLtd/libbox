#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "$(go env GOHOSTOS)" == windows ]]; then
  compiler="${WINDOWS_CC:-gcc}"
else
  compiler="${WINDOWS_CC:-x86_64-w64-mingw32-gcc}"
fi
if ! command -v "$compiler" >/dev/null; then
  echo 'A Windows x64 MinGW-w64 compiler is required (CC or x86_64-w64-mingw32-gcc).' >&2
  exit 1
fi
GOOS=windows GOARCH=amd64 CC="$compiler" bash "$repo_dir/scripts/build-cshared.sh"
