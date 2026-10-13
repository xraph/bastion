package proxy

import (
	"sync"
	"time"
)

const (
	// budgetBuckets is how many one-second buckets the retry budget
	// window holds, so the window is the last ten seconds.
	budgetBuckets = 10

	// minRetriesPerWindow is the floor under the retry budget: this many
	// retries per window are always allowed, whatever the percentage
	// says. Without it a target that gets one request every few seconds
	// could never retry, since one retry is already 100% of its traffic.
	minRetriesPerWindow = 3
)

// retryBudget caps retries to a share of the requests a target received
// over the last budgetBuckets seconds. It is keyed by target ID, the same
// key the circuit breakers use, because retries go back to the target
// that failed and the budget is there to protect that upstream.
type retryBudget struct {
	mu      sync.Mutex
	buckets [budgetBuckets]budgetBucket
}

type budgetBucket struct {
	sec     int64 // the unix second this bucket counts
	reqs    int64
	retries int64
}

// bucket returns the bucket for the second now falls in, cleared if it
// last counted an older second. The caller holds b.mu.
func (b *retryBudget) bucket(sec int64) *budgetBucket {
	bk := &b.buckets[sec%budgetBuckets]
	if bk.sec != sec {
		*bk = budgetBucket{sec: sec}
	}

	return bk
}

// recordRequest counts one request sent to the target.
func (b *retryBudget) recordRequest(now time.Time) {
	sec := now.Unix()

	b.mu.Lock()
	b.bucket(sec).reqs++
	b.mu.Unlock()
}

// tryRetry reports whether one more retry fits the budget, and counts it
// when it does. A retry fits while the window holds fewer than
// minRetriesPerWindow retries, or while retries, this one included, stay
// within percent of the window's requests.
func (b *retryBudget) tryRetry(now time.Time, percent float64) bool {
	sec := now.Unix()

	b.mu.Lock()
	defer b.mu.Unlock()

	var reqs, retries int64

	for i := range b.buckets {
		bk := &b.buckets[i]
		if age := sec - bk.sec; age >= 0 && age < budgetBuckets {
			reqs += bk.reqs
			retries += bk.retries
		}
	}

	if retries >= minRetriesPerWindow && float64(retries+1) > percent/100*float64(reqs) {
		return false
	}

	b.bucket(sec).retries++

	return true
}

// budgetFor returns the retry budget for a target, creating it on first
// use.
func (pe *Engine) budgetFor(targetID string) *retryBudget {
	if b, ok := pe.budgets.Load(targetID); ok {
		return b.(*retryBudget) //nolint:forcetypeassert // only *retryBudget is stored
	}

	b, _ := pe.budgets.LoadOrStore(targetID, &retryBudget{})

	return b.(*retryBudget) //nolint:forcetypeassert // only *retryBudget is stored
}

// now returns the engine's clock reading. Tests swap the clock.
func (pe *Engine) now() time.Time {
	if pe.clock != nil {
		return pe.clock()
	}

	return time.Now()
}
