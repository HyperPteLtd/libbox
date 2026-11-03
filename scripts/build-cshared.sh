#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
target_os="${GOOS:-$(go env GOHOSTOS)}"
target_arch="${GOARCH:-$(go env GOHOSTARCH)}"
build_mode="${BUILD_MODE:-c-shared}"
case "$build_mode" in
  c-archive) extension=a ;;
  c-shared)
    case "$target_os" in darwin) extension=dylib ;; windows) extension=dll ;; *) extension=so ;; esac ;;
  *) echo 'BUILD_MODE must be c-shared or c-archive' >&2; exit 1 ;;
esac
output_dir="${OUTPUT_DIR:-$repo_dir/dist/$target_os-$target_arch}"
tags="${GO_TAGS-with_quic,with_wireguard,with_utls,with_clash_api}"
mkdir -p "$output_dir"
CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" go build \
  -mod=readonly -trimpath -buildvcs=false \
  -tags "$tags" \
  -ldflags "-s -w -checklinkname=0 -X github.com/HyperPteLtd/libbox/internal/singbox.buildTags=$tags" -buildmode="$build_mode" \
  -o "$output_dir/libbox.$extension" ./cmd/libboxc
# Install the hand-maintained, versioned ABI, not cgo's implementation header.
cp include/libbox.h "$output_dir/libbox.h"
echo "$output_dir/libbox.$extension"
