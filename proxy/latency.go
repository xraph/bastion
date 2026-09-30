package proxy

import (
	"math"
	"slices"
	"time"
)

// RouteLatencyWindow is how many recent responses a route's p99 covers.
const RouteLatencyWindow = 1024

// GatewayLatencyWindow is how many recent responses the gateway-wide p99
// covers.
const GatewayLatencyWindow = 4096

// latencyTrack keeps a lifetime average and a fixed window of recent samples
// for an exact nearest-rank p99. It is not safe for concurrent use; the
// StatsCollector's lock guards it.
type latencyTrack struct {
	sumNs int64
	count int64
	ring  []int64
	next  int
	full  bool
}

func newLatencyTrack(window int) *latencyTrack {
	return &latencyTrack{ring: make([]int64, window)}
}

func (lt *latencyTrack) record(d time.Duration) {
	ns := d.Nanoseconds()
	lt.sumNs += ns
	lt.count++
	lt.ring[lt.next] = ns
	lt.next++

	if lt.next == len(lt.ring) {
		lt.next = 0
		lt.full = true
	}
}

// samples reports how many values the window currently holds.
func (lt *latencyTrack) samples() int {
	if lt.full {
		return len(lt.ring)
	}

	return lt.next
}

// avgMs is the lifetime mean in milliseconds, 0 with no samples.
func (lt *latencyTrack) avgMs() float64 {
	if lt.count == 0 {
		return 0
	}

	return float64(lt.sumNs) / float64(lt.count) / 1e6
}

// p99Ms is the nearest-rank 99th percentile of the window in milliseconds,
// 0 with no samples.
func (lt *latencyTrack) p99Ms() float64 {
	n := lt.samples()
	if n == 0 {
		return 0
	}

	sorted := slices.Clone(lt.ring[:n])
	slices.Sort(sorted)
	rank := int(math.Ceil(0.99 * float64(n)))

	return float64(sorted[rank-1]) / 1e6
}
