// Package metrics times what the server does and reports every component's
// measures as one log line an interval. There is no scrape endpoint: the line
// travels with the log, for a log shipper to filter on msg=metrics.
package metrics

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Timer gathers durations between two reports; the zero value is ready.
type Timer struct {
	count, total, longest atomic.Int64
}

func (t *Timer) Observe(d time.Duration) {
	t.count.Add(1)
	t.total.Add(int64(d))
	for {
		longest := t.longest.Load()
		if int64(d) <= longest || t.longest.CompareAndSwap(longest, int64(d)) {
			return
		}
	}
}

// Since observes the time elapsed from start.
func (t *Timer) Since(start time.Time) { t.Observe(time.Since(start)) }

// Attr reports how many durations were observed since the last report, their
// mean and the longest, and starts over.
func (t *Timer) Attr(name string) slog.Attr {
	count, total := t.count.Swap(0), t.total.Swap(0)
	longest := time.Duration(t.longest.Swap(0))
	var mean time.Duration
	if count > 0 {
		mean = time.Duration(total / count)
	}
	return slog.Group(name, "count", count, "avg", mean, "max", longest)
}

// Report logs the sources' measures as one "metrics" line every interval
// until ctx ends. Counters in it run from the start, timers from the last
// line.
func Report(ctx context.Context, interval time.Duration, sources ...func() []slog.Attr) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var attrs []slog.Attr
			for _, source := range sources {
				attrs = append(attrs, source()...)
			}
			slog.LogAttrs(ctx, slog.LevelInfo, "metrics", attrs...)
		}
	}
}
