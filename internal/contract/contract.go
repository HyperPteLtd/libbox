// Package contract owns internal DTOs and error semantics. Wire messages live in
// protocol/v1; no upstream core type crosses this boundary.
package contract

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

const APIVersion = 1

type Info struct {
	APIVersion   int
	SDKVersion   string
	CoreVersion  string
	BuildTags    string
	OS           string
	Architecture string
	Capabilities []string
}

type Error struct {
	Code    string
	Message string
	cause   error
}

func (e *Error) Error() string        { return "libbox:" + e.Code + ": " + e.Message }
func (e *Error) Unwrap() error        { return e.cause }
func Fail(code, message string) error { return &Error{Code: code, Message: message} }
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	var sdkError *Error
	if errors.As(err, &sdkError) {
		return err
	}
	return &Error{Code: code, Message: err.Error(), cause: err}
}
func ErrorValue(err error) *Error {
	if err == nil {
		return nil
	}
	var value *Error
	if errors.As(err, &value) {
		return &Error{Code: value.Code, Message: value.Message}
	}
	// gomobile turns errors into Java/Objective-C exceptions; preserve the code
	// if a native bridge returns a coded SDK failure as a foreign error value.
	if encoded, ok := strings.CutPrefix(err.Error(), "libbox:"); ok {
		if code, message, found := strings.Cut(encoded, ": "); found && code != "" {
			return &Error{Code: code, Message: message}
		}
	}
	return &Error{Code: "core_error", Message: err.Error()}
}

type Options struct {
	// Private SDK state, selected only through the mode command.
	ModeOverride          string
	WorkingDirectory      string
	TempDirectory         string
	NetworkMode           string
	EventBuffer           int
	UnderNetworkExtension bool
	IncludeAllNetworks    bool
	MemoryProfile         string
	MemoryLimitBytes      int64
	GCPercent             int
}

type Hooks struct {
	OpenTun             func(TunOptions) (int32, error)
	ProtectSocket       func(int32) error
	FindConnectionOwner func(ConnectionRequest) (ConnectionOwner, error)
}

type Network struct {
	DefaultInterface int
	Interfaces       []Interface
	WIFISSID         string
	WIFIBSSID        string
}

type Interface struct {
	Index            int
	Name             string
	MTU              int
	Addresses        []string
	DNSServers       []string
	DNSSearchDomains []string
	Type             string
	Metered          bool
	Constrained      bool
	Loopback         bool
}

func (n Network) Validate() error {
	seen := map[int]bool{}
	names := map[string]bool{}
	for _, i := range n.Interfaces {
		if i.Index <= 0 || i.Name == "" || i.MTU <= 0 || seen[i.Index] || names[i.Name] {
			return Fail("invalid_argument", "interfaces need unique positive indices, names and positive MTUs")
		}
		seen[i.Index], names[i.Name] = true, true
		switch i.Type {
		case "", "other", "wifi", "cellular", "ethernet":
		default:
			return Fail("invalid_argument", "invalid interface type")
		}
		for _, address := range i.Addresses {
			if _, err := netip.ParsePrefix(address); err != nil {
				return Fail("invalid_argument", fmt.Sprintf("invalid interface prefix: %s", address))
			}
		}
		for _, server := range i.DNSServers {
			if _, err := netip.ParseAddr(server); err != nil {
				return Fail("invalid_argument", fmt.Sprintf("invalid DNS server IP: %s", server))
			}
		}
	}
	if n.DefaultInterface != -1 && !seen[n.DefaultInterface] {
		return Fail("invalid_argument", "default_interface must name an interface index, or -1 for offline")
	}
	return nil
}

type TunOptions struct {
	APIVersion      int
	Name            string
	MTU             uint32
	Addresses       []string
	DNSServers      []string
	DNSMode         string
	AutoRoute       bool
	StrictRoute     bool
	Routes          []string
	ExcludeRoutes   []string
	IncludePackages []string
	ExcludePackages []string
	HTTPProxy       *HTTPProxy
}
type HTTPProxy struct {
	Enabled       bool
	Server        string
	Port          uint16
	BypassDomains []string
	MatchDomains  []string
}
type ConnectionRequest struct {
	APIVersion         int
	Protocol           int32
	SourceAddress      string
	SourcePort         int32
	DestinationAddress string
	DestinationPort    int32
}
type ConnectionOwner struct {
	UserID       int32
	ProcessID    uint32
	UserName     string
	ProcessPaths []string
	PackageNames []string
}
type RuntimeStatus struct {
	UploadBytes   int64
	DownloadBytes int64
	Connections   int
	Mode          string
	Modes         []string
	Paused        bool
}
type Status struct {
	APIVersion         int
	State              string
	Generation         uint64
	StartedAt          int64
	DroppedEvents      uint64
	LastError          *Error
	GoMemoryLimitBytes int64
	GoHeapAllocBytes   uint64
	RuntimeStatus
}
type Outbound struct {
	Tag     string
	Type    string
	DelayMS *int32
}
type Group struct {
	Tag        string
	Type       string
	Selected   string
	Selectable bool
	Outbounds  []Outbound
}
type Event struct {
	APIVersion int
	Sequence   uint64
	Time       int64
	Type       string
	Data       any
}
type Log struct {
	Level     string
	Message   string
	Truncated bool
}
type StateChange struct {
	State string
	Error *Error
}
type Emit func(kind string, data any)
