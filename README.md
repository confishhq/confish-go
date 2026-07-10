# confish-go

Official Go SDK for [confish](https://confi.sh) — typed configuration, actions, logs, feeds, and webhook verification.

- Standard-library only, no dependencies
- Context-aware HTTP with automatic retries on `429`/`5xx`
- Long-running action consumer with `context.Context` cancellation
- TTL-based feed publishing
- HMAC-SHA256 webhook verification

## Install

```sh
go get github.com/confishhq/confish-go
```

Requires Go 1.22+.

## Quick start

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/confishhq/confish-go"
)

type Config struct {
    SiteName        string   `json:"site_name"`
    MaxUploadMB     int      `json:"max_upload_mb"`
    MaintenanceMode bool     `json:"maintenance_mode"`
    AllowedOrigins  []string `json:"allowed_origins"`
}

func main() {
    client, err := confish.New(confish.Options{
        EnvID:  os.Getenv("CONFISH_ENV_ID"),
        APIKey: os.Getenv("CONFISH_API_KEY"),
    })
    if err != nil {
        log.Fatal(err)
    }

    var cfg Config
    if err := client.Config.Fetch(context.Background(), &cfg); err != nil {
        log.Fatal(err)
    }
    log.Printf("config: %+v", cfg)
}
```

## Reading and writing config

```go
// GET /c/{env_id}
var cfg Config
err := client.Config.Fetch(ctx, &cfg)

// PATCH — only listed fields change
err = client.Config.Update(ctx, map[string]any{"maintenance_mode": true}, &cfg)

// PUT — replaces everything; omitted fields reset to defaults
err = client.Config.Replace(ctx, Config{
    SiteName:        "My App",
    MaxUploadMB:     50,
    MaintenanceMode: false,
    AllowedOrigins:  []string{"https://example.com"},
}, &cfg)
```

The third argument receives the full updated configuration after a write. Pass `nil` if you don't need it.

> Write access must be enabled in environment settings before `Update` and `Replace` will work.

## Logging

```go
err := client.Logs.Info(ctx, "Worker started", map[string]any{"region": "eu-west-1"})
err = client.Logs.Error(ctx, "Job failed", map[string]any{"job_id": "abc"})

// Or write an entry directly (returns the new entry's ID):
id, err := client.Logs.Write(ctx, confish.LogEntry{
    Level:   confish.LevelInfo,
    Message: "User logged in",
    Context: map[string]any{"user_id": 123},
})
```

Levels: `LevelDebug`, `LevelInfo`, `LevelNotice`, `LevelWarning`, `LevelError`, `LevelCritical`, `LevelAlert`, `LevelEmergency`. They follow RFC 5424 (syslog), so they map cleanly onto `log/slog` levels.

## Actions

The action consumer polls for pending actions, acknowledges them, runs your handler, and reports completion or failure — including idempotent skip if another consumer claimed the same action first.

```go
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

err := client.Actions.Consume(ctx, confish.ConsumeOptions{
    PollInterval:    15 * time.Second, // base — defaults to 15s
    MaxPollInterval: 60 * time.Second, // adaptive backoff cap — defaults to 60s
    Concurrency:     2,
    Handler: func(ctx context.Context, action confish.Action, u confish.ActionUpdater) (map[string]any, error) {
        switch action.Type {
        case "place_order":
            var params struct {
                Symbol string  `json:"symbol"`
                Size   float64 `json:"size"`
            }
            if err := action.DecodeParams(&params); err != nil {
                return nil, err
            }
            _ = u.Progress(ctx, "Submitting order", map[string]any{"symbol": params.Symbol})
            // ... do work ...
            return map[string]any{"order_id": "abc123", "filled_price": 66980.0}, nil
        default:
            return nil, fmt.Errorf("unknown action type: %s", action.Type)
        }
    },
    OnError: func(err error, action confish.Action) {
        log.Printf("action %s: %v", action.ID, err)
    },
})
```

What happens automatically:
- A non-nil `result` becomes the action's `result` field on completion.
- Returning a non-nil `error` fails the action with `{"error": err.Error()}`.
- Returning `confish.ErrSkipAction` leaves the action acknowledged without completing or failing it.
- A `409 Conflict` on ack is silently skipped — safe to run multiple consumers.
- Cancelling `ctx` stops new work and waits for in-flight handlers to settle.
- After 3 consecutive empty polls the loop doubles its sleep up to `MaxPollInterval`, resetting to `PollInterval` the moment any action is processed. Idle consumers make ~240 requests/hour by default instead of polling flat-out.

You can also drive the lifecycle manually:

```go
actions, err := client.Actions.List(ctx)
_, err = client.Actions.Ack(ctx, "action_id")
_, err = client.Actions.Progress(ctx, "action_id", "closing 3 positions", map[string]any{"step": 2})
_, err = client.Actions.Complete(ctx, "action_id", map[string]any{"order_id": "abc"})
_, err = client.Actions.Fail(ctx, "action_id", map[string]any{"error": "timeout"})
```

## Feeds

Feeds hold live, TTL-scoped items — think job boards, open positions, active alerts. `client.Feed(slug)` returns a handle bound to one feed; no HTTP happens until you call a method on it.

```go
jobs := client.Feed("jobs")

