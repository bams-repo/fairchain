package logging

import (
	"log/slog"
	"os"
)

var (
	L *slog.Logger

	// DebugMode is set by the -debug CLI flag. When true, subsystems emit
	// hyper-verbose diagnostic output covering block relay, peer topology,
	// sync state, and message flow. This goes beyond slog.LevelDebug by
	// enabling periodic dumps and per-message tracing.
	DebugMode bool
)

func init() {
	L = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Init replaces the global logger with one configured at the given level.
func Init(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	L = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	}))
	slog.SetDefault(L)
}

// EnableDebug sets DebugMode and forces log level to debug.
func EnableDebug() {
	DebugMode = true
	L = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(L)
}

// With returns a child logger with additional default attributes.
func With(args ...any) *slog.Logger {
	return L.With(args...)
}
