package bastion

import "github.com/xraph/bastion/health"

// WireCallbacks builds the hook engine and health monitor that Register
// creates, then runs wireCallbacks, for the external test package.
func (e *Gateway) WireCallbacks() {
	e.hooks = NewHookEngine()
	e.healthMon = health.NewMonitor(health.Config{}, nil)
	e.wireCallbacks()
}
