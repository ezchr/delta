package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"delta/core/host"
)

// spike is the c-shared feasibility test (docs/STATUS.md, "Spike"): a TCP listener that
// greets and echoes, a periodic status line, and a "stress" call that exercises what could
// clash with the host process (preemption signals, GC, panics, writes to closed sockets).
type spike struct {
	logs *host.Logs
	ln   net.Listener
	quit chan struct{}
	wg   sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func startSpike(cfg Config, logs *host.Logs) (host.Service, error) {
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	s := &spike{logs: logs, ln: ln, quit: make(chan struct{}), conns: map[net.Conn]struct{}{}}
	s.wg.Add(2)
	host.Go("spike accept", func() { defer s.wg.Done(); s.accept() }, nil)
	host.Go("spike status", func() { defer s.wg.Done(); s.status() }, nil)
	logs.Add(host.Info, "spike listening on %s (GOMAXPROCS %d)", ln.Addr(), runtime.GOMAXPROCS(0))
	return s, nil
}

func (s *spike) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return
			default:
			}
			s.logs.Add(host.Warn, "accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		host.Go("spike conn", func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
				_ = c.Close()
			}()
			s.logs.Add(host.Info, "connection from %s", c.RemoteAddr())
			_, _ = fmt.Fprintf(c, "hello from the Go core inside the host process\n")
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				if sc.Text() == "panic" {
					panic("requested panic in a connection goroutine")
				}
				_, _ = fmt.Fprintf(c, "echo: %s\n", sc.Text())
			}
		}, nil)
	}
}

func (s *spike) status() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			s.logs.Add(host.Info, "status: %d goroutines, heap %d KiB, %d GCs", runtime.NumGoroutine(), m.HeapAlloc>>10, m.NumGC)
		}
	}
}

// stress runs for about d and returns what it did.
func (s *spike) stress(d time.Duration) map[string]any {
	res := map[string]any{}
	end := time.Now().Add(d)

	// Tight loops on every P: only asynchronous preemption (SIGURG) lets the GC stop them.
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0)*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x := 0
			for time.Now().Before(end) {
				for j := 0; j < 1e6; j++ {
					x += j
				}
			}
			_ = x
		}()
	}
	// Allocation churn and forced GCs meanwhile.
	allocs := 0
	for time.Now().Before(end) {
		b := make([][]byte, 0, 1024)
		for i := 0; i < 1024; i++ {
			b = append(b, make([]byte, 1024))
		}
		allocs += len(b)
		runtime.GC()
	}
	wg.Wait()
	res["allocs_kib"] = allocs

	// A panic on a goroutine started with host.Go must be logged, not kill the process.
	done := make(chan struct{})
	host.Go("stress panic", func() { panic("deliberate panic (spike)") }, func(any) { close(done) })
	select {
	case <-done:
		res["panic_recovered"] = true
	case <-time.After(2 * time.Second):
		res["panic_recovered"] = false
	}

	// A nil pointer dereference (SIGSEGV in Go code) must become a recoverable panic: the
	// host's crash handler (crashpad) must not see it.
	done2 := make(chan struct{})
	host.Go("stress nil deref", func() {
		var p *int
		*p = 1
	}, func(any) { close(done2) })
	select {
	case <-done2:
		res["nil_deref_recovered"] = true
	case <-time.After(2 * time.Second):
		res["nil_deref_recovered"] = false
	}

	// Writing to a socket the peer closed: EPIPE, and SIGPIPE must not reach the host.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		go func() {
			c, err := ln.Accept()
			if err == nil {
				_ = c.(*net.TCPConn).SetLinger(0)
				_ = c.Close()
			}
		}()
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			var werr error
			for i := 0; i < 50 && werr == nil; i++ {
				_, werr = c.Write(make([]byte, 64<<10))
			}
			res["broken_pipe_write"] = fmt.Sprint(werr)
			_ = c.Close()
		}
		_ = ln.Close()
	}
	// Writing after our own shutdown(SHUT_WR): the kernel answers EPIPE and raises SIGPIPE on
	// the writing thread, which must not end the host process.
	if ln2, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		go func() {
			if c, err := ln2.Accept(); err == nil {
				time.Sleep(time.Second)
				_ = c.Close()
			}
		}()
		if c, err := net.Dial("tcp", ln2.Addr().String()); err == nil {
			_ = c.(*net.TCPConn).CloseWrite()
			_, werr := c.Write([]byte("x"))
			res["epipe_write"] = fmt.Sprint(werr)
			_ = c.Close()
		}
		_ = ln2.Close()
	}
	res["goroutines"] = runtime.NumGoroutine()
	return res
}

func (s *spike) Call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "stress":
		var secs float64 = 3
		_ = json.Unmarshal(params, &secs)
		return s.stress(time.Duration(secs * float64(time.Second))), nil
	case "goroutines":
		return runtime.NumGoroutine(), nil
	}
	return nil, errors.New("unknown method " + method)
}

func (s *spike) Stop(deadline time.Time) error {
	close(s.quit)
	_ = s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.logs.Add(host.Info, "spike stopped (%d goroutines left)", runtime.NumGoroutine())
		return nil
	case <-time.After(time.Until(deadline)):
		return errors.New("spike: goroutines did not stop in time")
	}
}
