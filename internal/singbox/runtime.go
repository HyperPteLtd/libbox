// Package singbox contains every dependency on the upstream core. SDK contracts
// must never expose a type from this package or from an upstream module.
package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	goRuntime "runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HyperPteLtd/libbox/internal/backend"
	"github.com/HyperPteLtd/libbox/internal/contract"
	"github.com/HyperPteLtd/libbox/internal/singbox/loadbalance"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/experimental/clashmode"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/sagernet/sing/service/pause"
)

const pinnedVersion = "v1.15.0-alpha.13"

// SDK release version. Release tags must be "v" + sdkVersion
// (checked by .github/workflows/release.yml); build scripts read it from here.
const sdkVersion = "0.1.1"

// Set by artifact builds, where debug.ReadBuildInfo may be unavailable after
// embedding a Go c-archive in a native executable.
var buildTags string

func BuildInfo() contract.Info {
	version := pinnedVersion
	tags := buildTags
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/sagernet/sing-box" {
				version = dep.Version
				if dep.Replace != nil {
					version = dep.Replace.Path + "@" + dep.Replace.Version
				}
			}
		}
		for _, setting := range info.Settings {
			if setting.Key == "-tags" {
				tags = setting.Value
			}
		}
	}
	capabilities := []string{"lifecycle", "reload_rollback", "events", "traffic", "groups", "url_test", "mode", "pause", "host_network", "host_dns", "socket_protection", "connection_owner", "protobuf_control_v1", "protobuf_host_v1", "memory_profile", "memory_pressure", "loadbalance_group"}
	if lowMemoryBuild {
		capabilities = append(capabilities, "low_memory_build")
		if !strings.Contains(tags, "with_low_memory") {
			tags = strings.Trim(tags+",with_low_memory", ",")
		}
	}
	if goRuntime.GOOS == "windows" {
		capabilities = append(capabilities, "native_wintun")
	}
	if goRuntime.GOOS == "darwin" || goRuntime.GOOS == "ios" || goRuntime.GOOS == "linux" || goRuntime.GOOS == "android" {
		capabilities = append(capabilities, "host_tun")
	}
	if goRuntime.GOOS == "darwin" || goRuntime.GOOS == "ios" {
		capabilities = append(capabilities, "apple_raw_tun_fd")
	}
	return contract.Info{APIVersion: contract.APIVersion, SDKVersion: sdkVersion, CoreVersion: version, BuildTags: tags, OS: goRuntime.GOOS, Architecture: goRuntime.GOARCH, Capabilities: capabilities}
}

type Factory struct{}

var _ backend.Factory = Factory{}

type runtime struct {
	instance  *box.Box
	ctx       context.Context
	cancel    context.CancelFunc
	history   *urltest.HistoryStorage
	traffic   *trafficcontrol.Manager
	mode      *clashmode.Manager
	pause     pause.Manager
	platform  *platform
	control   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

type writer struct{ emit contract.Emit }

func (w writer) WriteMessage(level log.Level, message string) {
	message = strings.ToValidUTF8(message, "\uFFFD")
	truncated := len(message) > 16<<10
	if truncated {
		end := (16 << 10) - 3
		for !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end] + "…"
	}
	w.emit("log", contract.Log{Level: log.FormatLevel(level), Message: message, Truncated: truncated})
}

func (Factory) Check(ctx context.Context, config string, options contract.Options) error {
	core, err := build(ctx, config, options, contract.Hooks{}, contract.Network{DefaultInterface: -1}, func(string, any) {}, true)
	if err != nil {
		return err
	}
	return core.Close()
}
func (Factory) Build(ctx context.Context, config string, options contract.Options, hooks contract.Hooks, network contract.Network, emit contract.Emit) (backend.Runtime, error) {
	core, err := build(ctx, config, options, hooks, network, emit, false)
	if err != nil {
		return nil, err
	}
	return core, nil
}

