package grokregister

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// Optional UI sink: when set, Logf dual-writes to std log and the sink
// (used by GrokRegisterService so admin terminal shows Step/代理预检 lines).
var (
	logSinkMu sync.RWMutex
	logSink   func(string)
)

// SetLogSink installs a line sink for Logf. Pass nil to clear.
// Safe for concurrent Logf; sink should not block long.
func SetLogSink(fn func(string)) {
	logSinkMu.Lock()
	logSink = fn
	logSinkMu.Unlock()
}

// Logf is the package logger. Always goes to the standard logger; when a sink
// is installed it also receives the formatted line (no trailing newline).
func Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// Keep process stderr / container logs.
	log.Print(msg)
	logSinkMu.RLock()
	fn := logSink
	logSinkMu.RUnlock()
	if fn != nil {
		// Strip common stdlog noise if callers accidentally pass multi-line.
		line := strings.TrimRight(msg, "\r\n")
		if line != "" {
			fn(line)
		}
	}
}
