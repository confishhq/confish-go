package confish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// batchLogRecorder records requests behind a mutex so tests can poll for
// asynchronous background flushes without racing the server goroutine.
type batchLogRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *batchLogRecorder) snapshot() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

func (r *batchLogRecorder) waitForRequests(t *testing.T, n int) []recordedRequest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reqs := r.snapshot(); len(reqs) >= n {
			return reqs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d request(s); got %d", n, len(r.snapshot()))
	return nil
}

func newBatchLogServer(t *testing.T) (*httptest.Server, *batchLogRecorder) {
	t.Helper()
	rec := &batchLogRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ids":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func newTestSlogHandler(t *testing.T, baseURL string, opts SlogHandlerOptions) *SlogHandler {
	t.Helper()
	h, err := NewSlogHandler(newTestClient(t, baseURL), opts)
	if err != nil {
		t.Fatalf("NewSlogHandler: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func entriesOf(t *testing.T, req recordedRequest) []map[string]any {
	t.Helper()
	raw, ok := req.Body["entries"].([]any)
	if !ok {
		t.Fatalf("entries: %+v", req.Body["entries"])
	}
	entries := make([]map[string]any, len(raw))
	for i, e := range raw {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("entry %d: %+v", i, e)
		}
		entries[i] = entry
	}
	return entries
}

func TestSlogHandlerMapsLevelsByThreshold(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.Level(-8), "debug"},
		{slog.LevelDebug, "debug"},
		{slog.Level(-1), "debug"},
		{slog.LevelInfo, "info"},
		{slog.Level(1), "info"},
		{SlogLevelNotice, "notice"},
		{slog.Level(3), "notice"},
		{slog.LevelWarn, "warning"},
		{slog.Level(6), "warning"},
		{slog.LevelError, "error"},
		{slog.Level(11), "error"},
		{SlogLevelCritical, "critical"},
		{SlogLevelAlert, "alert"},
		{SlogLevelEmergency, "emergency"},
		{slog.Level(24), "emergency"},
	}

	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: time.Hour})

	for _, tc := range cases {
		record := slog.NewRecord(time.Now(), tc.level, "level check", 0)
		if err := h.Handle(context.Background(), record); err != nil {
			t.Fatalf("Handle(%v): %v", tc.level, err)
		}
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reqs := rec.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if reqs[0].Method != http.MethodPost || reqs[0].Path != "/c/env_test/logs" {
		t.Fatalf("request: %s %s", reqs[0].Method, reqs[0].Path)
	}
	entries := entriesOf(t, reqs[0])
	if len(entries) != len(cases) {
		t.Fatalf("entries: %d", len(entries))
	}
	for i, tc := range cases {
		if entries[i]["level"] != tc.want {
			t.Fatalf("case %d (slog level %v): level %q, want %q", i, tc.level, entries[i]["level"], tc.want)
		}
	}
}

func TestSlogHandlerEnabledRespectsMinLevel(t *testing.T) {
	srv, _ := newBatchLogServer(t)
	cases := []struct {
		name  string
		min   slog.Leveler
		level slog.Level
		want  bool
	}{
		{"default blocks debug", nil, slog.LevelDebug, false},
		{"default allows info", nil, slog.LevelInfo, true},
		{"custom blocks below", SlogLevelCritical, slog.LevelError, false},
		{"custom allows at", SlogLevelCritical, SlogLevelCritical, true},
		{"custom allows above", SlogLevelCritical, SlogLevelEmergency, true},
	}
	for _, tc := range cases {
		h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{Level: tc.min, FlushInterval: time.Hour})
		if got := h.Enabled(context.Background(), tc.level); got != tc.want {
			t.Fatalf("%s: Enabled(%v) = %v, want %v", tc.name, tc.level, got, tc.want)
		}
	}
}

