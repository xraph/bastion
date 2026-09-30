package bastion

import (
	"sync"
	"testing"
	"time"
)

func TestTarget_StatsDoesNotWriteAndIsRaceFree(t *testing.T) {
	tg := &Target{ID: "t", URL: "http://x"}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				tg.IncrConns()
				tg.RecordRequest(2*time.Millisecond, i%10 == 0)
				tg.DecrConns()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		_ = tg.Stats()
	}
	wg.Wait()

	s := tg.Stats()
	if s.TotalRequests != 4000 || s.TotalErrors != 400 || s.ActiveConns != 0 {
		t.Errorf("stats = %+v", s)
	}
	if s.AvgLatencyMs != 2 {
		t.Errorf("avg = %v, want 2", s.AvgLatencyMs)
	}
	if tg.TotalRequests != 0 {
		t.Error("Stats wrote to the target's exported fields")
	}
}
