package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"javabridge/core/bridge"
	"javabridge/core/host"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
)

func init() { bridgeStarter = startBridge }

// bridgeService is the running bridge: the Java listener and bridge.Run.
type bridgeService struct {
	logs *host.Logs
	l    *jserver.Listener
	done chan struct{}
}

func startBridge(cfg Config, logs *host.Logs) (host.Service, error) {
	log := slog.New(&logHandler{logs: logs})
	opt := func(k string) string { return cfg.Options[k] }
	view := 10
	if n, err := strconv.Atoi(opt("view_distance")); err == nil {
		view = n
	}
	motd := cfg.MOTD
	if motd == "" {
		motd = "A Bedrock server"
	}
	maxPlayers := cfg.MaxPlayers
	l, err := jserver.Listen(cfg.Listen, jserver.Config{
		CompressionThreshold: 256,
		Brand:                bridge.ProductName,
		OnlineMode:           strings.EqualFold(opt("online_mode"), "true"),
		Log:                  log.With("part", "java login"),
		Status: func() jserver.Status {
			return jserver.Status{MOTD: motd, MaxPlayers: maxPlayers, Online: bridge.Online()}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("java listener on %s: %w", cfg.Listen, err)
	}
	s := &bridgeService{logs: logs, l: l, done: make(chan struct{})}
	bc := bridge.Config{
		Log:          log,
		Target:       cfg.Bedrock,
		DataDir:      cfg.DataDir,
		ViewDistance: view,
		LangFile:     opt("lang_file"),
		Options:      cfg.Options,
	}
	host.Go("bridge", func() {
		defer close(s.done)
		bridge.Run(bc, l)
	}, nil)
	log.Info("Java listener started", "addr", l.Addr().String(), "bedrock", cfg.Bedrock)
	return s, nil
}

func (s *bridgeService) Stop(deadline time.Time) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	err := bridge.Stop(ctx, "The server is restarting.")
	select {
	case <-s.done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("bridge did not stop in time: %w", err)
	}
	return nil
}

func (s *bridgeService) Call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "players":
		return bridge.Players(), nil
	case "status":
		return map[string]any{"online": bridge.Online(), "listen": s.l.Addr().String()}, nil
	}
	return nil, errors.New("unknown method " + method)
}

// logHandler is a slog.Handler writing to the host's log queue: "message key=value ...".
type logHandler struct {
	logs  *host.Logs
	attrs []slog.Attr
	group string
}

func (h *logHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.logs.DebugOn() // debug follows jb_call("debug", ...)
}

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		b.WriteByte(' ')
		if h.group != "" {
			b.WriteString(h.group + ".")
		}
		b.WriteString(a.Key)
		b.WriteByte('=')
		v := a.Value.Resolve().String()
		if strings.ContainsAny(v, " \t\"") && !strings.Contains(v, "\n") {
			v = strconv.Quote(v)
		}
		b.WriteString(v)
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })
	lvl := host.Info
	switch {
	case r.Level >= slog.LevelError:
		lvl = host.Error
	case r.Level >= slog.LevelWarn:
		lvl = host.Warn
	case r.Level < slog.LevelInfo:
		lvl = host.Debug
	}
	h.logs.Add(lvl, "%s", b.String())
	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &n
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	n := *h
	if n.group != "" {
		name = n.group + "." + name
	}
	n.group = name
	return &n
}