func TestSlogHandlerFlattensGroupsAndAttrs(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: time.Hour})

	logger := slog.New(h).With("worker", "crawler-1").WithGroup("job").With("id", "sitemap-crawl")
	logger.Info("crawl finished",
		"pages", 118,
		slog.Group("timing", slog.Duration("elapsed", 90*time.Second)),
		slog.Group("", slog.String("inline", "yes")),
		slog.Any("err", errors.New("last page timed out")),
		slog.Time("at", time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)),
	)
	// The base handler must be unaffected by the derived handler's attrs.
	slog.New(h).Info("plain")

	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries := entriesOf(t, rec.waitForRequests(t, 1)[0])
	if len(entries) != 2 {
		t.Fatalf("entries: %d", len(entries))
	}
	first := entries[0]
	if first["message"] != "crawl finished" || first["level"] != "info" {
		t.Fatalf("entry: %+v", first)
	}
	if first["timestamp"] == nil || first["timestamp"] == "" {
		t.Fatalf("timestamp missing: %+v", first)
	}
	fields, ok := first["context"].(map[string]any)
	if !ok {
		t.Fatalf("context: %+v", first["context"])
	}
	want := map[string]any{
		"worker":             "crawler-1",
		"job.id":             "sitemap-crawl",
		"job.pages":          float64(118),
		"job.timing.elapsed": "1m30s",
		"job.inline":         "yes",
		"job.err":            "last page timed out",
		"job.at":             "2026-07-10T08:00:00Z",
	}
	if len(fields) != len(want) {
		t.Fatalf("context: %+v", fields)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("context[%q] = %v, want %v", k, fields[k], v)
		}
	}
	if _, present := entries[1]["context"]; present {
		t.Fatalf("plain entry should have no context: %+v", entries[1])
	}
}

func TestSlogHandlerFlushesAtThreshold(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	// Default FlushThreshold (50); interval effectively disabled.
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: time.Hour})
	logger := slog.New(h)

	for i := 0; i < 49; i++ {
		logger.Info("tick", "n", i)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("no flush expected below threshold, got %d request(s)", got)
	}

	logger.Info("tick", "n", 49)
	reqs := rec.waitForRequests(t, 1)
	if entries := entriesOf(t, reqs[0]); len(entries) != 50 {
		t.Fatalf("entries: %d", len(entries))
	}
}

func TestSlogHandlerFlushesOnInterval(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: 50 * time.Millisecond})
	logger := slog.New(h)

	logger.Info("job queued")
	logger.Info("job started")

	reqs := rec.waitForRequests(t, 1)
	entries := entriesOf(t, reqs[0])
	if len(entries) != 2 || entries[0]["message"] != "job queued" || entries[1]["message"] != "job started" {
		t.Fatalf("entries: %+v", entries)
	}
}

func TestSlogHandlerChunksLargeFlushes(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{
		QueueSize:      300,
		FlushThreshold: 1000, // never trips; drain via explicit Flush only
		FlushInterval:  time.Hour,
	})

	for i := 0; i < 250; i++ {
		record := slog.NewRecord(time.Now(), slog.LevelInfo, fmt.Sprintf("entry %d", i), 0)
		_ = h.Handle(context.Background(), record)
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reqs := rec.snapshot()
	if len(reqs) != 3 {
		t.Fatalf("requests: %d", len(reqs))
	}
	wantSizes := []int{100, 100, 50}
	for i, req := range reqs {
		entries := entriesOf(t, req)
		if len(entries) != wantSizes[i] {
			t.Fatalf("chunk %d: %d entries, want %d", i, len(entries), wantSizes[i])
		}
		if wantFirst := fmt.Sprintf("entry %d", i*100); entries[0]["message"] != wantFirst {
			t.Fatalf("chunk %d starts with %q, want %q", i, entries[0]["message"], wantFirst)
		}
	}
}

