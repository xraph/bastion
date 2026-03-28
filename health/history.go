package health

import (
	"sync"
	"time"
)

// History keeps a fixed-size ring buffer of health check results
// per upstream target. It is safe for concurrent use.
type History struct {
	mu       sync.RWMutex
	buffers  map[string]*ringBuffer // targetID -> ring buffer
	capacity int
}

// NewHistory creates a history tracker that stores up to capacity
// results per target.
func NewHistory(capacity int) *History {
	if capacity <= 0 {
		capacity = 100
	}

	return &History{
		buffers:  make(map[string]*ringBuffer),
		capacity: capacity,
	}
}

// Record stores a health check result.
func (hh *History) Record(result CheckResult) {
	hh.mu.Lock()
	defer hh.mu.Unlock()

	buf, ok := hh.buffers[result.TargetID]
	if !ok {
		buf = newRingBuffer(hh.capacity)
		hh.buffers[result.TargetID] = buf
	}

	buf.push(result)
}

// Get returns all recorded results for a target in chronological order.
func (hh *History) Get(targetID string) []CheckResult {
	hh.mu.RLock()
	defer hh.mu.RUnlock()

	buf, ok := hh.buffers[targetID]
	if !ok {
		return nil
	}

	return buf.snapshot()
}

// GetSince returns results for a target after the given time.
func (hh *History) GetSince(targetID string, since time.Time) []CheckResult {
	all := hh.Get(targetID)
	for i, r := range all {
		if !r.Timestamp.Before(since) {
			return all[i:]
		}
	}

	return nil
}

// Targets returns a list of all tracked target IDs.
func (hh *History) Targets() []string {
	hh.mu.RLock()
	defer hh.mu.RUnlock()

	ids := make([]string, 0, len(hh.buffers))
	for id := range hh.buffers {
		ids = append(ids, id)
	}

	return ids
}

// UptimePercent returns the percentage of healthy checks for a target
// over the last duration. Returns -1 if no data.
func (hh *History) UptimePercent(targetID string, window time.Duration) float64 {
	since := time.Now().Add(-window)
	results := hh.GetSince(targetID, since)

	if len(results) == 0 {
		return -1
	}

	healthy := 0
	for _, r := range results {
		if r.Healthy {
			healthy++
		}
	}

	return float64(healthy) / float64(len(results)) * 100
}

// Summary returns a Summary for the given target.
func (hh *History) Summary(targetID string) Summary {
	results := hh.Get(targetID)
	summary := Summary{TargetID: targetID}

	if len(results) == 0 {
		return summary
	}

	summary.TotalChecks = len(results)
	var totalLatency time.Duration

	for i := range results {
		if results[i].Healthy {
			summary.HealthyChecks++
		}
		totalLatency += results[i].Latency
	}

	summary.UptimePercent = float64(summary.HealthyChecks) / float64(summary.TotalChecks) * 100
	summary.AvgLatency = totalLatency / time.Duration(summary.TotalChecks)
	last := results[len(results)-1]
	summary.LastCheck = &last

	return summary
}

// ringBuffer is a fixed-size circular buffer for CheckResults.
type ringBuffer struct {
	data   []CheckResult
	cursor int
	full   bool
	cap    int
}

func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{
		data: make([]CheckResult, cap),
		cap:  cap,
	}
}

func (rb *ringBuffer) push(r CheckResult) {
	rb.data[rb.cursor] = r
	rb.cursor++

	if rb.cursor >= rb.cap {
		rb.cursor = 0
		rb.full = true
	}
}

func (rb *ringBuffer) snapshot() []CheckResult {
	if !rb.full {
		out := make([]CheckResult, rb.cursor)
		copy(out, rb.data[:rb.cursor])
		return out
	}

	// Full buffer: read from cursor (oldest) wrapping around
	out := make([]CheckResult, rb.cap)
	copy(out, rb.data[rb.cursor:])
	copy(out[rb.cap-rb.cursor:], rb.data[:rb.cursor])

	return out
}
