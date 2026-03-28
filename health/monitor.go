package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/xraph/forge"
)

// Monitor monitors the health of upstream targets.
type Monitor struct {
	config Config
	logger forge.Logger

	mu       sync.RWMutex
	targets  map[string]*monitoredTarget // key: targetID
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Callbacks
	onHealthChange func(event Event)

	client *http.Client
}

type monitoredTarget struct {
	target           Target
	routeID          string
	consecutiveFails int
	consecutiveOK    int
	lastCheck        time.Time
}

// NewMonitor creates a new health monitor.
func NewMonitor(config Config, logger forge.Logger) *Monitor {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: config.Timeout,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Monitor{
		config:  config,
		logger:  logger,
		targets: make(map[string]*monitoredTarget),
		stopCh:  make(chan struct{}),
		client: &http.Client{
			Timeout:   config.Timeout,
			Transport: transport,
		},
	}
}

// SetOnHealthChange sets the health change callback.
func (hm *Monitor) SetOnHealthChange(fn func(event Event)) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.onHealthChange = fn
}

// Register registers a target for health monitoring.
func (hm *Monitor) Register(routeID string, target Target) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.targets[target.GetID()] = &monitoredTarget{
		target:  target,
		routeID: routeID,
	}
}

// Deregister removes a target from health monitoring.
func (hm *Monitor) Deregister(targetID string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	delete(hm.targets, targetID)
}

// RecordPassiveFailure records a passive health check failure.
func (hm *Monitor) RecordPassiveFailure(targetID string) {
	if !hm.config.EnablePassive {
		return
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	mt, ok := hm.targets[targetID]
	if !ok {
		return
	}

	mt.consecutiveFails++
	mt.consecutiveOK = 0

	if mt.consecutiveFails >= hm.config.PassiveFailThreshold && mt.target.IsHealthy() {
		hm.setHealthy(mt, false)
	}
}

// RecordPassiveSuccess records a passive health check success.
func (hm *Monitor) RecordPassiveSuccess(targetID string) {
	if !hm.config.EnablePassive {
		return
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	mt, ok := hm.targets[targetID]
	if !ok {
		return
	}

	mt.consecutiveOK++

	if mt.consecutiveOK >= hm.config.SuccessThreshold {
		mt.consecutiveFails = 0
	}
}

// Start starts the health monitor background loop.
func (hm *Monitor) Start(ctx context.Context) {
	if !hm.config.Enabled {
		return
	}

	hm.wg.Add(1)

	go hm.loop(ctx)
}

// Stop stops the health monitor. It is safe to call multiple times.
func (hm *Monitor) Stop() {
	hm.stopOnce.Do(func() {
		close(hm.stopCh)
	})
	hm.wg.Wait()
	hm.client.CloseIdleConnections()
}

func (hm *Monitor) loop(ctx context.Context) {
	defer hm.wg.Done()

	ticker := time.NewTicker(hm.config.Interval)
	defer ticker.Stop()

	// Initial check
	hm.checkAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hm.stopCh:
			return
		case <-ticker.C:
			hm.checkAll(ctx)
		}
	}
}

func (hm *Monitor) checkAll(ctx context.Context) {
	hm.mu.RLock()
	targets := make([]*monitoredTarget, 0, len(hm.targets))
	for _, mt := range hm.targets {
		targets = append(targets, mt)
	}
	hm.mu.RUnlock()

	var wg sync.WaitGroup

	for _, mt := range targets {
		wg.Add(1)

		go func(mt *monitoredTarget) {
			defer wg.Done()

			hm.checkTarget(ctx, mt)
		}(mt)
	}

	wg.Wait()
}

func (hm *Monitor) checkTarget(ctx context.Context, mt *monitoredTarget) {
	checkCtx, cancel := context.WithTimeout(ctx, hm.config.Timeout)
	defer cancel()

	// Use per-target health path if the target provides one; otherwise fall
	// back to the global monitor path.
	healthPath := hm.config.Path
	if hp, ok := mt.target.(HealthPathProvider); ok {
		if p := hp.HealthPath(); p != "" {
			healthPath = p
		}
	}

	url := mt.target.GetURL() + healthPath

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
	if err != nil {
		hm.handleCheckFailure(mt, err)

		return
	}

	req.Header.Set("User-Agent", "forge-gateway-health-check")

	resp, err := hm.client.Do(req)
	if err != nil {
		hm.handleCheckFailure(mt, err)

		return
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		hm.handleCheckSuccess(mt)
	} else {
		hm.handleCheckFailure(mt, fmt.Errorf("unhealthy status: %d", resp.StatusCode))
	}
}

func (hm *Monitor) handleCheckSuccess(mt *monitoredTarget) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	mt.consecutiveOK++
	mt.consecutiveFails = 0
	mt.lastCheck = time.Now()

	if !mt.target.IsHealthy() && mt.consecutiveOK >= hm.config.SuccessThreshold {
		hm.setHealthy(mt, true)
	}
}

func (hm *Monitor) handleCheckFailure(mt *monitoredTarget, err error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	mt.consecutiveFails++
	mt.consecutiveOK = 0
	mt.lastCheck = time.Now()

	if mt.target.IsHealthy() && mt.consecutiveFails >= hm.config.FailureThreshold {
		hm.logger.Warn("upstream health check failed",
			forge.F("target_id", mt.target.GetID()),
			forge.F("target_url", mt.target.GetURL()),
			forge.F("consecutive_failures", mt.consecutiveFails),
			forge.F("error", err),
		)

		hm.setHealthy(mt, false)
	}
}

// setHealthy updates target health and emits event (must be called with lock held).
func (hm *Monitor) setHealthy(mt *monitoredTarget, healthy bool) {
	previous := mt.target.IsHealthy()
	mt.target.SetHealthy(healthy)

	if hm.onHealthChange != nil {
		event := Event{
			TargetID:  mt.target.GetID(),
			TargetURL: mt.target.GetURL(),
			Healthy:   healthy,
			Previous:  previous,
			RouteID:   mt.routeID,
			Timestamp: time.Now(),
		}

		go hm.onHealthChange(event)
	}

	if healthy {
		hm.logger.Info("upstream became healthy",
			forge.F("target_id", mt.target.GetID()),
			forge.F("target_url", mt.target.GetURL()),
		)
	} else {
		hm.logger.Warn("upstream became unhealthy",
			forge.F("target_id", mt.target.GetID()),
			forge.F("target_url", mt.target.GetURL()),
		)
	}
}

// Health returns an error if no targets are healthy (for HealthManager integration).
func (hm *Monitor) Health(_ context.Context) error {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	healthyCount := 0

	for _, mt := range hm.targets {
		if mt.target.IsHealthy() {
			healthyCount++
		}
	}

	if len(hm.targets) > 0 && healthyCount == 0 {
		return fmt.Errorf("no healthy upstream targets (0/%d)", len(hm.targets))
	}

	return nil
}
