package confish

import (
	"context"
	"fmt"
	"net/http"
)

// LogLevel enumerates the supported log levels (the full RFC 5424 set).
type LogLevel string

const (
	LevelDebug     LogLevel = "debug"
	LevelInfo      LogLevel = "info"
	LevelNotice    LogLevel = "notice"
	LevelWarning   LogLevel = "warning"
	LevelError     LogLevel = "error"
	LevelCritical  LogLevel = "critical"
	LevelAlert     LogLevel = "alert"
	LevelEmergency LogLevel = "emergency"
)

// MaxLogBatchSize is the maximum number of entries the server accepts in a
// single WriteBatch request.
const MaxLogBatchSize = 100

// LogEntry is a single log line, used both for POST /c/{env}/log (Write) and
// as one element of a POST /c/{env}/logs batch (WriteBatch).
type LogEntry struct {
	Level   LogLevel       `json:"level"`
	Message string         `json:"message"`
	Context map[string]any `json:"context,omitempty"`
	// Timestamp is an optional RFC3339 time for the entry. When empty the
	// server stamps arrival time — set it whenever sending is deferred (the
	// slog handler always records the original log time here, because its
	// flushes are delayed).
	Timestamp string `json:"timestamp,omitempty"`
}

// Logs wraps the log ingestion endpoint, with convenience methods for each level.
type Logs struct {
	client *Client
}

// Write sends a log entry to confish and returns the new entry's ID.
func (l *Logs) Write(ctx context.Context, entry LogEntry) (string, error) {
	var resp struct {
		ID string `json:"id"`
	}
	if err := l.client.do(ctx, http.MethodPost, "/c/"+l.client.envID+"/log", entry, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// WriteBatch sends up to MaxLogBatchSize entries in a single request
// (POST /c/{env}/logs) and returns the new entries' IDs, in input order.
// Passing more than MaxLogBatchSize entries fails fast without making a
// request — chunk larger batches yourself (the slog handler already does).
// An empty slice is a no-op: no request, nil IDs, nil error.
func (l *Logs) WriteBatch(ctx context.Context, entries []LogEntry) ([]string, error) {
	if len(entries) > MaxLogBatchSize {
		return nil, fmt.Errorf("confish: WriteBatch accepts at most %d entries per request, got %d", MaxLogBatchSize, len(entries))
	}
	if len(entries) == 0 {
		return nil, nil
	}
	var resp struct {
		IDs []string `json:"ids"`
	}
	body := map[string]any{"entries": entries}
	if err := l.client.do(ctx, http.MethodPost, "/c/"+l.client.envID+"/logs", body, &resp); err != nil {
		return nil, err
	}
	return resp.IDs, nil
}

func (l *Logs) Debug(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelDebug, message, fields)
}
func (l *Logs) Info(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelInfo, message, fields)
}
func (l *Logs) Notice(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelNotice, message, fields)
}
func (l *Logs) Warning(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelWarning, message, fields)
}
func (l *Logs) Error(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelError, message, fields)
}
func (l *Logs) Critical(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelCritical, message, fields)
}
func (l *Logs) Alert(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelAlert, message, fields)
}
func (l *Logs) Emergency(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelEmergency, message, fields)
}

func (l *Logs) send(ctx context.Context, level LogLevel, message string, fields map[string]any) error {
	_, err := l.Write(ctx, LogEntry{Level: level, Message: message, Context: fields})
	return err
}
