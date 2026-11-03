package singbox

import (
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/x/list"
	"sync"
)

type monitor struct {
	platform  *platform
	mu        sync.Mutex
	callbacks list.List[tun.DefaultInterfaceUpdateCallback]
	names     []string
	closed    bool
}

func (m *monitor) Start() error {
	m.platform.mu.Lock()
	m.platform.monitorStarted = true
	m.platform.mu.Unlock()
	if m.platform.manager != nil {
		if err := m.platform.manager.UpdateInterfaces(); err != nil {
			m.platform.mu.Lock()
			m.platform.monitorStarted = false
			m.platform.mu.Unlock()
			return err
		}
	}
	return nil
}
func (m *monitor) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.platform.mu.Lock()
	m.platform.monitorStarted = false
	m.platform.mu.Unlock()
	return nil
}
func (m *monitor) DefaultInterface() *control.Interface {
	if i := m.platform.defaultInterface(); i != nil {
		return &i.Interface
	}
	return nil
}
func (*monitor) OverrideAndroidVPN() bool { return false }
func (*monitor) AndroidVPNEnabled() bool  { return false }
func (m *monitor) RegisterCallback(cb tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callbacks.PushBack(cb)
}
func (m *monitor) UnregisterCallback(element *list.Element[tun.DefaultInterfaceUpdateCallback]) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks.Remove(element)
}
func (m *monitor) RegisterMyInterface(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.names = append(m.names, name)
}
func (m *monitor) MyInterfaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.names...)
}
func (m *monitor) notify() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	callbacks := m.callbacks.Array()
	m.mu.Unlock()
	for _, callback := range callbacks {
		callback(m.DefaultInterface(), 0)
	}
}
