package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// sharedMemHandler wraps MemoryHandler so that all derived loggers
// (created via WithAttrs) still write to the same underlying record store.
// This makes it possible to test logging.With... helpers with MemoryHandler.
type sharedMemHandler struct {
	mem   *MemoryHandler
	attrs []slog.Attr
}

func newSharedMemHandler(level Level) *sharedMemHandler {
	return &sharedMemHandler{mem: NewMemoryHandler(level)}
}

func (h *sharedMemHandler) Enabled(ctx context.Context, level Level) bool {
	return h.mem.Enabled(ctx, level)
}

func (h *sharedMemHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, a := range h.attrs {
		r.AddAttrs(a)
	}
	return h.mem.Handle(ctx, r)
}

func (h *sharedMemHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &sharedMemHandler{mem: h.mem, attrs: newAttrs}
}

func (h *sharedMemHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"invalid", LevelInfo},
		{"DEBUG", LevelDebug},
		{"Info", LevelInfo},
		{"WARN", LevelWarn},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseLevel(tt.input)
			if got != tt.expected {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNewHandler(t *testing.T) {
	t.Run("text handler", func(t *testing.T) {
		h := NewHandler("info", "text", io.Discard)
		if _, ok := h.(*slog.TextHandler); !ok {
			t.Errorf("expected *slog.TextHandler, got %T", h)
		}
	})

	t.Run("json handler", func(t *testing.T) {
		h := NewHandler("debug", "json", io.Discard)
		if _, ok := h.(*slog.JSONHandler); !ok {
			t.Errorf("expected *slog.JSONHandler, got %T", h)
		}
	})

	t.Run("json handler case insensitive", func(t *testing.T) {
		h := NewHandler("info", "JSON", io.Discard)
		if _, ok := h.(*slog.JSONHandler); !ok {
			t.Errorf("expected *slog.JSONHandler for uppercase JSON, got %T", h)
		}
	})

	t.Run("level filtering", func(t *testing.T) {
		h := NewHandler("error", "text", io.Discard)
		if h.Enabled(context.Background(), LevelInfo) {
			t.Error("expected info to be disabled for error level")
		}
		if !h.Enabled(context.Background(), LevelError) {
			t.Error("expected error to be enabled")
		}
	})

	t.Run("default output nil", func(t *testing.T) {
		h := NewHandler("info", "text", nil)
		if h == nil {
			t.Error("expected handler to be created with nil output")
		}
	})
}

func TestInitAndDefault(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() {
		defaultLogger.Store(oldLogger)
		slog.SetDefault(oldLogger)
	}()

	var buf bytes.Buffer
	Init("info", "text", &buf)

	if Default() != defaultLogger.Load() {
		t.Error("Default() should return the logger set by Init")
	}
	if slog.Default() != defaultLogger.Load() {
		t.Error("slog.Default() should be set by Init")
	}

	// Attach a MemoryHandler to verify record capture via Default()
	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))
	slog.SetDefault(defaultLogger.Load())

	Info("init test message")
	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Message != "init test message" {
		t.Errorf("expected message 'init test message', got %q", records[0].Message)
	}
}

func TestWithComponent(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() { defaultLogger.Store(oldLogger) }()

	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))

	logger := WithComponent("test-component")
	logger.Info("component msg")

	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attrs[FieldComponent] != "test-component" {
		t.Errorf("expected attr %s=%q, got %v", FieldComponent, "test-component", records[0].Attrs[FieldComponent])
	}
}

func TestWithProvider(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() { defaultLogger.Store(oldLogger) }()

	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))

	logger := WithProvider("jail")
	logger.Info("provider msg")

	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attrs[FieldProvider] != "jail" {
		t.Errorf("expected attr %s=%q, got %v", FieldProvider, "jail", records[0].Attrs[FieldProvider])
	}
}

func TestWithInstance(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() { defaultLogger.Store(oldLogger) }()

	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))

	logger := WithInstance("jail", "myjail")
	logger.Info("instance msg")

	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attrs[FieldProvider] != "jail" {
		t.Errorf("expected attr %s=%q, got %v", FieldProvider, "jail", records[0].Attrs[FieldProvider])
	}
	if records[0].Attrs[FieldInstance] != "myjail" {
		t.Errorf("expected attr %s=%q, got %v", FieldInstance, "myjail", records[0].Attrs[FieldInstance])
	}
}