func build(parent context.Context, config string, options contract.Options, hooks contract.Hooks, network contract.Network, emit contract.Emit, validation bool) (*runtime, error) {
	ctx, cancel := context.WithCancel(parent)
	ctx = filemanager.WithDefault(ctx, options.WorkingDirectory, options.TempDirectory, os.Getuid(), os.Getgid())
	ctx = coreContext(ctx)
	ctx = loadbalance.ContextWithObserver(ctx, func(health loadbalance.Health) {
		emit("group_health", contract.GroupHealth{Group: health.Group, Healthy: health.Healthy, Unhealthy: health.Unhealthy, Fallback: health.Fallback})
	})
	parsed, err := sjson.UnmarshalExtendedContext[option.Options](ctx, []byte(config))
	if err != nil {
		cancel()
		return nil, contract.Wrap("invalid_config", err)
	}
	// An empty/null JSON input should not silently become an empty configuration.
	// sing-box supports extended JSON, so use its parser for this shape check too.
	object, shapeErr := sjson.UnmarshalExtended[map[string]json.RawMessage]([]byte(config))
	if shapeErr != nil || object == nil {
		cancel()
		return nil, contract.Fail("invalid_config", "configuration must be an object")
	}
	defaultMode := "Rule"
	modes := clashmode.CalculateModeList(parsed)
	if parsed.Experimental != nil && parsed.Experimental.ClashAPI != nil {
		api := parsed.Experimental.ClashAPI
		if api.ExternalController != "" {
			cancel()
			return nil, contract.Fail("unsupported", "external Clash controller bypasses the SDK lifecycle; use protobuf control")
		}
		if api.DefaultMode != "" {
			defaultMode = api.DefaultMode
		}
		// Keep the configured default, but do not construct an alternate controller.
		parsed.Experimental.ClashAPI = nil
	}
	for _, entry := range parsed.Services {
		if entry.Type == "api" {
			cancel()
			return nil, contract.Fail("unsupported", "external API service bypasses the SDK lifecycle")
		}
	}
	if !slices.Contains(modes, defaultMode) {
		modes = append([]string{defaultMode}, modes...)
	}
	selectedMode := defaultMode
	for _, mode := range modes {
		if strings.EqualFold(mode, options.ModeOverride) {
			selectedMode = mode
			break
		}
	}
	if options.NetworkMode == "host" {
		value, _ := sjson.UnmarshalExtended[any]([]byte(config))
		if err := checkHostConfig(value); err != nil {
			cancel()
			return nil, err
		}
	}
	if err := preparePlatformOptions(&parsed, options, hooks, validation, goRuntime.GOOS); err != nil {
		cancel()
		return nil, err
	}
	if parsed.Log == nil {
		parsed.Log = &option.LogOptions{Level: "info"}
	}
	parsed.Log.DisableColor = true
	if validation {
		parsed.Log = &option.LogOptions{Disabled: true}
	}
	r := &runtime{ctx: ctx, cancel: cancel, history: urltest.NewHistoryStorage()}
	service.MustRegisterPtr(ctx, r.history)
	if options.NetworkMode == "host" || validation {
		r.platform = newPlatform(options, hooks, network)
		service.MustRegister[adapter.PlatformInterface](ctx, r.platform)
		if options.NetworkMode == "host" {
			registerHostDNS(ctx, r.platform)
		}
	}
	r.instance, err = box.New(box.Options{Context: ctx, Options: parsed, PlatformLogWriter: writer{emit}})
	if err != nil {
		cancel()
		if r.platform != nil {
			r.platform.closeCallbacks()
		}
		_ = r.history.Close()
		return nil, err
	}
	r.traffic = service.PtrFromContext[trafficcontrol.Manager](ctx)
	// Rule items resolve this pointer when Start runs. Publish an immutable mode
	// manager before that point. The upstream manager's SetMode writes a plain
	// string concurrently read by routing; SDK mode changes rebuild the runtime.
	r.mode = clashmode.NewManager(ctx, r.instance.LogFactory().NewLogger("sdk-mode"), selectedMode, modes)
	service.MustRegisterPtr(ctx, r.mode)
	r.pause = service.FromContext[pause.Manager](ctx)
	return r, nil
}

