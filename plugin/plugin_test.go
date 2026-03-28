package plugin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	bastion "github.com/xraph/bastion"
)

type testPlugin struct {
	bastion.BasePlugin
	requestCalled  bool
	responseCalled bool
	errorCalled    bool
	rejectRequest  bool
}

func (tp *testPlugin) OnRequest(r *http.Request, route *bastion.Route) error {
	tp.requestCalled = true
	if tp.rejectRequest {
		return errors.New("rejected by test plugin")
	}

	return nil
}

func (tp *testPlugin) OnResponse(resp *http.Response, route *bastion.Route) {
	tp.responseCalled = true
}

func (tp *testPlugin) OnError(err error, route *bastion.Route, w http.ResponseWriter) {
	tp.errorCalled = true
}

func TestPluginManager_Register(t *testing.T) {
	pm := NewManager()
	hooks := bastion.NewHookEngine()

	p := &testPlugin{BasePlugin: bastion.BasePlugin{PluginName: "test"}}
	pm.Register(p, hooks)

	names := pm.PluginNames()
	if len(names) != 1 || names[0] != "test" {
		t.Errorf("expected [test], got %v", names)
	}
}

func TestPluginManager_HookIntegration(t *testing.T) {
	pm := NewManager()
	hooks := bastion.NewHookEngine()

	p := &testPlugin{BasePlugin: bastion.BasePlugin{PluginName: "test"}}
	pm.Register(p, hooks)

	route := &bastion.Route{ID: "r1", Path: "/api"}
	req := httptest.NewRequest("GET", "/api", nil)

	// Running hooks should trigger plugin methods
	if err := hooks.RunOnRequest(req, route); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !p.requestCalled {
		t.Error("expected OnRequest to be called via hooks")
	}
}

func TestPluginManager_RejectRequest(t *testing.T) {
	pm := NewManager()
	hooks := bastion.NewHookEngine()

	p := &testPlugin{
		BasePlugin:    bastion.BasePlugin{PluginName: "blocker"},
		rejectRequest: true,
	}
	pm.Register(p, hooks)

	route := &bastion.Route{ID: "r1", Path: "/api"}
	req := httptest.NewRequest("GET", "/api", nil)

	err := hooks.RunOnRequest(req, route)
	if err == nil {
		t.Fatal("expected error from rejected request")
	}

	if err.Error() != "rejected by test plugin" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPluginManager_MultiplePlugins(t *testing.T) {
	pm := NewManager()
	hooks := bastion.NewHookEngine()

	p1 := &testPlugin{BasePlugin: bastion.BasePlugin{PluginName: "first"}}
	p2 := &testPlugin{BasePlugin: bastion.BasePlugin{PluginName: "second"}}

	pm.Register(p1, hooks)
	pm.Register(p2, hooks)

	names := pm.PluginNames()
	if len(names) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(names))
	}

	plugins := pm.Plugins()
	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}
}

func TestBasePlugin_NoOps(t *testing.T) {
	bp := &bastion.BasePlugin{PluginName: "noop"}

	if bp.Name() != "noop" {
		t.Errorf("expected 'noop', got %q", bp.Name())
	}

	if err := bp.OnRequest(nil, nil); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}

	bp.OnResponse(nil, nil)   // should not panic
	bp.OnError(nil, nil, nil) // should not panic
}