func TestWithVM(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() { defaultLogger.Store(oldLogger) }()

	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))

	logger := WithVM("qemu", "myvm")
	logger.Info("vm msg")

	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attrs[FieldProvider] != "qemu" {
		t.Errorf("expected attr %s=%q, got %v", FieldProvider, "qemu", records[0].Attrs[FieldProvider])
	}
	if records[0].Attrs[FieldVM] != "myvm" {
		t.Errorf("expected attr %s=%q, got %v", FieldVM, "myvm", records[0].Attrs[FieldVM])
	}
}

func TestContextLogging(t *testing.T) {
	mem := NewMemoryHandler(LevelDebug)
	logger := slog.New(mem)

	ctx := WithContext(context.Background(), logger)
	retrieved := FromContext(ctx)
	if retrieved != logger {
		t.Error("FromContext should return the exact logger that was stored")
	}

	// empty context should fallback to default logger
	nilLogger := FromContext(context.TODO())
	if nilLogger != defaultLogger.Load() {
		t.Error("FromContext with no stored logger should return the default logger")
	}
}

func TestWithFields(t *testing.T) {
	sh := newSharedMemHandler(LevelDebug)
	logger := slog.New(sh)

	ctx := WithContext(context.Background(), logger)
	fieldLogger := WithFields(ctx, "key1", "val1", "key2", 42)
	fieldLogger.Info("fields msg")

	records := sh.mem.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Attrs["key1"] != "val1" {
		t.Errorf("expected key1=val1, got %v", records[0].Attrs["key1"])
	}
	if records[0].Attrs["key2"] != int64(42) {
		t.Errorf("expected key2=int64(42), got %v (type %T)", records[0].Attrs["key2"], records[0].Attrs["key2"])
	}
}

func TestMemoryHandler(t *testing.T) {
	t.Run("Enabled", func(t *testing.T) {
		h := NewMemoryHandler(LevelWarn)
		if h.Enabled(context.Background(), LevelDebug) {
			t.Error("expected debug to be disabled when level is warn")
		}
		if h.Enabled(context.Background(), LevelInfo) {
			t.Error("expected info to be disabled when level is warn")
		}
		if !h.Enabled(context.Background(), LevelWarn) {
			t.Error("expected warn to be enabled")
		}
		if !h.Enabled(context.Background(), LevelError) {
			t.Error("expected error to be enabled")
		}
	})

	t.Run("Handle and Records", func(t *testing.T) {
		h := NewMemoryHandler(LevelDebug)
		r := slog.NewRecord(time.Now(), LevelInfo, "hello", 0)
		r.Add("foo", "bar")
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle failed: %v", err)
		}

		records := h.Records()
		if len(records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(records))
		}
		if records[0].Message != "hello" {
			t.Errorf("expected message 'hello', got %q", records[0].Message)
		}
		if records[0].Level != LevelInfo {
			t.Errorf("expected level Info, got %v", records[0].Level)
		}
		if records[0].Attrs["foo"] != "bar" {
			t.Errorf("expected attr foo=bar, got %v", records[0].Attrs["foo"])
		}
		if records[0].Time.IsZero() {
			t.Error("expected non-zero time")
		}
	})

	t.Run("Reset", func(t *testing.T) {
		h := NewMemoryHandler(LevelDebug)
		r := slog.NewRecord(time.Now(), LevelWarn, "warn msg", 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle failed: %v", err)
		}
		if len(h.Records()) != 1 {
			t.Fatal("expected 1 record before reset")
		}
		h.Reset()
		if len(h.Records()) != 0 {
			t.Errorf("expected 0 records after reset, got %d", len(h.Records()))
		}
	})

	t.Run("WithAttrs", func(t *testing.T) {
		h := NewMemoryHandler(LevelDebug)
		h2 := h.WithAttrs([]slog.Attr{slog.String("static", "value")})
		r := slog.NewRecord(time.Now(), LevelInfo, "msg", 0)
		r.Add("dynamic", "val")
		if err := h2.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle failed: %v", err)
		}

		// Derived handlers share the parent's record store, so the record
		// logged via h2 must also be visible from the original handler.
		if len(h.Records()) != 1 {
			t.Errorf("original handler should share the store and see 1 record, got %d", len(h.Records()))
		}

		mh, ok := h2.(*MemoryHandler)
		if !ok {
			t.Fatalf("expected *MemoryHandler, got %T", h2)
		}
		records := mh.Records()
		if len(records) != 1 {
			t.Fatalf("expected 1 record on new handler, got %d", len(records))
		}
		if records[0].Attrs["static"] != "value" {
			t.Errorf("expected static attr, got %v", records[0].Attrs["static"])
		}
		if records[0].Attrs["dynamic"] != "val" {
			t.Errorf("expected dynamic attr, got %v", records[0].Attrs["dynamic"])
		}
	})

	t.Run("WithGroup returns MemoryHandler", func(t *testing.T) {
		h := NewMemoryHandler(LevelDebug)
		h2 := h.WithGroup("group")
		if _, ok := h2.(*MemoryHandler); !ok {
			t.Errorf("expected *MemoryHandler from WithGroup, got %T", h2)
		}

		// The group name must qualify subsequent attribute keys.
		r := slog.NewRecord(time.Now(), LevelInfo, "grouped", 0)
		r.Add("k", "v")
		if err := h2.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle failed: %v", err)
		}
		records := h.Records()
		if len(records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(records))
		}
		if records[0].Attrs["group.k"] != "v" {
			t.Errorf("expected grouped attr group.k=v, got %v", records[0].Attrs)
		}
	})

	t.Run("level filtering ignores low level records", func(t *testing.T) {
		h := NewMemoryHandler(LevelWarn)
		infoRecord := slog.NewRecord(time.Now(), LevelInfo, "info", 0)
		warnRecord := slog.NewRecord(time.Now(), LevelWarn, "warn", 0)

		if err := h.Handle(context.Background(), infoRecord); err != nil {
			t.Fatalf("Handle(info) failed: %v", err)
		}
		if err := h.Handle(context.Background(), warnRecord); err != nil {
			t.Fatalf("Handle(warn) failed: %v", err)
		}

		records := h.Records()
		if len(records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(records))
		}
		if records[0].Message != "warn" {
			t.Errorf("expected only warn record, got %q", records[0].Message)
		}
	})
}

