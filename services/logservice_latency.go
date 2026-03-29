package services

import (
	"math"
	"sort"
)

const slowRequestThresholdSec = 5.0

type durationAccumulator struct {
	values       []float64
	total        float64
	slowRequests int64
}

func (a *durationAccumulator) Add(value float64) {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	a.values = append(a.values, value)
	a.total += value
	if value >= slowRequestThresholdSec {
		a.slowRequests++
	}
}

func (a *durationAccumulator) SampleCount() int64 {
	return int64(len(a.values))
}

func (a *durationAccumulator) AvgSec() float64 {
	count := a.SampleCount()
	if count <= 0 {
		return 0
	}
	return a.total / float64(count)
}

func (a *durationAccumulator) P95Sec() float64 {
	return percentileDuration(a.sortedValues(), 0.95)
}

func (a *durationAccumulator) P99Sec() float64 {
	return percentileDuration(a.sortedValues(), 0.99)
}

func (a *durationAccumulator) SlowRequests() int64 {
	return a.slowRequests
}

func (a *durationAccumulator) SlowRate() float64 {
	count := a.SampleCount()
	if count <= 0 {
		return 0
	}
	return float64(a.slowRequests) / float64(count)
}

func (a *durationAccumulator) sortedValues() []float64 {
	if len(a.values) == 0 {
		return nil
	}
	values := append([]float64(nil), a.values...)
	sort.Float64s(values)
	return values
}

func percentileDuration(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(float64(len(sorted))*percentile)) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func applyDurationStatsToLogStats(stats *LogStats, acc *durationAccumulator) {
	if stats == nil || acc == nil {
		return
	}
	stats.DurationSamples = acc.SampleCount()
	if stats.DurationSamples <= 0 {
		return
	}
	stats.DurationAvgSec = acc.AvgSec()
	stats.DurationP95Sec = acc.P95Sec()
	stats.DurationP99Sec = acc.P99Sec()
	stats.SlowRequests = acc.SlowRequests()
	stats.SlowRate = acc.SlowRate()
}

func applyDurationStatsToProviderStat(stat *ProviderDailyStat, acc *durationAccumulator) {
	if stat == nil || acc == nil {
		return
	}
	stat.DurationSamples = acc.SampleCount()
	if stat.DurationSamples <= 0 {
		return
	}
	stat.DurationAvgSec = acc.AvgSec()
	stat.DurationP95Sec = acc.P95Sec()
	stat.DurationP99Sec = acc.P99Sec()
	stat.SlowRequests = acc.SlowRequests()
	stat.SlowRate = acc.SlowRate()
}
