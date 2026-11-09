package singbox

import (
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"testing"
)

func tunConfig(count int) option.Options {
	var parsed option.Options
	for range count {
		parsed.Inbounds = append(parsed.Inbounds, option.Inbound{Type: "tun", Options: &option.TunInboundOptions{}})
	}
	return parsed
}

func TestReleaseTUNStacks(t *testing.T) {
	for _, stack := range []string{"", "system", "mixed", "gvisor", "go", "unknown"} {
		parsed := tunConfig(1)
		parsed.Inbounds[0].Options.(*option.TunInboundOptions).Stack = stack
		err := preparePlatformOptions(&parsed, contract.Options{NetworkMode: "host"}, contract.Hooks{}, true, "android")
		allowed := stack == "" || stack == "go" || stack == "system" || tun.WithGVisor && (stack == "mixed" || stack == "gvisor")
		if (err == nil) != allowed {
			t.Fatalf("stack=%s compiled gvisor=%v error=%v", stack, tun.WithGVisor, err)
		}
		parsed = tunConfig(1)
		parsed.Inbounds[0].Options.(*option.TunInboundOptions).Stack = stack
		err = preparePlatformOptions(&parsed, contract.Options{NetworkMode: "host", UnderNetworkExtension: true, IncludeAllNetworks: true}, contract.Hooks{}, true, "ios")
		allowed = stack == "" || stack == "go" || tun.WithGVisor && stack == "gvisor"
		if (err == nil) != allowed {
			t.Fatalf("include_all_networks stack=%s compiled gvisor=%v error=%v", stack, tun.WithGVisor, err)
		}
	}
}
func TestAppleRawTUNPolicy(t *testing.T) {
	for _, goos := range []string{"darwin", "ios"} {
		t.Run(goos, func(t *testing.T) {
			parsed := tunConfig(1)
			if err := preparePlatformOptions(&parsed, contract.Options{NetworkMode: "native"}, contract.Hooks{}, true, goos); err == nil {
				t.Fatal("accepted a core-owned Apple TUN")
			}
			options := contract.Options{NetworkMode: "host", UnderNetworkExtension: true}
			if err := preparePlatformOptions(&parsed, options, contract.Hooks{}, true, goos); err != nil {
				t.Fatal(err)
			}
			if parsed.Route.AutoDetectInterface != (goos == "darwin") {
				t.Fatal("wrong Apple egress policy")
			}
			if err := preparePlatformOptions(&parsed, options, contract.Hooks{}, false, goos); err == nil {
				t.Fatal("missing raw-fd provider accepted")
			}
			parsed = tunConfig(2)
			if err := preparePlatformOptions(&parsed, options, contract.Hooks{}, true, goos); err == nil {
				t.Fatal("multiple TUNs accepted in one NE provider")
			}
			parsed = tunConfig(0)
			if err := preparePlatformOptions(&parsed, options, contract.Hooks{}, true, goos); err == nil {
				t.Fatal("NE accepted a config without TUN")
			}
			parsed = option.Options{}
			if err := preparePlatformOptions(&parsed, contract.Options{NetworkMode: "native"}, contract.Hooks{}, true, goos); err != nil {
				t.Fatal("proxy-only native mode rejected", err)
			}
		})
	}
}
func TestOtherPlatformsKeepTheirTUNOwnership(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		parsed := tunConfig(1)
		if err := preparePlatformOptions(&parsed, contract.Options{NetworkMode: "native"}, contract.Hooks{}, true, goos); err != nil {
			t.Fatal(goos, err)
		}
	}
	parsed := tunConfig(1)
	if err := preparePlatformOptions(&parsed, contract.Options{NetworkMode: "host"}, contract.Hooks{}, true, "android"); err != nil || !parsed.Route.AutoDetectInterface {
		t.Fatal("Android socket protection routing changed", err)
	}
}
