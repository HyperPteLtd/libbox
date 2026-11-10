package main

import (
	"strings"

	"github.com/HyperPteLtd/libbox/internal/singbox"
	C "github.com/sagernet/sing-box/constant"
)

// This command is sing-box's own CLI (cmd/sing-box of the pinned release),
// run with the SDK's registries so configurations may use the loadbalance
// outbound group. Changes from upstream are limited to:
//   - cmd.go: singbox.CoreContext replaces include.Context
//   - cmd_version.go: Revision falls back to libboxRevision; a "libbox:" line
//   - this file
// Hosts such as OpenWrt passwall read "sing-box version <x>" and "Tags:" from
// `version`, so those lines keep the upstream format.

// Set by scripts/build-linux-cli.sh (-X main.libboxRevision=<commit>).
var libboxRevision string

func init() {
	// Artifact builds also set this with -X; fall back to the module pin.
	if C.Version == "unknown" {
		C.Version = strings.TrimPrefix(singbox.BuildInfo().CoreVersion, "v")
	}
}