func (r *runtime) Start() error {
	if err := r.instance.Start(); err != nil {
		return err
	}
	if r.platform != nil {
		r.instance.Network().UpdateWIFIState(r.ctx)
	}
	return nil
}
func (r *runtime) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		if r.platform != nil {
			r.platform.closeCallbacks()
		}
		r.closeErr = errors.Join(r.instance.Close(), r.history.Close())
	})
	return r.closeErr
}
func (r *runtime) Status() contract.RuntimeStatus {
	r.control.Lock()
	defer r.control.Unlock()
	status := contract.RuntimeStatus{Modes: []string{}}
	if r.traffic != nil {
		status.UploadBytes, status.DownloadBytes = r.traffic.Total()
		status.Connections = r.traffic.ConnectionsLen()
	}
	if r.mode != nil {
		status.Mode = r.mode.Mode()
		status.Modes = append(status.Modes, r.mode.ModeList()...)
	}
	if r.pause != nil {
		status.Paused = r.pause.IsPaused()
	}
	return status
}
func (r *runtime) outbound(outbound adapter.Outbound) contract.Outbound {
	result := contract.Outbound{Tag: outbound.Tag(), Type: outbound.Type()}
	if history := r.history.LoadURLTestHistory(group.RealTag(outbound, "tcp")); history != nil {
		delay := int32(history.Delay)
		result.DelayMS = &delay
	}
	return result
}
func (r *runtime) Outbounds() []contract.Outbound {
	result := []contract.Outbound{}
	for _, outbound := range r.instance.Outbound().Outbounds() {
		result = append(result, r.outbound(outbound))
	}
	return result
}
func (r *runtime) Groups() []contract.Group {
	result := []contract.Group{}
	for _, outbound := range r.instance.Outbound().Outbounds() {
		g, ok := outbound.(adapter.OutboundGroup)
		if !ok {
			continue
		}
		_, selectable := outbound.(*group.Selector)
		item := contract.Group{Tag: g.Tag(), Type: g.Type(), Selectable: selectable, Outbounds: []contract.Outbound{}}
		if balancer, ok := outbound.(*loadbalance.Group); ok {
			// Its Selected is the site-hashing dialer, not a member.
			item.Selected = balancer.Current()
		} else if selected := g.Selected("tcp"); selected != nil {
			item.Selected = selected.Tag()
		}
		for _, tag := range g.All() {
			if child, ok := r.instance.Outbound().Outbound(tag); ok {
				item.Outbounds = append(item.Outbounds, r.outbound(child))
			}
		}
		result = append(result, item)
	}
	return result
}
func (r *runtime) SelectOutbound(groupTag, outboundTag string) error {
	r.control.Lock()
	defer r.control.Unlock()
	outbound, ok := r.instance.Outbound().Outbound(groupTag)
	if !ok {
		return contract.Fail("not_found", "group not found: "+groupTag)
	}
	selector, ok := outbound.(*group.Selector)
	if !ok {
		return contract.Fail("invalid_argument", "outbound is not a selector: "+groupTag)
	}
	if !selector.SelectOutbound(outboundTag) {
		return contract.Fail("not_found", "outbound is not in selector: "+outboundTag)
	}
	return nil
}
func (r *runtime) URLTest(ctx context.Context, tag, testURL string) (int32, error) {
	outbound, ok := r.instance.Outbound().Outbound(tag)
	if !ok {
		return 0, contract.Fail("not_found", "outbound not found: "+tag)
	}
	realTag := group.RealTag(outbound, "tcp")
	delay, err := urltest.URLTest(ctx, testURL, outbound)
	if err != nil {
		r.history.DeleteURLTestHistory(realTag)
		return 0, err
	}
	r.history.StoreURLTestHistory(realTag, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
	return int32(delay), nil
}
func (r *runtime) CloseConnections() error {
	if r.traffic != nil {
		r.traffic.CloseAllConnections()
	}
	return nil
}
func (r *runtime) SetPaused(paused bool) error {
	if r.pause == nil {
		return contract.Fail("unsupported", "pause control is unavailable")
	}
	if paused {
		r.instance.CloseIdleConnections()
		r.pause.DevicePause()
	} else {
		r.pause.DeviceWake()
	}
	return nil
}
func (r *runtime) UpdateNetwork(network contract.Network) error {
	if r.platform == nil {
		return contract.Fail("unsupported", "host networking is unavailable")
	}
	if err := r.platform.update(network); err != nil {
		return err
	}
	if r.platform.networkReady() {
		r.instance.Network().UpdateWIFIState(r.ctx)
	}
	return nil
}
