#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
target="${1:-}"
case "$target" in android|apple) ;; *) echo 'Usage: scripts/build-mobile.sh android|apple' >&2; exit 1 ;; esac
export PATH="$repo_dir/tools/bin:$PATH"
if [[ ! -x "$repo_dir/tools/bin/gomobile" || ! -x "$repo_dir/tools/bin/gobind" ]]; then
  echo 'Run scripts/install-mobile-tools.sh first.' >&2
  exit 1
fi
mkdir -p dist/mobile
module_path="$(go list -m)"
# This gomobile fork generates ./build packages. Keep them beneath an ignored
# Go directory so concurrent go test ./... cannot discover temporary packages.
# A deterministic directory also prevents random paths in Go build metadata.
mkdir -p "$repo_dir/_build"
build_dir="$repo_dir/_build/mobile-$target"
if ! mkdir "$build_dir"; then
  echo "Another $target build is running, or its build directory needs cleanup: $build_dir" >&2
  exit 1
fi
trap 'rm -rf "$build_dir"' EXIT
cd "$build_dir"
tags="${GO_TAGS-with_quic,with_wireguard,with_utls,with_clash_api}"
link_flags="-s -w -checklinkname=0 -X github.com/HyperPteLtd/libbox/internal/singbox.buildTags=$tags"
case "$target" in
  android)
    gomobile bind -target "${GOMOBILE_TARGET:-android/arm64,android/arm,android/amd64}" \
      -androidapi 24 -javapkg com.hyperpteltd -libname box \
      -tags "$tags" -trimpath -buildvcs=false -ldflags "$link_flags" \
      -o "$repo_dir/dist/mobile/libbox.aar" "$module_path" ;;
  apple)
    gomobile bind -target "${GOMOBILE_TARGET:-ios,iossimulator,macos}" \
      -iosversion 15.0 -macosversion 13.0 -libname box \
      -tags-not-macos=with_low_memory \
      -tags "$tags" -trimpath -buildvcs=false -ldflags "$link_flags" \
      -o "$repo_dir/dist/mobile/Libbox.xcframework" "$module_path" ;;
esac
