package plugin

import (
	"sync"

	bastion "github.com/xraph/bastion"
)

// Manager registers and invokes gateway plugins.
// It integrates with the HookEngine to participate in the proxy pipeline.
type Manager struct {
	mu      sync.RWMutex
	plugins []bastion.GatewayPlugin
}

// NewManager creates a new plugin manager.
func NewManager() *Manager {
	return &Manager{}
}

// Register adds a plugin. It also wires the plugin into the provided
// HookEngine so existing hook-based callers invoke plugin methods.
func (pm *Manager) Register(plugin bastion.GatewayPlugin, hooks *bastion.HookEngine) {
	pm.mu.Lock()
	pm.plugins = append(pm.plugins, plugin)
	pm.mu.Unlock()

	hooks.OnRequest(plugin.OnRequest)
	hooks.OnResponse(plugin.OnResponse)
	hooks.OnError(plugin.OnError)
}

// Plugins returns a snapshot of all registered plugins.
func (pm *Manager) Plugins() []bastion.GatewayPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]bastion.GatewayPlugin, len(pm.plugins))
	copy(out, pm.plugins)

	return out
}

// PluginNames returns the names of all registered plugins.
func (pm *Manager) PluginNames() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	names := make([]string, len(pm.plugins))
	for i, p := range pm.plugins {
		names[i] = p.Name()
	}

	return names
}
