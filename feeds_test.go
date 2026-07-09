package confish

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestFeedSetSendsDataAndTTLSeconds(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fi_1","external_id":"sitemap-crawl","data":{"status":"running"},"expires_at":"2026-07-10T00:00:00Z","created_at":"2026-07-09T00:00:00Z","updated_at":"2026-07-09T00:00:00Z"}`))
	})
	c := newTestClient(t, srv.URL)

	item, err := c.Feed("jobs").Set(context.Background(), "sitemap-crawl", map[string]any{"status": "running"}, SetItemOptions{TTL: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if item.ID != "fi_1" || item.ExternalID != "sitemap-crawl" || item.Data["status"] != "running" {
		t.Fatalf("item: %+v", item)
	}
	if item.ExpiresAt != "2026-07-10T00:00:00Z" {
		t.Fatalf("expires_at: %q", item.ExpiresAt)
	}
	call := (*calls)[0]
	if call.Method != http.MethodPut {
		t.Fatalf("method: %s", call.Method)
	}
	if call.Path != "/c/env_test/feeds/jobs/items/sitemap-crawl" {
		t.Fatalf("path: %q", call.Path)
	}
	if call.Body["ttl"] != float64(86400) {
		t.Fatalf("ttl: %v", call.Body["ttl"])
	}
	data, ok := call.Body["data"].(map[string]any)
	if !ok || data["status"] != "running" {
		t.Fatalf("data: %+v", call.Body["data"])
	}
}

func TestFeedSetOmitsTTLWhenZero(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fi_1","external_id":"sitemap-crawl","expires_at":null}`))
	})
	c := newTestClient(t, srv.URL)

	item, err := c.Feed("jobs").Set(context.Background(), "sitemap-crawl", map[string]any{"status": "done"}, SetItemOptions{})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if item.ExpiresAt != "" {
		t.Fatalf("expires_at should be empty for permanent items: %q", item.ExpiresAt)
	}
	if _, present := (*calls)[0].Body["ttl"]; present {
		t.Fatalf("ttl key should be omitted when unset: %+v", (*calls)[0].Body)
	}
}

func TestFeedListReturnsItems(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"fi_2","external_id":"b","data":{"n":2}},{"id":"fi_1","external_id":"a","data":{"n":1}}]}`))
	})
	c := newTestClient(t, srv.URL)

	items, err := c.Feed("jobs").List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].ExternalID != "b" || items[1].ExternalID != "a" {
		t.Fatalf("items: %+v", items)
	}
	if (*calls)[0].Method != http.MethodGet {
		t.Fatalf("method: %s", (*calls)[0].Method)
	}
	if (*calls)[0].Path != "/c/env_test/feeds/jobs/items" {
		t.Fatalf("path: %q", (*calls)[0].Path)
	}
}

func TestFeedDelete(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newTestClient(t, srv.URL)

	if err := c.Feed("jobs").Delete(context.Background(), "sitemap-crawl"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if (*calls)[0].Method != http.MethodDelete {
		t.Fatalf("method: %s", (*calls)[0].Method)
	}
	if (*calls)[0].Path != "/c/env_test/feeds/jobs/items/sitemap-crawl" {
		t.Fatalf("path: %q", (*calls)[0].Path)
	}
}

func TestFeedReplaceSendsItemsAndDecodesCounts(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"updated":1,"deleted":3}`))
	})
	c := newTestClient(t, srv.URL)

	result, err := c.Feed("jobs").Replace(context.Background(), []FeedItemInput{
		{ExternalID: "sitemap-crawl", Data: map[string]any{"status": "running"}, TTL: time.Hour},
		{ExternalID: "index-rebuild", Data: map[string]any{"status": "queued"}},
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if result.Created != 1 || result.Updated != 1 || result.Deleted != 3 {
		t.Fatalf("result: %+v", result)
	}
	call := (*calls)[0]
	if call.Method != http.MethodPut {
		t.Fatalf("method: %s", call.Method)
	}
	if call.Path != "/c/env_test/feeds/jobs/items" {
		t.Fatalf("path: %q", call.Path)
	}
	items, ok := call.Body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items: %+v", call.Body["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok || first["external_id"] != "sitemap-crawl" || first["ttl"] != float64(3600) {
		t.Fatalf("first item: %+v", items[0])
	}
	data, ok := first["data"].(map[string]any)
	if !ok || data["status"] != "running" {
		t.Fatalf("first item data: %+v", first["data"])
	}
	second, ok := items[1].(map[string]any)
	if !ok || second["external_id"] != "index-rebuild" {
		t.Fatalf("second item: %+v", items[1])
	}
	if _, present := second["ttl"]; present {
		t.Fatalf("ttl key should be omitted when unset: %+v", second)
	}
}

func TestFeedReplaceEmptySliceClearsFeed(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":0,"updated":0,"deleted":7}`))
	})
	c := newTestClient(t, srv.URL)

	result, err := c.Feed("jobs").Replace(context.Background(), []FeedItemInput{})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if result.Deleted != 7 {
		t.Fatalf("result: %+v", result)
	}
	items, ok := (*calls)[0].Body["items"].([]any)
	if !ok {
		t.Fatalf("items should be an empty array, not absent or null: %+v", (*calls)[0].Body)
	}
	if len(items) != 0 {
		t.Fatalf("items: %+v", items)
	}
}

func TestFeedReplaceValidationError(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"invalid","errors":{"items.1.external_id":["Duplicate external_id."]}}`))
	})
	c := newTestClient(t, srv.URL)

	_, err := c.Feed("jobs").Replace(context.Background(), []FeedItemInput{
		{ExternalID: "a", Data: map[string]any{"n": 1}},
		{ExternalID: "a", Data: map[string]any{"n": 2}},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if got := ve.Errors["items.1.external_id"]; len(got) != 1 || got[0] != "Duplicate external_id." {
		t.Fatalf("errors: %+v", ve.Errors)
	}
}

func TestFeedNotFoundError(t *testing.T) {
	srv, _ := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Feed not found"}`))
	})
	c := newTestClient(t, srv.URL)

	_, err := c.Feed("nope").List(context.Background())
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
	if nf.Message != "Feed not found" {
		t.Fatalf("message: %q", nf.Message)
	}
}
