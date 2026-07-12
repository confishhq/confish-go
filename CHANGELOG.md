# Changelog

## v0.3.0 — 2026-07-12

### Added

- **slog adapter**: `NewSlogHandler(client, SlogHandlerOptions{...})` returns a `log/slog` Handler that ships records to confish in the background. Bounded in-memory queue (default 1000; oldest entries dropped on overflow), flushed when 50 entries are queued or every 5 seconds — whichever comes first — and chunked to at most 100 entries per request. Each entry carries the timestamp captured at log time. Groups flatten into dotted context keys (`job.id`). Includes explicit `Flush(ctx)` and `Close()` (final flush bounded by `CloseTimeout`), a `Dropped()` counter, and optional `OnError`/`OnDrop` callbacks. `Handle` never blocks on the network and never returns delivery errors into the caller's logging path. Safe for concurrent use.
- `SlogLevelNotice` (2), `SlogLevelCritical` (12), `SlogLevelAlert` (16), `SlogLevelEmergency` (20) — `slog.Level` constants in slog's numbering gaps, making all eight RFC 5424 levels reachable; in-between levels map down to the nearest named level.
- `Logs.WriteBatch(ctx, entries)` — batch log write (`POST /c/{env}/logs`), returning the new entries' IDs in input order. At most `MaxLogBatchSize` = 100 entries per request — larger batches fail fast client-side without making a request (aligned across all five SDKs); an empty slice is a no-op.
- `LogEntry.Timestamp` — optional RFC3339 timestamp; when empty the server stamps arrival time.

## v0.2.0 — 2026-07-09

Coordinated minor across all five confish SDKs: the new feeds resource, plus a one-time pre-adoption reshuffle of the existing surface.

### Added

- **Feeds**: `client.Feed(slug)` returns a bound handle (no HTTP on construction) with:
  - `Set(ctx, externalID, data, SetItemOptions{TTL: ...})` — upserts (creates or fully replaces) an item. `TTL` is a `time.Duration` sent as whole seconds; zero means permanent, and — because `Set` is a declarative full replace — also clears any TTL previously set on the item.
  - `List(ctx)` — the feed's live items, newest first.
  - `Delete(ctx, externalID)` — idempotent.
  - `Replace(ctx, []FeedItemInput)` — declarative whole-feed replace (collection PUT), built for sync-style cron jobs pushing their full dataset in one request. Existing external IDs update in place, new ones are created, anything absent is deleted, and an empty slice clears the feed; all-or-nothing on validation failure. Returns `FeedReplaceResult{Created, Updated, Deleted}`.
- `NotFoundError` (HTTP 404) in the shared error hierarchy, matched via `errors.As`. Applies to feeds, actions, and config alike.
- `Logs.Write(ctx, entry)` — writes a `LogEntry` directly and returns the new entry's ID (absorbs the removed `client.Log`).
- `Logs.Emergency(...)` and `LevelEmergency`, completing the RFC 5424 level set.

### Breaking

- **Config namespace**: `client.Fetch` / `client.Update` / `client.Replace` moved to the `client.Config` sub-resource — `client.Config.Fetch(ctx, &cfg)` etc. Signatures are unchanged.
- **Logs consolidation**: the `client.Logger` field is now `client.Logs`, and the flat `client.Log(...)` method is removed in favor of `client.Logs.Write(...)`. Per-level methods are unchanged.
- **Webhook verify returns the payload**: `webhook.Verify(...)` now returns `(Payload, error)` — parsing and verification are one operation. On failure the error distinguishes `ErrInvalidSignature` from the new `ErrTimestampOutsideTolerance`.
- **`Actions.Update` renamed to `Actions.Progress`** (same wire call) — it appends a progress note to the action's timeline, it does not update the action. The `ActionUpdater` interface method used by consumer handlers is renamed to `Progress` accordingly.

## v0.1.0 — 2026-04-30

- Initial release: typed config fetch/update/replace, log ingestion, action consumer with adaptive backoff, HMAC-SHA256 webhook verification.
