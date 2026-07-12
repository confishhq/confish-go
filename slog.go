package confish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// slog.Level values for the RFC 5424 levels that log/slog does not name.
// Together with the four built-in slog levels, all eight confish levels are
// reachable: slog.LevelDebug (-4), slog.LevelInfo (0), SlogLevelNotice (2),
// slog.LevelWarn (4), slog.LevelError (8), SlogLevelCritical (12),
// SlogLevelAlert (16), SlogLevelEmergency (20). Any in-between level maps to
// the nearest named level below it.
const (
	SlogLevelNotice    = slog.Level(2)
	SlogLevelCritical  = slog.Level(12)
	SlogLevelAlert     = slog.Level(16)
	SlogLevelEmergency = slog.Level(20)
)

// SlogHandlerOptions configure NewSlogHandler. The zero value gives sensible
// defaults for every field.
type SlogHandlerOptions struct {
	// Level is the minimum record level the handler accepts (checked in
	// Enabled). Pass a *slog.LevelVar to change it at runtime.
	// Default: slog.LevelInfo.
	Level slog.Leveler
	// QueueSize bounds the in-memory queue. When the queue is full the OLDEST
	// entries are dropped to make room (see Dropped and OnDrop).
	// Default: 1000.
	QueueSize int
	// FlushThreshold triggers a background flush as soon as this many entries
	// are queued. Default: 50.
	FlushThreshold int
	// FlushInterval flushes the queue on a timer regardless of volume, so
	// quiet periods still ship promptly. Default: 5 seconds.
	FlushInterval time.Duration
	// CloseTimeout bounds the final flush performed by Close.
	// Default: 5 seconds.
	CloseTimeout time.Duration
	// OnError is called when a flush fails after the client's built-in
	// retries; the affected entries are discarded and counted by Dropped.
	// Optional. Must not log through this handler (feedback loop).
	OnError func(err error)
	// OnDrop is called with the number of entries evicted when the queue
	// overflows. Optional. Must not log through this handler (feedback loop).
	OnDrop func(count int)
}

// SlogHandler is a log/slog Handler that ships records to confish in the
// background, so existing slog calls flow to confish as a sink:
//
//	handler, err := confish.NewSlogHandler(client, confish.SlogHandlerOptions{})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer handler.Close() // flushes anything still queued
//
//	slog.SetDefault(slog.New(handler))
//
// Records are queued in memory and flushed by a background goroutine — as
// soon as FlushThreshold entries are queued or every FlushInterval, whichever
// comes first — chunked to at most MaxLogBatchSize entries per request. Each
// entry carries the timestamp captured at log time (the record's time), so
// delayed flushes don't skew the entry's place on the timeline.
//
// Handle never blocks on the network, never panics, and never returns an
// error into the caller's logging path; delivery failures surface through
// OnError. Handlers derived via WithAttrs and WithGroup share the queue and
// goroutine of the handler they came from — one Close covers them all.
//
// SlogHandler is safe for concurrent use.
type SlogHandler struct {
	core   *slogCore
	prefix string         // dotted group prefix accumulated by WithGroup
	attrs  map[string]any // flattened attrs from WithAttrs; treated as immutable
}

var _ slog.Handler = (*SlogHandler)(nil)

// slogCore is the queue and flusher shared by a handler and everything
// derived from it via WithAttrs/WithGroup.
type slogCore struct {
	logs           *Logs
	level          slog.Leveler
	queueSize      int
	flushThreshold int
	closeTimeout   time.Duration
	onError        func(err error)
	onDrop         func(count int)

	mu     sync.Mutex // guards queue and closed
	queue  []LogEntry
	closed bool

	sendMu  sync.Mutex // serializes flushes so entries ship in order
	dropped atomic.Uint64

	flushCh chan struct{}
	closeCh chan struct{}
	doneCh  chan struct{}
}

