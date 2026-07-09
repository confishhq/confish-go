package confish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActionsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"actions":[{"id":"a1","type":"noop","status":"pending"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	actions, err := c.Actions.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(actions) != 1 || actions[0].ID != "a1" {
		t.Fatalf("actions: %+v", actions)
	}
}

func TestActionsCompleteWithResult(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a1","status":"completed"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if _, err := c.Actions.Complete(context.Background(), "a1", map[string]any{"ok": true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	result, ok := captured["result"].(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("captured: %+v", captured)
	}
}

func TestActionsProgressPostsToUpdateEndpoint(t *testing.T) {
	var path string
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a1","status":"acknowledged","updates":[{"message":"closing 3 positions"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	action, err := c.Actions.Progress(context.Background(), "a1", "closing 3 positions", map[string]any{"step": 2})
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if path != "/c/env_test/actions/a1/update" {
		t.Fatalf("path: %q", path)
	}
	if captured["message"] != "closing 3 positions" {
		t.Fatalf("captured: %+v", captured)
	}
	data, ok := captured["data"].(map[string]any)
	if !ok || data["step"] != float64(2) {
		t.Fatalf("data: %+v", captured["data"])
	}
	if len(action.Updates) != 1 || action.Updates[0].Message != "closing 3 positions" {
		t.Fatalf("updates: %+v", action.Updates)
	}
}

func TestConsumeProcessesAction(t *testing.T) {
	var listCount int32
	var completed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions"):
			n := atomic.AddInt32(&listCount, 1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"actions":[{"id":"a1","type":"noop","status":"pending"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"actions":[]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/ack"):
			_, _ = w.Write([]byte(`{"id":"a1","status":"acknowledged"}`))
		case strings.HasSuffix(r.URL.Path, "/complete"):
			completed.Store(true)
			_, _ = w.Write([]byte(`{"id":"a1","status":"completed"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- c.Actions.Consume(ctx, ConsumeOptions{
			PollInterval: 5 * time.Millisecond,
			Handler: func(_ context.Context, _ Action, _ ActionUpdater) (map[string]any, error) {
				return map[string]any{"filled": true}, nil
			},
		})
	}()

	deadline := time.After(2 * time.Second)
	for !completed.Load() {
		select {
		case <-deadline:
			t.Fatal("action was not completed in time")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume returned: %v", err)
	}
}

func TestConsumeFailsOnHandlerError(t *testing.T) {
	var failed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions"):
			_, _ = w.Write([]byte(`{"actions":[{"id":"a1","type":"noop","status":"pending"}]}`))
		case strings.HasSuffix(r.URL.Path, "/ack"):
			_, _ = w.Write([]byte(`{"id":"a1","status":"acknowledged"}`))
		case strings.HasSuffix(r.URL.Path, "/fail"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			result := body["result"].(map[string]any)
			if result["error"] != "boom" {
				t.Errorf("error result: %+v", result)
			}
			failed.Store(true)
			_, _ = w.Write([]byte(`{"id":"a1","status":"failed"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = c.Actions.Consume(ctx, ConsumeOptions{
			PollInterval: 5 * time.Millisecond,
			Handler: func(_ context.Context, _ Action, _ ActionUpdater) (map[string]any, error) {
				return nil, errBoom
			},
		})
	}()

	deadline := time.After(2 * time.Second)
	for !failed.Load() {
		select {
		case <-deadline:
			t.Fatal("action was not failed in time")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestConsumeSkipsOn409Ack(t *testing.T) {
	var handlerRan atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions"):
			_, _ = w.Write([]byte(`{"actions":[{"id":"a1","type":"noop","status":"pending"}]}`))
		case strings.HasSuffix(r.URL.Path, "/ack"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"already acknowledged"}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = c.Actions.Consume(ctx, ConsumeOptions{
		PollInterval: 5 * time.Millisecond,
		Handler: func(_ context.Context, _ Action, _ ActionUpdater) (map[string]any, error) {
			handlerRan.Store(true)
			return nil, nil
		},
	})

	if handlerRan.Load() {
		t.Fatal("handler should not have run after 409 ack")
	}
}

var errBoom = newErr("boom")

type stringErr string

func (e stringErr) Error() string { return string(e) }
func newErr(s string) error       { return stringErr(s) }