// PUT — upserts (creates or fully replaces) the item keyed by external ID
item, err := jobs.Set(ctx, "sitemap-crawl", map[string]any{
    "status": "running",
    "pages":  1284,
}, confish.SetItemOptions{TTL: 24 * time.Hour})

// GET — the feed's live items, newest first
items, err := jobs.List(ctx)

// DELETE — idempotent; deleting a missing item still succeeds
err = jobs.Delete(ctx, "sitemap-crawl")
```

`TTL` is converted to whole seconds on the wire (the server accepts 1 second to 30 days). A zero `TTL` means the item is permanent — and because `Set` is a declarative full replace, leaving `TTL` unset also **clears** any TTL previously set on the item. External IDs may be at most 255 characters.

Sync-style cron jobs that recompute their full dataset can push it in one declarative request with `Replace` — the feed becomes exactly these items:

```go
result, err := jobs.Replace(ctx, []confish.FeedItemInput{
    {ExternalID: "sitemap-crawl", Data: map[string]any{"status": "running"}, TTL: 24 * time.Hour},
    {ExternalID: "index-rebuild", Data: map[string]any{"status": "queued"}},
})
log.Printf("created %d, updated %d, deleted %d", result.Created, result.Updated, result.Deleted)
```

Existing external IDs update in place, new ones are created, and anything absent is **deleted** — an empty slice clears the feed. The write is all-or-nothing: duplicate external IDs, payloads over your plan's item cap, or any schema-invalid item return `*confish.ValidationError` with nothing written.

An unknown feed slug returns `*confish.NotFoundError`; data that fails the feed's schema (or a full feed) returns `*confish.ValidationError`.

## Webhook verification

```go
import "github.com/confishhq/confish-go/webhook"

http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }
    payload, err := webhook.Verify(body, r.Header.Get("X-Confish-Signature"), os.Getenv("CONFISH_WEBHOOK_SECRET"), webhook.Options{})
    if err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }
    // handle payload.Event ...
    w.WriteHeader(http.StatusOK)
})
```

`Verify` parses and verifies in one operation: on success it returns the parsed `webhook.Payload`; on failure the error tells you why — `webhook.ErrInvalidSignature` (missing, malformed, or mismatched signature) or `webhook.ErrTimestampOutsideTolerance` (possible replay).

It uses constant-time comparison and rejects timestamps older than 5 minutes by default. Override with `Options.Tolerance`, or pass `-1` to disable timestamp checking entirely. Always pass the **raw, unparsed body** — re-serializing parsed JSON breaks verification.

## Errors

Failed responses are returned as typed errors compatible with `errors.As`:

```go
var (
    rateLimit  *confish.RateLimitError
    validation *confish.ValidationError
    auth       *confish.AuthError
    notFound   *confish.NotFoundError
)

err := client.Config.Fetch(ctx, &cfg)
switch {
case errors.As(err, &rateLimit):
    log.Printf("retry after %ds", rateLimit.RetryAfter)
case errors.As(err, &validation):
    for field, msgs := range validation.Errors {
        log.Printf("%s: %v", field, msgs)
    }
case errors.As(err, &auth):
    log.Printf("auth failed: %s", auth.Message)
case errors.As(err, &notFound):
    log.Printf("not found: %s", notFound.Message)
}
```

Network errors (DNS, TLS, refused connections) come back as `*confish.NetworkError`.

By default the client retries `429` (honoring `Retry-After`) and `5xx` responses up to twice. Tune with `Options.MaxRetries`.

## Options

```go
client, err := confish.New(confish.Options{
    EnvID:         "a1b2c3d4e5f6",
    APIKey:        "confish_sk_...",
    BaseURL:       confish.DefaultBaseURL,    // override for self-hosted
    HTTPClient:    &http.Client{Timeout: 10*time.Second}, // injected transport
    UserAgent:     "my-app/1.0",
    MaxRetries:    2,
    MaxRetryDelay: 30 * time.Second,
})
```

## License

MIT
