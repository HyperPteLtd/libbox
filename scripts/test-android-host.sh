#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="$repo_dir/_build/android-host-tests"
mkdir -p "$output"
javac_bin=javac
java_bin=java
if [[ -n "${JAVA_HOME:-}" ]]; then
  javac_bin="$JAVA_HOME/bin/javac"
  java_bin="$JAVA_HOME/bin/java"
fi
"$javac_bin" -d "$output" \
  "$repo_dir/examples/android/app/src/main/java/com/hyperpteltd/libbox/example/OwnedTun.java" \
  "$repo_dir/examples/android/tests/com/hyperpteltd/libbox/example/OwnedTunTest.java"
"$java_bin" -cp "$output" com.hyperpteltd.libbox.example.OwnedTunTest
