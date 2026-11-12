#!/usr/bin/env bash
# Build the sing-box-compatible CLI (cmd/sing-box) for Linux router CPUs.
#
#   scripts/build-linux-cli.sh [target ...]      default: every target below
#
# Each target becomes dist/cli/libbox-<version>-linux-<target>.tar.gz holding
# `sing-box`, LICENSE and NOTICE; dist/cli/sha256sums lists the archives.
# Binaries are static (CGO_ENABLED=0). Needs GNU tar (Linux, or a Linux
# container on macOS) for reproducible archives.
#
#   target            GOARCH    variant          typical OpenWrt package arch
#   amd64             amd64                      x86_64
#   arm64             arm64                      aarch64_cortex-a53, aarch64_generic, ...
#   armv7             arm       GOARM=7          arm_cortex-a7_neon-vfpv4, arm_cortex-a9_*, ...
#   armv6             arm       GOARM=6          arm_arm1176jzf-s_vfp
#   armv5             arm       GOARM=5          arm_arm926ej-s, arm_xscale, ...
#   mipsle-softfloat  mipsle    GOMIPS=softfloat mipsel_24kc, mipsel_74kc, ...
#   mips-softfloat    mips      GOMIPS=softfloat mips_24kc, mips_4kec, ...
#   386-softfloat     386       GO386=softfloat  i386_pentium-mmx, i386_pentium4
#   riscv64           riscv64                    riscv64_generic
#   loong64           loong64                    loongarch64_generic
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"

all_targets="amd64 arm64 armv7 armv6 armv5 mipsle-softfloat mips-softfloat 386-softfloat riscv64 loong64"
targets="${*:-$all_targets}"

version="$(sed -n 's/^const sdkVersion = "\([^"]*\)"$/\1/p' internal/singbox/runtime.go)"
core="$(sed -n 's/^const pinnedVersion = "v\([^"]*\)"$/\1/p' internal/singbox/runtime.go)"
[[ -n "$version" && -n "$core" ]] || { echo 'cannot read sdkVersion / pinnedVersion from internal/singbox/runtime.go' >&2; exit 1; }
revision="${LIBBOX_REVISION:-$(git rev-parse HEAD 2>/dev/null || true)}"
# The upstream non-Naive feature set, with no gVisor TUN stack. Router archives
# stay self-contained and static; Naive's native runtime belongs to SDK profiles.
tags="${GO_TAGS-$(bash "$repo_dir/scripts/build-tags.sh" cli)}"
# Archive timestamps: the commit time when known, so identical sources give identical archives
mtime="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || echo 0)}"

out_dir="${OUTPUT_DIR:-$repo_dir/dist/cli}"
mkdir -p "$out_dir"

for target in $targets; do
  goarch="" goarm="" gomips="" go386=""
  case "$target" in
    amd64) goarch=amd64 ;;
    arm64) goarch=arm64 ;;
    armv7) goarch=arm goarm=7 ;;
    armv6) goarch=arm goarm=6 ;;
    armv5) goarch=arm goarm=5 ;;
    mipsle-softfloat) goarch=mipsle gomips=softfloat ;;
    mips-softfloat) goarch=mips gomips=softfloat ;;
    386-softfloat) goarch=386 go386=softfloat ;;
    riscv64) goarch=riscv64 ;;
    loong64) goarch=loong64 ;;
    *) echo "unknown target: $target (known: $all_targets)" >&2; exit 1 ;;
  esac

  name="libbox-$version-linux-$target"
  stage="$out_dir/$name"
  rm -rf "$stage"
  mkdir -p "$stage"
  echo "== $name"
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" GOMIPS="$gomips" GO386="$go386" go build \
    -mod=readonly -trimpath -buildvcs=false \
    -tags "$tags" \
    -ldflags "-s -w -buildid= -checklinkname=0 -X runtime.godebugDefault=multipathtcp=0,tlssha1=1 -X github.com/sagernet/sing-box/constant.Version=$core -X main.libboxRevision=$revision -X github.com/HyperPteLtd/libbox/internal/singbox.buildTags=$tags" \
    -o "$stage/sing-box" ./cmd/sing-box
  cp LICENSE NOTICE "$stage/"
  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$mtime" \
    -C "$out_dir" -czf "$out_dir/$name.tar.gz" "$name"
  rm -rf "$stage"
done

(cd "$out_dir" && sha256sum libbox-*-linux-*.tar.gz > sha256sums)
cat "$out_dir/sha256sums"
