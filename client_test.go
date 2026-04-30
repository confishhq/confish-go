package confish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type recordedRequest struct {
	Method string
	Path   string
	Auth   string
	Body   map[string]any
}

func newTestServer(t *testing.T, handler func(req recordedRequest, w http.ResponseWriter)) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var calls []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		req := recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		}
		calls = append(calls, req)
		handler(req, w)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Options{
		EnvID:         "env_test",
		APIKey:        "confish_sk_test",
		BaseURL:       baseURL,
		MaxRetries:    1,
		MaxRetryDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestFetchDecodesIntoStruct(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"site_name":"My App","max_upload_mb":25,"maintenance_mode":false}`))
	})
	c := newTestClient(t, srv.URL)

	type Config struct {
		SiteName        string `json:"site_name"`
		MaxUploadMB     int    `json:"max_upload_mb"`
		MaintenanceMode bool   `json:"maintenance_mode"`
	}
	var got Config
	if err := c.Fetch(context.Background(), &got); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.SiteName != "My App" || got.MaxUploadMB != 25 || got.MaintenanceMode {
		t.Fatalf("unexpected config: %+v", got)
	}
	if (*calls)[0].Path != "/c/env_test" {
		t.Fatalf("path: %q", (*calls)[0].Path)
	}
	if (*calls)[0].Auth != "Bearer confish_sk_test" {
		t.Fatalf("auth: %q", (*calls)[0].Auth)
	}
}

func TestUpdateWrapsValues(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	c := newTestClient(t, srv.URL)

	err := c.Update(context.Background(), map[string]any{"maintenance_mode": true}, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if (*calls)[0].Method != http.MethodPatch {
		t.Fatalf("method: %s", (*calls)[0].Method)
	}
	values, ok := (*calls)[0].Body["values"].(map[string]any)
	if !ok || values["maintenance_mode"] != true {
		t.Fatalf("body: %+v", (*calls)[0].Body)
	}
}

func TestAuthErrorOn401(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Missing API key"}`))
	})
	c := newTestClient(t, srv.URL)

	var cfg map[string]any
	err := c.Fetch(context.Background(), &cfg)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if authErr.Message != "Missing API key" {
		t.Fatalf("message: %q", authErr.Message)
	}
}

func TestValidationErrorExposesFieldErrors(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"invalid","errors":{"values.max_upload_mb":["Must be at most 100."]}}`))
	})
	c := newTestClient(t, srv.URL)

	err := c.Update(context.Background(), map[string]any{"x": 1}, nil)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if got := ve.Errors["values.max_upload_mb"]; len(got) != 1 || got[0] != "Must be at most 100." {
		t.Fatalf("errors: %+v", ve.Errors)
	}
}

func TestRateLimitRetriesThenSucceeds(t *testing.T) {
	var attempts int
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c := newTestClient(t, srv.URL)

	var got map[string]any
	if err := c.Fetch(context.Background(), &got); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts: %d", attempts)
	}
}

func TestRateLimitExhaustsRetries(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "0")
		w.Header().Set("X-RateLimit-Limit", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"limited"}`))
	})
	c := newTestClient(t, srv.URL)

	var got map[string]any
	err := c.Fetch(context.Background(), &got)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected *RateLimitError, got %T", err)
	}
	if rl.Limit != 60 {
		t.Fatalf("limit: %d", rl.Limit)
	}
}

func TestLogReturnsID(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"log_123"}`))
	})
	c := newTestClient(t, srv.URL)

	id, err := c.Log(context.Background(), LogEntry{Level: LevelInfo, Message: "hi"})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if id != "log_123" {
		t.Fatalf("id: %s", id)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{APIKey: "k"}); err == nil {
		t.Fatal("expected error for missing EnvID")
	}
	if _, err := New(Options{EnvID: "e"}); err == nil {
		t.Fatal("expected error for missing APIKey")
	}
}

// Just to silence the unused-import warning of strconv in CI variants.
var _ = strconv.Itoa
