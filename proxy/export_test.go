package proxy

import "time"

// SetClock replaces the clock the retry budget reads. Call it before the
// engine serves any request.
func SetClock(pe *Engine, now func() time.Time) { pe.clock = now }