// NewSlogHandler constructs a SlogHandler backed by client and starts its
// background flusher. Call Close when done to stop the goroutine and flush
// whatever is still queued.
func NewSlogHandler(client *Client, opts SlogHandlerOptions) (*SlogHandler, error) {
	if client == nil {
		return nil, fmt.Errorf("confish: client is required")
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 1000
	}
	if opts.FlushThreshold <= 0 {
		opts.FlushThreshold = 50
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 5 * time.Second
	}
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = 5 * time.Second
	}

	core := &slogCore{
		logs:           client.Logs,
		level:          opts.Level,
		queueSize:      opts.QueueSize,
		flushThreshold: opts.FlushThreshold,
		closeTimeout:   opts.CloseTimeout,
		onError:        opts.OnError,
		onDrop:         opts.OnDrop,
		flushCh:        make(chan struct{}, 1),
		closeCh:        make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
	go core.run(opts.FlushInterval)
	return &SlogHandler{core: core}, nil
}

// Enabled reports whether records at level should be handled, honoring the
// configured minimum level.
func (h *SlogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.core.level.Level()
}

// Handle queues the record for background delivery. It never blocks beyond
// queue insertion and always returns nil — delivery failures surface via
// OnError, never through the caller's logging path.
func (h *SlogHandler) Handle(_ context.Context, record slog.Record) error {
	entry := LogEntry{
		Level:   logLevelFromSlog(record.Level),
		Message: record.Message,
	}
	if !record.Time.IsZero() {
		entry.Timestamp = record.Time.Format(time.RFC3339Nano)
	}

	fields := make(map[string]any, len(h.attrs)+record.NumAttrs())
	for k, v := range h.attrs {
		fields[k] = v
	}
	record.Attrs(func(attr slog.Attr) bool {
		flattenAttr(fields, h.prefix, attr)
		return true
	})
	if len(fields) > 0 {
		entry.Context = fields
	}

	h.core.enqueue(entry)
	return nil
}

// WithAttrs returns a handler whose entries carry attrs in their context,
// flattened under the current group prefix. The new handler shares the
// receiver's queue and flusher.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := make(map[string]any, len(h.attrs)+len(attrs))
	for k, v := range h.attrs {
		next[k] = v
	}
	for _, attr := range attrs {
		flattenAttr(next, h.prefix, attr)
	}
	return &SlogHandler{core: h.core, prefix: h.prefix, attrs: next}
}

// WithGroup returns a handler that prefixes subsequent attribute keys with
// name + "." — groups become dotted keys ("group.key") in the entry's
// context. The new handler shares the receiver's queue and flusher.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &SlogHandler{core: h.core, prefix: h.prefix + name + ".", attrs: h.attrs}
}

// Flush synchronously sends everything queued at the time of the call,
// chunked to at most MaxLogBatchSize entries per request. Entries that fail
// to send after the client's built-in retries are discarded, reported via
// OnError, counted by Dropped, and returned here joined into one error.
func (h *SlogHandler) Flush(ctx context.Context) error {
	return h.core.flush(ctx)
}

// Close stops the background flusher and sends whatever is still queued,
// bounded by CloseTimeout. Records handled after Close are dropped (and
// counted by Dropped). Close is idempotent; subsequent calls return nil.
func (h *SlogHandler) Close() error {
	return h.core.close()
}

// Dropped reports how many entries have been lost so far — evicted because
// the queue overflowed (oldest first), discarded because a flush failed after
// retries, or handled after Close.
func (h *SlogHandler) Dropped() uint64 {
	return h.core.dropped.Load()
}

func (c *slogCore) enqueue(entry LogEntry) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.dropped.Add(1)
		return
	}
	var evicted int
	for len(c.queue) >= c.queueSize {
		c.queue = c.queue[1:]
		evicted++
	}
	c.queue = append(c.queue, entry)
	pending := len(c.queue)
	c.mu.Unlock()

	if evicted > 0 {
		c.dropped.Add(uint64(evicted))
		c.notifyDrop(evicted)
	}
	if pending >= c.flushThreshold {
		select {
		case c.flushCh <- struct{}{}:
		default:
		}
	}
}

