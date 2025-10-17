package obs

import (
	"log"
)

type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger is a minimal logging interface for observability.
type Logger interface {
	Logf(level Level, format string, args ...interface{})
}

// NopLogger discards all logs.
type NopLogger struct{}

func (NopLogger) Logf(level Level, format string, args ...interface{}) {}

// StdLogger adapts the standard library logger.
type StdLogger struct {
	L    *log.Logger
	Min  Level
	Pref string // optional prefix per log line
}

func (s StdLogger) Logf(level Level, format string, args ...interface{}) {
	if s.L == nil {
		return
	}
	if level < s.Min {
		return
	}
	if s.Pref != "" {
		s.L.Printf("%s[%s] "+format, append([]interface{}{s.Pref, level.String()}, args...)...)
	} else {
		s.L.Printf("[%s] "+format, append([]interface{}{level.String()}, args...)...)
	}
}
