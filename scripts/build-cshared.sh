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
tags="${GO_TAGS-$(GOOS="$target_os" bash "$repo_dir/scripts/build-tags.sh" native)}"
mkdir -p "$output_dir"
CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" go build \
  -mod=readonly -trimpath -buildvcs=false \
  -tags "$tags" \
  -ldflags "-s -w -checklinkname=0 -X github.com/HyperPteLtd/libbox/internal/singbox.buildTags=$tags" -buildmode="$build_mode" \
  -o "$output_dir/libbox.$extension" ./cmd/libboxc
# Install the hand-maintained, versioned ABI, not cgo's implementation header.
cp include/libbox.h "$output_dir/libbox.h"
if [[ ",$tags," == *,with_naive_outbound,* && ",$tags," == *,with_purego,* ]]; then
  cronet_target="${target_os}_${target_arch}"
  if [[ "$target_os" == linux && ",$tags," == *,with_musl,* ]]; then
    cronet_target="${cronet_target}_musl"
  fi
  python_bin="${PYTHON:-python3}"
  if [[ "$(go env GOHOSTOS)" == windows ]]; then python_bin="${PYTHON:-python}"; fi
  "$python_bin" "$repo_dir/scripts/stage-cronet.py" "$cronet_target" "$output_dir"
fi
echo "$output_dir/libbox.$extension"
