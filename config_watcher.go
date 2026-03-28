package bastion

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// ConfigWatcher monitors a config file and triggers a callback when
// the file changes. It uses polling rather than OS-specific file
// watchers for portability.
type ConfigWatcher struct {
	path     string
	interval time.Duration
	onChange func(Config)
	onError  func(error)
	stopCh   chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	lastMod  time.Time
	lastSize int64
}

// NewConfigWatcher creates a config file watcher.
func NewConfigWatcher(path string, interval time.Duration, onChange func(Config)) *ConfigWatcher {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	return &ConfigWatcher{
		path:     path,
		interval: interval,
		onChange: onChange,
		onError:  func(error) {},
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// SetErrorHandler sets a callback for errors during config reload.
func (cw *ConfigWatcher) SetErrorHandler(fn func(error)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	cw.onError = fn
}

// Start begins polling the config file for changes.
func (cw *ConfigWatcher) Start() {
	// Record initial state
	if info, err := os.Stat(cw.path); err == nil {
		cw.lastMod = info.ModTime()
		cw.lastSize = info.Size()
	}

	go cw.poll()
}

// Stop ends the polling loop and waits for it to finish.
func (cw *ConfigWatcher) Stop() {
	close(cw.stopCh)
	<-cw.done
}

func (cw *ConfigWatcher) poll() {
	defer close(cw.done)

	ticker := time.NewTicker(cw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-cw.stopCh:
			return
		case <-ticker.C:
			cw.check()
		}
	}
}

func (cw *ConfigWatcher) check() {
	info, err := os.Stat(cw.path)
	if err != nil {
		return // file doesn't exist or not accessible; ignore
	}

	cw.mu.Lock()
	changed := info.ModTime() != cw.lastMod || info.Size() != cw.lastSize
	if changed {
		cw.lastMod = info.ModTime()
		cw.lastSize = info.Size()
	}
	onError := cw.onError
	cw.mu.Unlock()

	if !changed {
		return
	}

	cfg, err := cw.loadConfig()
	if err != nil {
		onError(fmt.Errorf("config reload failed: %w", err))
		return
	}

	// Validate before applying
	if err := ValidateConfig(&cfg); err != nil {
		onError(fmt.Errorf("config validation failed: %w", err))
		return
	}

	cw.onChange(cfg)
}

func (cw *ConfigWatcher) loadConfig() (Config, error) {
	data, err := os.ReadFile(cw.path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}
