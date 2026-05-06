// Package log writes structured JSON lines to ~/.drift/logs/drift.log.
// Format is versioned per the plan's structured logging section so log
// analysis tools can parse without scrubbing.
//
// Format:
//   {"v":1,"ts":"2026-05-05T12:34:56Z","level":"info","module":"relay",
//    "event":"handshake_complete","fields":{...}}
//
// Rotates at 10MB, keeps 5 generations.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level enum. Mirrors slog/zap conventions; we don't pull in either
// because our needs are simple and we want zero log dependencies.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// MaxFileSize is the rotation threshold. Files larger than this get
// rotated to .1, .2, etc on the next write. 10MB is generous for a
// local relay; revise once we have customer-volume telemetry.
const MaxFileSize = 10 * 1024 * 1024

// MaxGenerations is how many rotated files we keep before deleting.
const MaxGenerations = 5

// Format version. Bump if we ever change the JSON line shape; readers
// gate on "v" to know which fields exist.
const FormatVersion = 1

// LogPath returns ~/.drift/logs/drift.log. Pulled into a function so
// tests can override via $HOME.
func LogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".drift", "logs", "drift.log")
}

// Entry is one log record. Fields is optional context (key-value
// pairs); event names the action ("handshake_complete", "bind_failed").
type Entry struct {
	V      int            `json:"v"`
	TS     string         `json:"ts"`
	Level  Level          `json:"level"`
	Module string         `json:"module"`
	Event  string         `json:"event"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Logger writes Entry records to the log file. Thread-safe; Drift's
// goroutines (relay handler, heartbeat, hook handlers) all share one
// instance via the package-level Default().
type Logger struct {
	mu   sync.Mutex
	path string
	w    io.WriteCloser
}

var (
	defaultLogger *Logger
	defaultMu     sync.Mutex
)

// Default returns the process-wide Logger, creating it on first call.
// All callers go through this; we don't expose New() because there's
// no use case for multiple Loggers per process.
func Default() *Logger {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultLogger == nil {
		defaultLogger = &Logger{path: LogPath()}
	}
	return defaultLogger
}

// Info / Warn / Error / Debug are convenience wrappers around Log.
// The module string identifies which package issued the log; pick a
// short stable name (e.g. "relay", "hook", "install").
func Info(module, event string, fields map[string]any) {
	Default().Log(LevelInfo, module, event, fields)
}
func Warn(module, event string, fields map[string]any) {
	Default().Log(LevelWarn, module, event, fields)
}
func Error(module, event string, fields map[string]any) {
	Default().Log(LevelError, module, event, fields)
}
func Debug(module, event string, fields map[string]any) {
	Default().Log(LevelDebug, module, event, fields)
}

// Log writes a single entry. Best-effort: I/O errors are swallowed
// because logging failures should not propagate up to user-facing
// commands. The drift doctor command surfaces log file health so
// silent failures still get noticed.
func (l *Logger) Log(level Level, module, event string, fields map[string]any) {
	entry := Entry{
		V:      FormatVersion,
		TS:     time.Now().UTC().Format(time.RFC3339),
		Level:  level,
		Module: module,
		Event:  event,
		Fields: fields,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.ensureOpen(); err != nil {
		// Logging failed; nothing useful we can do here.
		return
	}
	if l.shouldRotate() {
		_ = l.rotate()
		_ = l.ensureOpen()
	}
	_, _ = l.w.Write(line)
}

// ensureOpen lazily opens the log file. Creates the parent dir if
// missing. No-op when the file is already open.
func (l *Logger) ensureOpen() error {
	if l.w != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(l.path), err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", l.path, err)
	}
	l.w = f
	return nil
}

// shouldRotate returns true if the current log file exceeds the size
// threshold. Caller already holds the mutex.
func (l *Logger) shouldRotate() bool {
	if l.w == nil {
		return false
	}
	st, err := os.Stat(l.path)
	if err != nil {
		return false
	}
	return st.Size() >= MaxFileSize
}

// rotate moves drift.log -> drift.log.1, drift.log.1 -> drift.log.2,
// etc, deleting drift.log.MaxGenerations when it overflows. Closes
// the current file handle so ensureOpen creates a fresh one.
func (l *Logger) rotate() error {
	if l.w != nil {
		_ = l.w.Close()
		l.w = nil
	}
	// Delete the oldest first so we don't trip over its existence
	// when we shift the chain.
	oldest := fmt.Sprintf("%s.%d", l.path, MaxGenerations)
	_ = os.Remove(oldest)
	for i := MaxGenerations - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", l.path, i)
		to := fmt.Sprintf("%s.%d", l.path, i+1)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(l.path); err == nil {
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes + closes the log file handle. Used by the service
// shutdown path; callers don't normally invoke this directly.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return nil
	}
	err := l.w.Close()
	l.w = nil
	return err
}

// Tail reads the last n lines of the log file and returns them in
// chronological order. Used by drift doctor for the diagnostic dump.
// Lossy on edge: if the file rotated between Tail's open and read, we
// return whatever's in the current file.
func Tail(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	path := LogPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	lines := splitLines(string(data))
	if len(lines) <= n {
		return lines, nil
	}
	return lines[len(lines)-n:], nil
}

// splitLines is a small helper (avoid bufio for one-shot parsing).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if start < i {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
