package metrics

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestReportLogsEverySourceAndTimersStartOver: one line carries all the
// sources' measures, and a timer counts from the line before.
func TestReportLogsEverySourceAndTimersStartOver(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
	defer slog.SetDefault(previous)

	var flush Timer
	flush.Observe(2 * time.Millisecond)
	flush.Observe(4 * time.Millisecond)
	reported := make(chan struct{}, 64)
	storage := func() []slog.Attr {
		reported <- struct{}{}
		return []slog.Attr{slog.Group("storage", "write_queue", 3, flush.Attr("flush"))}
	}
	games := func() []slog.Attr { return []slog.Attr{slog.Group("games", "loaded", 2)} }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Report(ctx, 5*time.Millisecond, storage, games)
	}()
	<-reported
	<-reported
	cancel()
	<-done

	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	const first = "msg=metrics storage.write_queue=3 storage.flush.count=2 storage.flush.avg=3ms storage.flush.max=4ms games.loaded=2"
	if len(lines) < 2 || !strings.HasSuffix(lines[0], first) ||
		!strings.Contains(lines[1], "storage.flush.count=0 storage.flush.avg=0s storage.flush.max=0s") {
		t.Fatalf("metrics lines:\n%s", log.String())
	}
}
