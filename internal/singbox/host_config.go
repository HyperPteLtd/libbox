package singbox

import (
	"fmt"
	"github.com/HyperPteLtd/libbox/internal/contract"
)

// These settings bypass the host routing/protection policy in the pinned core.
// Reject them rather than silently accept a configuration whose sockets escape
// the host callback. This policy only applies in network_mode=host.
func checkHostConfig(value any) error {
	switch object := value.(type) {
	case []any:
		for _, child := range object {
			if err := checkHostConfig(child); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, child := range object {
			switch key {
			case "bind_interface", "inet4_bind_address", "inet6_bind_address", "default_interface", "protect_path", "netns":
				if child != nil && child != "" {
					return contract.Fail("unsupported", fmt.Sprintf("%s bypasses host network control", key))
				}
			}
			if err := checkHostConfig(child); err != nil {
				return err
			}
		}
	}
	return nil
}