// run is the background flusher: it drains the queue when signalled by
// enqueue (FlushThreshold reached) or on every tick, whichever comes first.
func (c *slogCore) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.flushCh:
			_ = c.flush(context.Background())
		case <-ticker.C:
			_ = c.flush(context.Background())
		case <-c.closeCh:
			close(c.doneCh)
			return
		}
	}
}

// flush drains the queue and ships it in order, chunked to at most
// MaxLogBatchSize entries per request. A flush with nothing queued sends no
// request. Failed chunks are not requeued — the client already retried
// 429/5xx internally — so a persistent outage cannot grow the queue without
// bound or block the caller's logging.
func (c *slogCore) flush(ctx context.Context) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.mu.Lock()
	batch := c.queue
	c.queue = nil
	c.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}

	var errs []error
	for start := 0; start < len(batch); start += MaxLogBatchSize {
		end := min(start+MaxLogBatchSize, len(batch))
		if _, err := c.logs.WriteBatch(ctx, batch[start:end]); err != nil {
			c.dropped.Add(uint64(end - start))
			c.notifyError(err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *slogCore) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	close(c.closeCh)
	<-c.doneCh

	ctx, cancel := context.WithTimeout(context.Background(), c.closeTimeout)
	defer cancel()
	return c.flush(ctx)
}

// notifyDrop and notifyError shield the handler from user callbacks: a panic
// inside a callback must never escape into the caller's logging path.

func (c *slogCore) notifyDrop(count int) {
	if c.onDrop == nil {
		return
	}
	defer func() { _ = recover() }()
	c.onDrop(count)
}

func (c *slogCore) notifyError(err error) {
	if c.onError == nil {
		return
	}
	defer func() { _ = recover() }()
	c.onError(err)
}

// logLevelFromSlog maps a slog.Level onto the RFC 5424 set by threshold:
// a level at or above a named level (but below the next) takes its name.
func logLevelFromSlog(level slog.Level) LogLevel {
	switch {
	case level >= SlogLevelEmergency:
		return LevelEmergency
	case level >= SlogLevelAlert:
		return LevelAlert
	case level >= SlogLevelCritical:
		return LevelCritical
	case level >= slog.LevelError:
		return LevelError
	case level >= slog.LevelWarn:
		return LevelWarning
	case level >= SlogLevelNotice:
		return LevelNotice
	case level >= slog.LevelInfo:
		return LevelInfo
	default:
		return LevelDebug
	}
}

// flattenAttr resolves attr and writes it into out, flattening groups with
// dotted keys ("group.key") so the entry's context stays a flat, queryable
// object. It follows the slog handler contract: empty attrs are ignored,
// empty groups are elided, and a group with an empty key is inlined.
func flattenAttr(out map[string]any, prefix string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		if len(group) == 0 {
			return
		}
		groupPrefix := prefix
		if attr.Key != "" {
			groupPrefix += attr.Key + "."
		}
		for _, member := range group {
			flattenAttr(out, groupPrefix, member)
		}
		return
	}
	if attr.Key == "" {
		return
	}
	out[prefix+attr.Key] = attrValue(attr.Value)
}

// attrValue converts a resolved slog.Value into a JSON-safe plain value.
// Durations and times become human-readable strings.
func attrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindBool:
		return v.Bool()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	default:
		return jsonSafe(v.Any())
	}
}

// jsonSafe returns val if it can be marshaled to JSON, otherwise a formatted
// string fallback — one bad attribute must never poison a whole batch.
// Errors are special-cased to their message (json.Marshal turns most of them
// into "{}").
func jsonSafe(val any) any {
	if err, ok := val.(error); ok {
		if _, isMarshaler := val.(json.Marshaler); !isMarshaler {
			return err.Error()
		}
	}
	if _, err := json.Marshal(val); err != nil {
		return fmt.Sprintf("%+v", val)
	}
	return val
}
