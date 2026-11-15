// Package host is the part of the core that talks to the process hosting it (the Endstone
// plugin): one running instance at a time, its configuration, and a bounded log queue that the
// host drains by polling. Nothing here calls back into the host: the Go runtime never enters
// Python, the host always calls in.
package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Level of a log record, as the host's logger names it.
type Level string

const (
	Debug Level = "DEBUG"
	Info  Level = "INFO"
	Warn  Level = "WARNING"
	Error Level = "ERROR"
)

// Record is one log line for the host.
type Record struct {
	Level Level  `json:"level"`
	Msg   string `json:"msg"`
}

// maxQueued is how many records wait for the host before the oldest are dropped.
const maxQueued = 4096

// Logs is the queue the host polls. It never blocks a writer.
type Logs struct {
	mu      sync.Mutex
	recs    []Record
	dropped int
	debug   bool
}

// Add queues a record, dropping the oldest when the host does not keep up.
func (l *Logs) Add(level Level, format string, args ...any) {
	if level == Debug && !l.debugOn() {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.mu.Lock()
	if len(l.recs) >= maxQueued {
		n := len(l.recs) / 4
		l.recs = append(l.recs[:0], l.recs[n:]...)
		l.dropped += n
	}
	l.recs = append(l.recs, Record{Level: level, Msg: msg})
	l.mu.Unlock()
}

// DebugOn reports whether debug records are kept.
func (l *Logs) DebugOn() bool { return l.debugOn() }

func (l *Logs) debugOn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.debug
}

// SetDebug turns debug records on or off.
func (l *Logs) SetDebug(on bool) {
	l.mu.Lock()
	l.debug = on
	l.mu.Unlock()
}

// Drain returns the queued records (oldest first) and empties the queue.
func (l *Logs) Drain() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recs) == 0 && l.dropped == 0 {
		return nil
	}
	out := l.recs
	if l.dropped > 0 {
		out = append([]Record{{Level: Warn, Msg: fmt.Sprintf("%d log lines dropped (host did not poll fast enough)", l.dropped)}}, out...)
		l.dropped = 0
	}
	l.recs = nil
	return out
}

// Encode turns records into the text the C interface hands out: one record per line,
// "LEVEL<TAB>message", with newlines inside a message escaped as "\n".
func Encode(recs []Record) string {
	var b strings.Builder
	for _, r := range recs {
		b.WriteString(string(r.Level))
		b.WriteByte('\t')
		b.WriteString(strings.ReplaceAll(r.Msg, "\n", `\n`))
		b.WriteByte('\n')
	}
	return b.String()
}

// Service is what the host runs: started once with the configuration, stopped once.
type Service interface {
	// Stop ends everything the service started and returns when it has, or when the
	// deadline passes.
	Stop(deadline time.Time) error
	// Call answers a request from the host (JSON in, JSON out), for anything beyond start/stop.
	Call(method string, params json.RawMessage) (any, error)
}

// Factory builds and starts a service from the host's JSON configuration.
type Factory func(cfg json.RawMessage, logs *Logs) (Service, error)

var (
	mu      sync.Mutex
	current Service
	factory Factory
	logs    = &Logs{}
	lastErr string
)

// Register sets the service the host starts. Called from the library's init.
func Register(f Factory) { factory = f }

// Log adds a record to the host's queue from anywhere.
func Log(level Level, format string, args ...any) { logs.Add(level, format, args...) }

// LogQueue is the shared queue (for services that pass it on).
func LogQueue() *Logs { return logs }

// ErrRunning is returned by Start when an instance is already running.
var ErrRunning = errors.New("already running: stop it first")

// Start starts the registered service. Panics are turned into errors.
func Start(cfg []byte) (err error) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while starting: %v\n%s", r, debug.Stack())
		}
		if err != nil {
			lastErr = err.Error()
		}
	}()
	if current != nil {
		return ErrRunning
	}
	if factory == nil {
		return errors.New("no service registered")
	}
	svc, err := factory(json.RawMessage(cfg), logs)
	if err != nil {
		return err
	}
	current = svc
	return nil
}

// Stop stops the running service, waiting at most timeout.
func Stop(timeout time.Duration) (err error) {
	mu.Lock()
	defer mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while stopping: %v\n%s", r, debug.Stack())
		}
		if err != nil {
			lastErr = err.Error()
		}
	}()
	if current == nil {
		return nil
	}
	svc := current
	current = nil
	return svc.Stop(time.Now().Add(timeout))
}

// Running reports whether a service is running.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return current != nil
}

// Call forwards a host request to the running service. The request is
// {"method": "...", "params": {...}}; the answer {"ok": true, "result": ...} or
// {"ok": false, "error": "..."}.
func Call(req []byte) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			out, _ = json.Marshal(map[string]any{"ok": false, "error": fmt.Sprintf("panic: %v", r)})
			Log(Error, "panic in call: %v\n%s", r, debug.Stack())
		}
	}()
	var in struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	answer := func(res any, err error) []byte {
		if err != nil {
			b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
			return b
		}
		b, err := json.Marshal(map[string]any{"ok": true, "result": res})
		if err != nil {
			b, _ = json.Marshal(map[string]any{"ok": false, "error": err.Error()})
		}
		return b
	}
	if err := json.Unmarshal(req, &in); err != nil {
		return answer(nil, fmt.Errorf("bad request: %w", err))
	}
	switch in.Method {
	case "goroutines":
		// Works whether or not a service runs: the leak check after stop.
		return answer(runtime.NumGoroutine(), nil)
	case "stacks":
		buf := make([]byte, 1<<20)
		return answer(string(buf[:runtime.Stack(buf, true)]), nil)
	}
	if in.Method == "debug" {
		var on bool
		_ = json.Unmarshal(in.Params, &on)
		logs.SetDebug(on)
		return answer(on, nil)
	}
	mu.Lock()
	svc := current
	mu.Unlock()
	if svc == nil {
		return answer(nil, errors.New("not running"))
	}
	return answer(svc.Call(in.Method, in.Params))
}

// DrainLogs returns the queued log text, "" when there is none.
func DrainLogs() string { return Encode(logs.Drain()) }

// LastError returns the last start/stop error and clears it.
func LastError() string {
	mu.Lock()
	defer mu.Unlock()
	e := lastErr
	lastErr = ""
	return e
}

// Go runs f on a new goroutine that can never take the host process down: a panic is logged
// and passed to onPanic (which may be nil).
func Go(name string, f func(), onPanic func(any)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log(Error, "panic in %s: %v\n%s", name, r, debug.Stack())
				if onPanic != nil {
					func() {
						defer func() { _ = recover() }()
						onPanic(r)
					}()
				}
			}
		}()
		f()
	}()
}