func TestSlogHandlerDropsOldestOnOverflow(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	var dropNotices int
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{
		QueueSize:      3,
		FlushThreshold: 100, // never trips; overflow before any flush
		FlushInterval:  time.Hour,
		OnDrop:         func(count int) { dropNotices += count },
	})
	logger := slog.New(h)

	for i := 0; i < 5; i++ {
		logger.Info(fmt.Sprintf("entry %d", i))
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries := entriesOf(t, rec.waitForRequests(t, 1)[0])
	if len(entries) != 3 {
		t.Fatalf("entries: %d", len(entries))
	}
	for i, want := range []string{"entry 2", "entry 3", "entry 4"} {
		if entries[i]["message"] != want {
			t.Fatalf("entry %d: %q, want %q (oldest should be dropped)", i, entries[i]["message"], want)
		}
	}
	if h.Dropped() != 2 {
		t.Fatalf("Dropped: %d", h.Dropped())
	}
	if dropNotices != 2 {
		t.Fatalf("OnDrop total: %d", dropNotices)
	}
}

func TestSlogHandlerCloseFlushesRemaining(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: time.Hour})
	logger := slog.New(h)

	logger.Info("crawl started")
	logger.Warn("robots.txt missing")
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reqs := rec.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	entries := entriesOf(t, reqs[0])
	if len(entries) != 2 || entries[1]["level"] != "warning" {
		t.Fatalf("entries: %+v", entries)
	}

	// After Close: records are dropped and counted, never sent.
	logger.Info("too late")
	time.Sleep(50 * time.Millisecond)
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("requests after Close: %d", got)
	}
	if h.Dropped() != 1 {
		t.Fatalf("Dropped: %d", h.Dropped())
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSlogHandlerFlushFailureReportsAndDiscards(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	var reported []error
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{
		FlushInterval: time.Hour,
		OnError:       func(err error) { reported = append(reported, err) },
	})
	logger := slog.New(h)

	logger.Info("crawl started")
	logger.Info("crawl finished")

	err := h.Flush(context.Background())
	var se *ServerError
	if !errors.As(err, &se) {
		t.Fatalf("expected *ServerError, got %T: %v", err, err)
	}
	if len(reported) != 1 {
		t.Fatalf("OnError calls: %d", len(reported))
	}
	if h.Dropped() != 2 {
		t.Fatalf("Dropped: %d", h.Dropped())
	}
	// Failed entries are discarded, not retried on the next flush.
	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("empty Flush after failure: %v", err)
	}
}

func TestSlogHandlerSendsNothingWhenIdle(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: 20 * time.Millisecond})

	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // several ticks
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("idle handler sent %d request(s)", got)
	}
}

func TestSlogHandlerTimestampIsRecordTime(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{FlushInterval: time.Hour})

	at := time.Date(2026, 7, 10, 12, 30, 15, 123456789, time.UTC)
	if err := h.Handle(context.Background(), slog.NewRecord(at, slog.LevelInfo, "job started", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	entries := entriesOf(t, rec.waitForRequests(t, 1)[0])
	if entries[0]["timestamp"] != "2026-07-10T12:30:15.123456789Z" {
		t.Fatalf("timestamp: %q", entries[0]["timestamp"])
	}
}

func TestSlogHandlerConcurrentUse(t *testing.T) {
	srv, rec := newBatchLogServer(t)
	h := newTestSlogHandler(t, srv.URL, SlogHandlerOptions{
		QueueSize:     5000,
		FlushInterval: 20 * time.Millisecond,
	})

	const goroutines, perGoroutine = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			logger := slog.New(h).With("goroutine", g).WithGroup("job")
			for i := 0; i < perGoroutine; i++ {
				logger.Info("work", "i", i)
				if i%25 == 0 {
					_ = h.Flush(context.Background())
				}
			}
		}(g)
	}
	wg.Wait()
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var total int
	for _, req := range rec.snapshot() {
		entries := entriesOf(t, req)
		if len(entries) > MaxLogBatchSize {
			t.Fatalf("request over batch cap: %d entries", len(entries))
		}
		total += len(entries)
	}
	if total != goroutines*perGoroutine {
		t.Fatalf("total entries: %d, want %d", total, goroutines*perGoroutine)
	}
	if h.Dropped() != 0 {
		t.Fatalf("Dropped: %d", h.Dropped())
	}
}
