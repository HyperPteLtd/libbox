package singbox

import (
	"os"
	"regexp"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestCoreUsesExactRelease(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	const module = "github.com/sagernet/sing-box"
	for _, replacement := range file.Replace {
		if replacement.Old.Path == module {
			t.Fatal("sing-box must resolve to its upstream release, without a replace")
		}
	}
	for _, requirement := range file.Require {
		if requirement.Mod.Path != module {
			continue
		}
		if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:alpha|beta|rc)\.[0-9]+)?$`).MatchString(requirement.Mod.Version) {
			t.Fatal("sing-box must use an exact release tag, not a commit pseudo-version")
		}
		if requirement.Mod.Version != pinnedVersion || BuildInfo().CoreVersion != pinnedVersion {
			t.Fatal("core version metadata differs from the module pin")
		}
		return
	}
	t.Fatal("missing sing-box release requirement")
}
