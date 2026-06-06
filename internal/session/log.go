package session

// Logger is the minimal logging surface the session layer needs. It is
// deliberately a subset of go-cli-builder's log.Logger, so the CLI can pass its
// logger straight through.
type Logger interface {
	Info(format string, a ...any)
	Warning(format string, a ...any)
	Error(format string, a ...any)
}

// NopLogger discards all logs; handy for tests and headless daemons.
type NopLogger struct{}

func (NopLogger) Info(string, ...any)    {}
func (NopLogger) Warning(string, ...any) {}
func (NopLogger) Error(string, ...any)   {}
