// Package logging provides structured logging for Hospitus.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level represents a log level.
type Level = slog.Level

// Log levels.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Standard field names for consistent structured logging.
const (
	FieldProvider  = "provider"
	FieldInstance  = "instance"
	FieldVM        = "vm"
	FieldComponent = "component"
	FieldAction    = "action"
	FieldError     = "err"
	FieldTaskID    = "task_id"
)

type contextKey string

const loggerKey contextKey = "logger"

// defaultLogger holds the process-wide logger. It is stored in an
// atomic.Pointer so that Init (which may run concurrently with logging
// calls) never races with readers.
var defaultLogger atomic.Pointer[slog.Logger]

func init() {
	// Default initialization with text format and Info level.
	defaultLogger.Store(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: LevelInfo,
	})))
}

// NewHandler creates a new slog.Handler with the specified configuration.
func NewHandler(level, format string, output io.Writer) slog.Handler {
	if output == nil {
		output = os.Stdout
	}

	lvl := ParseLevel(level)

	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: lvl <= LevelDebug,
	}

	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(output, opts)
	}
	return slog.NewTextHandler(output, opts)
}

// Init initializes the global logger with the specified configuration.
func Init(level, format string, output io.Writer) {
	handler := NewHandler(level, format, output)
	logger := slog.New(handler)
	defaultLogger.Store(logger)
	slog.SetDefault(logger)
}

// ParseLevel parses a log level string.
func ParseLevel(level string) Level {
	switch strings.ToLower(level) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Default returns the default logger.
func Default() *slog.Logger {
	return defaultLogger.Load()
}

// With returns a logger with additional attributes.
func With(args ...any) *slog.Logger {
	return defaultLogger.Load().With(args...)
}

// WithComponent returns a logger configured for a specific component.
func WithComponent(component string) *slog.Logger {
	return defaultLogger.Load().With(FieldComponent, component)
}

// WithProvider returns a logger configured for a specific provider.
func WithProvider(provider string) *slog.Logger {
	return defaultLogger.Load().With(FieldProvider, provider)
}

// WithInstance returns a logger configured for a specific instance.
func WithInstance(provider, instance string) *slog.Logger {
	return defaultLogger.Load().With(FieldProvider, provider, FieldInstance, instance)
}

// WithVM returns a logger configured for a specific VM.
func WithVM(provider, vm string) *slog.Logger {
	return defaultLogger.Load().With(FieldProvider, provider, FieldVM, vm)
}

// Debug logs at debug level.
func Debug(msg string, args ...any) {
	defaultLogger.Load().Debug(msg, args...)
}

// Info logs at info level.
func Info(msg string, args ...any) {
	defaultLogger.Load().Info(msg, args...)
}

// Warn logs at warn level.
func Warn(msg string, args ...any) {
	defaultLogger.Load().Warn(msg, args...)
}

// Error logs at error level.
func Error(msg string, args ...any) {
	defaultLogger.Load().Error(msg, args...)
}

// DebugContext logs at debug level with context.
func DebugContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.Load().DebugContext(ctx, msg, args...)
}

// InfoContext logs at info level with context.
func InfoContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.Load().InfoContext(ctx, msg, args...)
}

// WarnContext logs at warn level with context.
func WarnContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.Load().WarnContext(ctx, msg, args...)
}

// ErrorContext logs at error level with context.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.Load().ErrorContext(ctx, msg, args...)
}

// FromContext returns the logger from the context, or the default logger if not found.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return defaultLogger.Load()
	}
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return logger
	}
	return defaultLogger.Load()
}

// WithContext returns a new context with the given logger attached.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// WithFields returns a logger from the context with additional fields, or the default logger with fields.
func WithFields(ctx context.Context, args ...any) *slog.Logger {
	return FromContext(ctx).With(args...)
}

// MemoryRecord represents a single log record captured by MemoryHandler.
type MemoryRecord struct {
	Level   Level
	Message string
	Attrs   map[string]any
	Time    time.Time
}

// memoryStore is the shared record store backing a MemoryHandler and every
// handler derived from it via WithAttrs/WithGroup. Sharing the store ensures
// records emitted by derived loggers remain observable from the parent.
type memoryStore struct {
	mu      sync.RWMutex
	records []MemoryRecord
}

// MemoryHandler is a slog.Handler that stores records in memory.
type MemoryHandler struct {
	store       *memoryStore
	level       Level
	attrs       []slog.Attr
	groupPrefix string
}

// NewMemoryHandler creates a new MemoryHandler.
func NewMemoryHandler(level Level) *MemoryHandler {
	return &MemoryHandler{
		store: &memoryStore{},
		level: level,
	}
}

// Enabled implements slog.Handler.
func (h *MemoryHandler) Enabled(_ context.Context, level Level) bool {
	return level >= h.level
}

// prefix qualifies an attribute key with the handler's active group path.
func (h *MemoryHandler) prefix(key string) string {
	return h.groupPrefix + key
}

// Handle implements slog.Handler.
func (h *MemoryHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.Enabled(ctx, r.Level) {
		return nil
	}

	attrs := make(map[string]any)
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[h.prefix(a.Key)] = a.Value.Any()
		return true
	})

	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	h.store.records = append(h.store.records, MemoryRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   attrs,
		Time:    r.Time,
	})
	return nil
}

// WithAttrs implements slog.Handler.
func (h *MemoryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	for _, a := range attrs {
		newAttrs = append(newAttrs, slog.Attr{Key: h.prefix(a.Key), Value: a.Value})
	}
	return &MemoryHandler{
		store:       h.store,
		level:       h.level,
		attrs:       newAttrs,
		groupPrefix: h.groupPrefix,
	}
}

// WithGroup implements slog.Handler.
func (h *MemoryHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &MemoryHandler{
		store:       h.store,
		level:       h.level,
		attrs:       h.attrs,
		groupPrefix: h.groupPrefix + name + ".",
	}
}

// Records returns a defensive copy of the captured records.
func (h *MemoryHandler) Records() []MemoryRecord {
	h.store.mu.RLock()
	defer h.store.mu.RUnlock()
	out := make([]MemoryRecord, len(h.store.records))
	copy(out, h.store.records)
	return out
}

// Reset clears the captured records.
func (h *MemoryHandler) Reset() {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	h.store.records = nil
}
