#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$repo_dir/_build/apple-host-tests"
xcrun swiftc \
  "$repo_dir/examples/apple/Shared/AppleHostSupport.swift" \
  "$repo_dir/examples/apple/Shared/AppleRawTunDescriptor.swift" \
  "$repo_dir/examples/apple/Tests/HostSupportTests.swift" \
  -o "$repo_dir/_build/apple-host-tests/tests"
"$repo_dir/_build/apple-host-tests/tests"
