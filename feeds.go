package confish

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// FeedItem mirrors the JSON shape returned by the feeds API.
//
// ExpiresAt is empty for permanent items (no TTL).
type FeedItem struct {
	ID         string         `json:"id"`
	ExternalID string         `json:"external_id"`
	Data       map[string]any `json:"data"`
	ExpiresAt  string         `json:"expires_at"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
}

// SetItemOptions configure Feed.Set.
type SetItemOptions struct {
	// TTL is how long the item stays live before expiring, sent as whole seconds
	// on the wire (the server accepts 1 second to 30 days). Zero means permanent —
	// and because Set is a declarative full replace (PUT), leaving TTL unset also
	// clears any TTL previously set on the item.
	TTL time.Duration
}

// FeedItemInput is one item in a Feed.Replace payload.
type FeedItemInput struct {
	// ExternalID keys the item (max 255 characters). It travels in the JSON
	// body, so no path escaping applies.
	ExternalID string
	// Data is the item's payload, validated against the feed's schema.
	Data any
	// TTL is how long the item stays live before expiring, sent as whole
	// seconds on the wire only when set. Zero means permanent.
	TTL time.Duration
}

// FeedReplaceResult reports what a Feed.Replace call changed.
type FeedReplaceResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

// Feed is a handle bound to a single feed, addressed by slug. Obtain one via
// Client.Feed.
type Feed struct {
	client *Client
	slug   string
}

// Feed returns a handle bound to the feed with the given slug. Construction is
// local — no HTTP request is made until one of the handle's methods is called.
func (c *Client) Feed(slug string) *Feed {
	return &Feed{client: c, slug: slug}
}

// Set upserts (creates or fully replaces) the feed item keyed by externalID.
// externalID may be at most 255 characters. Returns *NotFoundError if the feed
// slug does not exist and *ValidationError if data fails the feed's schema or
// the feed is full.
func (f *Feed) Set(ctx context.Context, externalID string, data any, opts SetItemOptions) (FeedItem, error) {
	body := map[string]any{"data": data}
	if opts.TTL > 0 {
		body["ttl"] = int64(opts.TTL / time.Second)
	}
	var out FeedItem
	err := f.client.do(ctx, http.MethodPut, f.itemPath(externalID), body, &out)
	return out, err
}

// List returns the feed's live items, newest first.
func (f *Feed) List(ctx context.Context) ([]FeedItem, error) {
	var resp struct {
		Items []FeedItem `json:"items"`
	}
	if err := f.client.do(ctx, http.MethodGet, f.itemsPath(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// Delete removes the item keyed by externalID. Idempotent — deleting an item
// that does not exist still succeeds.
func (f *Feed) Delete(ctx context.Context, externalID string) error {
	return f.client.do(ctx, http.MethodDelete, f.itemPath(externalID), nil, nil)
}

// Replace declaratively replaces the environment's entire feed partition with
// items (collection PUT). Built for sync-style cron jobs that push their full
// dataset in one request: existing external IDs update in place, new ones are
// created, and anything absent is DELETED — an empty slice clears the feed.
//
// The write is all-or-nothing: duplicate external IDs, payloads over the
// plan's item cap, or any schema-invalid item return *ValidationError with
// nothing written. Per item, TTL follows the same rules as Set — whole
// seconds on the wire, omitted (permanent) when zero.
func (f *Feed) Replace(ctx context.Context, items []FeedItemInput) (FeedReplaceResult, error) {
	wire := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{"external_id": item.ExternalID, "data": item.Data}
		if item.TTL > 0 {
			entry["ttl"] = int64(item.TTL / time.Second)
		}
		wire = append(wire, entry)
	}
	var out FeedReplaceResult
	err := f.client.do(ctx, http.MethodPut, f.itemsPath(), map[string]any{"items": wire}, &out)
	return out, err
}

func (f *Feed) itemsPath() string {
	return "/c/" + f.client.envID + "/feeds/" + f.slug + "/items"
}

func (f *Feed) itemPath(externalID string) string {
	// External IDs are arbitrary user-supplied strings (spaces, unicode,
	// slashes) - escape so the path segment is always well-formed.
	return f.itemsPath() + "/" + url.PathEscape(externalID)
}
