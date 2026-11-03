#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$repo_dir/tools/bin"
GOBIN="$repo_dir/tools/bin" go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
GOBIN="$repo_dir/tools/bin" go install github.com/sagernet/gomobile/cmd/gobind@v0.1.13
