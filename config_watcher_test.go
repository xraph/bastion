package bastion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigWatcher_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := Config{Enabled: true, BasePath: "/gw"}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, data, 0644)

	changed := make(chan Config, 1)

	cw := NewConfigWatcher(cfgPath, 100*time.Millisecond, func(c Config) {
		changed <- c
	})
	cw.Start()
	defer cw.Stop()

	// Wait for initial state to be recorded
	time.Sleep(200 * time.Millisecond)

	// Modify the config
	cfg.BasePath = "/api"
	data, _ = json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, data, 0644)

	select {
	case newCfg := <-changed:
		if newCfg.BasePath != "/api" {
			t.Errorf("expected /api, got %s", newCfg.BasePath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for config change")
	}
}

func TestConfigWatcher_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := Config{Enabled: true}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, data, 0644)

	errCh := make(chan error, 1)

	cw := NewConfigWatcher(cfgPath, 100*time.Millisecond, func(c Config) {
		t.Error("should not call onChange for invalid JSON")
	})
	cw.SetErrorHandler(func(err error) {
		errCh <- err
	})
	cw.Start()
	defer cw.Stop()

	time.Sleep(200 * time.Millisecond)

	// Write invalid JSON
	_ = os.WriteFile(cfgPath, []byte("not json!!!"), 0644)

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for error")
	}
}

func TestConfigWatcher_NoChangeNoCallback(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := Config{Enabled: true}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(cfgPath, data, 0644)

	callCount := 0
	cw := NewConfigWatcher(cfgPath, 100*time.Millisecond, func(c Config) {
		callCount++
	})
	cw.Start()
	defer cw.Stop()

	time.Sleep(500 * time.Millisecond)

	if callCount != 0 {
		t.Errorf("expected 0 callbacks without change, got %d", callCount)
	}
}

func TestConfigWatcher_MissingFile(t *testing.T) {
	cw := NewConfigWatcher("/nonexistent/config.json", 100*time.Millisecond, func(c Config) {
		t.Error("should not callback for missing file")
	})
	cw.Start()

	time.Sleep(300 * time.Millisecond)
	cw.Stop()
}
