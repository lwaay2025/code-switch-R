package services

import "testing"

func TestDurationAccumulatorSummaries(t *testing.T) {
	acc := &durationAccumulator{}
	for _, value := range []float64{0, 1, 2, 3, 10, 20} {
		acc.Add(value)
	}

	if got := acc.SampleCount(); got != 5 {
		t.Fatalf("SampleCount = %d, want 5", got)
	}
	if got := acc.SlowRequests(); got != 2 {
		t.Fatalf("SlowRequests = %d, want 2", got)
	}
	if got := acc.AvgSec(); got != 7.2 {
		t.Fatalf("AvgSec = %v, want 7.2", got)
	}
	if got := acc.P95Sec(); got != 20 {
		t.Fatalf("P95Sec = %v, want 20", got)
	}
	if got := acc.P99Sec(); got != 20 {
		t.Fatalf("P99Sec = %v, want 20", got)
	}
	if got := acc.SlowRate(); got != 0.4 {
		t.Fatalf("SlowRate = %v, want 0.4", got)
	}
}

func TestPercentileDurationHandlesBounds(t *testing.T) {
	values := []float64{1, 2, 3}
	if got := percentileDuration(values, 0); got != 1 {
		t.Fatalf("percentileDuration(..., 0) = %v, want 1", got)
	}
	if got := percentileDuration(values, 1); got != 3 {
		t.Fatalf("percentileDuration(..., 1) = %v, want 3", got)
	}
	if got := percentileDuration(values, 0.5); got != 2 {
		t.Fatalf("percentileDuration(..., 0.5) = %v, want 2", got)
	}
}
