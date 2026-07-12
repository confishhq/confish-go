package confish

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestLogsWriteReturnsID(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"log_123"}`))
	})
	c := newTestClient(t, srv.URL)

	id, err := c.Logs.Write(context.Background(), LogEntry{Level: LevelInfo, Message: "hi"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id != "log_123" {
		t.Fatalf("id: %s", id)
	}
	if (*calls)[0].Path != "/c/env_test/log" {
		t.Fatalf("path: %q", (*calls)[0].Path)
	}
}

func TestLogsWriteBatchRejectsOversizedBatchWithoutRequest(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		t.Error("no request expected for an oversized batch")
		w.WriteHeader(http.StatusCreated)
	})
	c := newTestClient(t, srv.URL)

	entries := make([]LogEntry, MaxLogBatchSize+1)
	for i := range entries {
		entries[i] = LogEntry{Level: LevelInfo, Message: "tick"}
	}
	ids, err := c.Logs.WriteBatch(context.Background(), entries)
	if err == nil {
		t.Fatal("expected error for batch over MaxLogBatchSize")
	}
	if !strings.Contains(err.Error(), "at most 100 entries") || !strings.Contains(err.Error(), "got 101") {
		t.Fatalf("error: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids: %v", ids)
	}
	if len(*calls) != 0 {
		t.Fatalf("requests: %d", len(*calls))
	}
}

func TestLogsWriteBatchEmptySliceIsNoOp(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		t.Error("no request expected for an empty batch")
		w.WriteHeader(http.StatusCreated)
	})
	c := newTestClient(t, srv.URL)

	for _, entries := range [][]LogEntry{nil, {}} {
		ids, err := c.Logs.WriteBatch(context.Background(), entries)
		if err != nil {
			t.Fatalf("WriteBatch(%v): %v", entries, err)
		}
		if ids != nil {
			t.Fatalf("ids: %v", ids)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("requests: %d", len(*calls))
	}
}

func TestLogsWriteBatchSendsEntriesAndReturnsIDs(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ids":["log_1","log_2"]}`))
	})
	c := newTestClient(t, srv.URL)

	ids, err := c.Logs.WriteBatch(context.Background(), []LogEntry{
		{Level: LevelInfo, Message: "crawl started"},
		{Level: LevelError, Message: "crawl failed", Context: map[string]any{"pages": 118}, Timestamp: "2026-07-10T12:00:00Z"},
	})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if len(ids) != 2 || ids[0] != "log_1" || ids[1] != "log_2" {
		t.Fatalf("ids: %v", ids)
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/c/env_test/logs" {
		t.Fatalf("request: %s %s", call.Method, call.Path)
	}
	entries, ok := call.Body["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries: %+v", call.Body["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["level"] != "info" || first["message"] != "crawl started" {
		t.Fatalf("first entry: %+v", entries[0])
	}
	if _, present := first["timestamp"]; present {
		t.Fatalf("timestamp should be omitted when unset: %+v", first)
	}
	second, _ := entries[1].(map[string]any)
	if second["level"] != "error" || second["timestamp"] != "2026-07-10T12:00:00Z" {
		t.Fatalf("second entry: %+v", entries[1])
	}
}

func TestLogsEmergencySendsLevel(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"log_124"}`))
	})
	c := newTestClient(t, srv.URL)

	err := c.Logs.Emergency(context.Background(), "region down", map[string]any{"region": "eu-west-1"})
	if err != nil {
		t.Fatalf("Emergency: %v", err)
	}
	body := (*calls)[0].Body
	if body["level"] != "emergency" || body["message"] != "region down" {
		t.Fatalf("body: %+v", body)
	}
	fields, ok := body["context"].(map[string]any)
	if !ok || fields["region"] != "eu-west-1" {
		t.Fatalf("context: %+v", body["context"])
	}
}
