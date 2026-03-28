package health

import (
	"testing"
	"time"
)

func TestHistory_RecordAndGet(t *testing.T) {
	hh := NewHistory(10)

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   true,
		Latency:   5 * time.Millisecond,
		Timestamp: time.Now(),
	})

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   false,
		Error:     "timeout",
		Latency:   30 * time.Millisecond,
		Timestamp: time.Now(),
	})

	results := hh.Get("t1")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Healthy != true {
		t.Error("first result should be healthy")
	}

	if results[1].Error != "timeout" {
		t.Error("second result should have timeout error")
	}
}

func TestHistory_RingBufferOverflow(t *testing.T) {
	hh := NewHistory(3)

	for i := 0; i < 5; i++ {
		hh.Record(CheckResult{
			TargetID:  "t1",
			Healthy:   i%2 == 0,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	results := hh.Get("t1")
	if len(results) != 3 {
		t.Fatalf("expected 3 results (capacity), got %d", len(results))
	}

	// Oldest kept should be index 2 (i=2), then 3, then 4
	if results[0].Healthy != true { // i=2, even
		t.Error("expected first kept result to be healthy (i=2)")
	}
}

func TestHistory_GetSince(t *testing.T) {
	hh := NewHistory(10)
	now := time.Now()

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   true,
		Timestamp: now.Add(-2 * time.Minute),
	})

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   false,
		Timestamp: now.Add(-30 * time.Second),
	})

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   true,
		Timestamp: now,
	})

	results := hh.GetSince("t1", now.Add(-1*time.Minute))
	if len(results) != 2 {
		t.Fatalf("expected 2 results since 1 min ago, got %d", len(results))
	}
}

func TestHistory_Targets(t *testing.T) {
	hh := NewHistory(10)

	hh.Record(CheckResult{TargetID: "t1", Timestamp: time.Now()})
	hh.Record(CheckResult{TargetID: "t2", Timestamp: time.Now()})

	targets := hh.Targets()
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
}

func TestHistory_UptimePercent(t *testing.T) {
	hh := NewHistory(10)
	now := time.Now()

	for i := 0; i < 10; i++ {
		hh.Record(CheckResult{
			TargetID:  "t1",
			Healthy:   i < 8, // 8 healthy, 2 unhealthy
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	pct := hh.UptimePercent("t1", 1*time.Hour)
	if pct != 80.0 {
		t.Errorf("expected 80%% uptime, got %.1f%%", pct)
	}

	// Non-existent target
	pct = hh.UptimePercent("nonexistent", 1*time.Hour)
	if pct != -1 {
		t.Errorf("expected -1 for nonexistent, got %.1f", pct)
	}
}

func TestHistory_Summary(t *testing.T) {
	hh := NewHistory(10)
	now := time.Now()

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   true,
		Latency:   10 * time.Millisecond,
		Timestamp: now,
	})

	hh.Record(CheckResult{
		TargetID:  "t1",
		Healthy:   false,
		Latency:   20 * time.Millisecond,
		Timestamp: now.Add(time.Second),
	})

	summary := hh.Summary("t1")
	if summary.TotalChecks != 2 {
		t.Errorf("expected 2 total, got %d", summary.TotalChecks)
	}

	if summary.HealthyChecks != 1 {
		t.Errorf("expected 1 healthy, got %d", summary.HealthyChecks)
	}

	if summary.UptimePercent != 50.0 {
		t.Errorf("expected 50%%, got %.1f%%", summary.UptimePercent)
	}

	if summary.AvgLatency != 15*time.Millisecond {
		t.Errorf("expected 15ms avg, got %v", summary.AvgLatency)
	}

	if summary.LastCheck == nil {
		t.Fatal("expected last check")
	}

	if summary.LastCheck.Healthy {
		t.Error("last check should be unhealthy")
	}
}

func TestHistory_EmptySummary(t *testing.T) {
	hh := NewHistory(10)

	summary := hh.Summary("nonexistent")
	if summary.TotalChecks != 0 {
		t.Error("expected empty summary")
	}
}

func TestHistory_GetNonExistent(t *testing.T) {
	hh := NewHistory(10)

	results := hh.Get("nonexistent")
	if results != nil {
		t.Error("expected nil for nonexistent target")
	}
}
