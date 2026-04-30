package confish

import "context"

// LogLevel enumerates the supported log levels.
type LogLevel string

const (
	LevelDebug    LogLevel = "debug"
	LevelInfo     LogLevel = "info"
	LevelNotice   LogLevel = "notice"
	LevelWarning  LogLevel = "warning"
	LevelError    LogLevel = "error"
	LevelCritical LogLevel = "critical"
	LevelAlert    LogLevel = "alert"
)

// LogEntry is the payload for POST /c/{env}/log.
type LogEntry struct {
	Level   LogLevel       `json:"level"`
	Message string         `json:"message"`
	Context map[string]any `json:"context,omitempty"`
}

// Logger provides convenience methods around Client.Log for each level.
type Logger struct {
	client *Client
}

func (l *Logger) Debug(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelDebug, message, fields)
}
func (l *Logger) Info(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelInfo, message, fields)
}
func (l *Logger) Notice(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelNotice, message, fields)
}
func (l *Logger) Warning(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelWarning, message, fields)
}
func (l *Logger) Error(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelError, message, fields)
}
func (l *Logger) Critical(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelCritical, message, fields)
}
func (l *Logger) Alert(ctx context.Context, message string, fields map[string]any) error {
	return l.send(ctx, LevelAlert, message, fields)
}

func (l *Logger) send(ctx context.Context, level LogLevel, message string, fields map[string]any) error {
	_, err := l.client.Log(ctx, LogEntry{Level: level, Message: message, Context: fields})
	return err
}