func TestGlobalFunctions(t *testing.T) {
	oldLogger := defaultLogger.Load()
	defer func() { defaultLogger.Store(oldLogger) }()

	sh := newSharedMemHandler(LevelDebug)
	defaultLogger.Store(slog.New(sh))

	Debug("debug msg", "k1", "v1")
	Info("info msg")
	Warn("warn msg")
	Error("error msg")

	records := sh.mem.Records()
	if len(records) != 4 {
		t.Fatalf("expected 4 records, got %d", len(records))
	}

	expected := []struct {
		level   Level
		message string
		hasAttr bool
		attrKey string
		attrVal any
	}{
		{LevelDebug, "debug msg", true, "k1", "v1"},
		{LevelInfo, "info msg", false, "", nil},
		{LevelWarn, "warn msg", false, "", nil},
		{LevelError, "error msg", false, "", nil},
	}

	for i, exp := range expected {
		if records[i].Level != exp.level {
			t.Errorf("record %d: expected level %v, got %v", i, exp.level, records[i].Level)
		}
		if records[i].Message != exp.message {
			t.Errorf("record %d: expected message %q, got %q", i, exp.message, records[i].Message)
		}
		if exp.hasAttr {
			if records[i].Attrs[exp.attrKey] != exp.attrVal {
				t.Errorf("record %d: expected attr %s=%v, got %v", i, exp.attrKey, exp.attrVal, records[i].Attrs[exp.attrKey])
			}
		}
	}
}

// TestWith covers the With() package-level function.
func TestWith(t *testing.T) {
	logger := With("key", "value")
	if logger == nil {
		t.Fatal("With() returned nil logger")
	}
}

// TestContextFunctions covers DebugContext, InfoContext, WarnContext, ErrorContext.
func TestContextFunctions(t *testing.T) {
	// These just delegate to defaultLogger; we only need them to not panic.
	ctx := context.Background()
	// Redirect defaultLogger to discard output to keep test output clean.
	old := defaultLogger.Load()
	defaultLogger.Store(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { defaultLogger.Store(old) })

	DebugContext(ctx, "debug with ctx", "k", "v")
	InfoContext(ctx, "info with ctx")
	WarnContext(ctx, "warn with ctx")
	ErrorContext(ctx, "error with ctx")
}
