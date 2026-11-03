#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="$repo_dir/_build/swift-protobuf"
if [[ ! -d "$source_dir/.git" ]]; then
  git clone --depth 1 --branch 1.32.0 https://github.com/apple/swift-protobuf.git "$source_dir"
fi
if [[ "$(git -C "$source_dir" rev-parse HEAD)" != c6fe6442e6a64250495669325044052e113e990c ]]; then
  echo 'Unexpected swift-protobuf revision; use the pinned 1.32.0 source.' >&2; exit 1
fi
PROTOBUF_NO_PROTOC=true swift build --package-path "$source_dir" -c release --product protoc-gen-swift
