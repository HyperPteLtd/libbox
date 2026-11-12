#!/usr/bin/env bash
# Default feature profiles for the pinned sing-box release. Keep SDK and CLI
# entry points in sync here; check-build-tags.py compares them with upstream.
# The mobile SDKs (android, apple) ship HyperVPN's subset instead: naive plus the
# QUIC and uTLS/REALITY outbounds. The mode switch and selectors go through the
# SDK's protobuf control, so the Clash API and server-side features stay out.
set -euo pipefail

mobile_tags='with_quic,with_utls,with_naive_outbound,badlinkname,tfogo_checklinkname0'
common_tags='with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,with_cloudflared,with_usbip,with_openvpn,with_openconnect,badlinkname,tfogo_checklinkname0'
profile="${1:-native}"
case "$profile" in
  cli) tags="$common_tags" ;;
  android|apple) tags="$mobile_tags" ;;
  darwin) tags="$common_tags,with_naive_outbound" ;;
  linux|windows) tags="$common_tags,with_naive_outbound,with_purego" ;;
  native)
    target_os="${GOOS:-$(go env GOOS)}"
    case "$target_os" in
      linux|windows) tags="$common_tags,with_naive_outbound,with_purego" ;;
      darwin) tags="$common_tags,with_naive_outbound" ;;
      ios|android) tags="$mobile_tags" ;;
      *) echo "No default SDK feature profile for $target_os" >&2; exit 1 ;;
    esac ;;
  *) echo 'Usage: build-tags.sh native|cli|android|apple|linux|windows|darwin' >&2; exit 1 ;;
esac
printf '%s\n' "$tags"
