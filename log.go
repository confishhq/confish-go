package confish

import (
	"context"
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

// LogEntry is the payload for POST /c/{env}/log.
type LogEntry struct {
	Level   LogLevel       `json:"level"`
	Message string         `json:"message"`
	Context map[string]any `json:"context,omitempty"`
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
