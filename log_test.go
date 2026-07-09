package confish

import (
	"context"
	"net/http"
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
