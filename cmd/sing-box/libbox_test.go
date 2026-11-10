package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HyperPteLtd/libbox/internal/singbox"
	C "github.com/sagernet/sing-box/constant"
)

func useConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	configPaths = []string{path}
	configDirectories = nil
	globalCtx = singbox.CoreContext(context.Background())
}

const loadbalanceConfig = `{
  "log": {"disabled": true},
  "outbounds": [
    {"type": "direct", "tag": "jp-1"},
    {"type": "direct", "tag": "jp-2"},
    {"type": "loadbalance", "tag": "JP", "outbounds": ["jp-1", "jp-2"], "strategy": "%s"}
  ],
  "route": {"final": "JP"}
}`

// The CLI must accept the SDK's loadbalance group, which upstream sing-box does not know.
func TestCheckAcceptsLoadbalance(t *testing.T) {
	useConfig(t, strings.Replace(loadbalanceConfig, "%s", "consistent-hashing", 1))
	if err := check(); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// ...and validate it with the SDK's rules, not merely parse it.
func TestCheckRejectsInvalidLoadbalance(t *testing.T) {
	useConfig(t, strings.Replace(loadbalanceConfig, "%s", "round-robin", 1))
	if err := check(); err == nil {
		t.Fatal("check accepted an unsupported loadbalance strategy")
	}
}

func TestCheckRejectsUnknownOutbound(t *testing.T) {
	useConfig(t, `{"outbounds": [{"type": "no-such-type", "tag": "x"}]}`)
	if err := check(); err == nil {
		t.Fatal("check accepted an unknown outbound type")
	}
}

// OpenWrt passwall parses `^sing-box version (\S+)` and the "Tags:" line, and
// compares the version with ">= 1.14.0" / ">= 1.15.0" gates.
func TestVersionOutput(t *testing.T) {
	want := strings.TrimPrefix(singbox.BuildInfo().CoreVersion, "v")
	if C.Version != want {
		t.Fatalf("constant.Version = %q, want %q from the module pin", C.Version, want)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = write
	nameOnly = false
	printVersion(nil, nil)
	os.Stdout = stdout
	write.Close()
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}

	text := string(output)
	if m := regexp.MustCompile(`^sing-box version (\S+)\n`).FindStringSubmatch(text); m == nil || m[1] != want {
		t.Fatalf("first line is not `sing-box version %s`:\n%s", want, text)
	}
	if !strings.Contains(text, "\nlibbox: "+singbox.BuildInfo().SDKVersion+"\n") {
		t.Fatalf("missing libbox line:\n%s", text)
	}
	if !strings.Contains(text, "\nCGO: ") {
		t.Fatalf("missing CGO line:\n%s", text)
	}
}
