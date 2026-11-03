#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
if [[ ! -d dist/mobile/Libbox.xcframework ]]; then
  echo 'Build the Apple SDK first: bash scripts/build-mobile.sh apple' >&2; exit 1
fi
xcodegen generate --spec examples/apple/project.yml
# Preserve obsolete products outside the app instead of deleting them.
retire_product() {
  if [[ -e "$1" || -L "$1" ]]; then
    mkdir -p "$repo_dir/_build/retired-apple-products"
    retired_dir="$(mktemp -d "$repo_dir/_build/retired-apple-products/product.XXXXXX")"
    mv "$1" "$retired_dir/"
  fi
}
retire_product "$repo_dir/dist/examples/apple/Build/Products/Debug/MacHost"
retire_product "$repo_dir/dist/examples/apple/Build/Products/Debug/MacHost.app/Contents/Library/SystemExtensions/MacPacketTunnel.systemextension"
retire_product "$repo_dir/dist/examples/apple/Build/Products/Debug/MacPacketTunnel.systemextension"
# Compile only: no simulator boot, app launch, extension activation or VPN setup.
PROTOBUF_NO_PROTOC=true xcodebuild -project examples/apple/LibboxHost.xcodeproj \
  -scheme LibboxHost -sdk iphoneos -destination 'generic/platform=iOS' \
  -derivedDataPath "$repo_dir/dist/examples/apple" CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO build
PROTOBUF_NO_PROTOC=true xcodebuild -project examples/apple/LibboxHost.xcodeproj \
  -scheme MacHost -destination 'generic/platform=macOS' \
  -derivedDataPath "$repo_dir/dist/examples/apple" CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO build
python3 "$repo_dir/scripts/verify-apple-bundles.py"
