package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"javabridge/core/bridge"
	"javabridge/core/host"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
	"github.com/google/uuid"
)

func init() { bridgeStarter = startBridge }

// bridgeService is the running bridge: the Java listener and bridge.Run.
type bridgeService struct {
	logs   *host.Logs
	l      *jserver.Listener
	done   chan struct{}
	secret string // per-start trust secret the plugin reads with jb_call("identity_secret")
	namespace string
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
	// Account mode: "online" (premium, the default) verifies every Java login with Mojang
	// (encryption + session-server hasJoined) and gives a stable BDS identity via the hook;
	// "offline" lets anyone join under any name (cracked/private servers) and is opt-in.
	accountMode := strings.ToLower(strings.TrimSpace(opt("account_mode")))
	if accountMode == "" {
		// Back-compat: the old online_mode option, else the premium default.
		if v := strings.TrimSpace(opt("online_mode")); v != "" {
			if strings.EqualFold(v, "true") {
				accountMode = "online"
			} else {
				accountMode = "offline"
			}
		} else {
			accountMode = "online"
		}
	}
	premium := accountMode != "offline"
	// Velocity modern forwarding: behind a Velocity proxy, the proxy verifies the Mojang
	// account and forwards the verified profile signed with a shared secret. When set, the Java
	// listener trusts that instead of doing its own online handshake (the listener must then be
	// reachable only by the proxy). The identity provider (the XUID hook) is unchanged.
	var veloSecret []byte
	if v := strings.TrimSpace(opt("velocity_secret")); v != "" {
		veloSecret = []byte(v)
	} else if f := strings.TrimSpace(opt("velocity_secret_file")); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			veloSecret = []byte(strings.TrimSpace(string(b)))
		} else {
			log.Error("could not read velocity_secret_file", "file", f, "err", err)
		}
	}
	// Advanced/test overrides: run OnlineIdentity (the hook) while the Java listener stays
	// offline, so the hook can be exercised with an offline Java test client. Not for normal
	// use - a production server keeps both tied to account_mode.
	javaOnline := premium
	if v := strings.TrimSpace(opt("java_online")); v != "" {
		javaOnline = strings.EqualFold(v, "true")
	}
	useHook := premium
	if v := strings.TrimSpace(opt("identity_hook")); v != "" {
		useHook = strings.EqualFold(v, "true")
	}
	if !premium {
		log.Warn("account_mode=offline: Java logins are NOT verified with Mojang - any player can join "+
			"under any name, including staff names. Use online (the default) unless this is a private/cracked server.")
	}
	// Identity provider and the per-start trust secret the hook needs. Generated once per start
	// (so it changes on every reload); the plugin reads it with jb_call("identity_secret").
	var identity bridge.IdentityProvider = bridge.OfflineIdentity{}
	secret := ""
	namespaceStr := bridge.DefaultNamespace().String()
	if useHook {
		var err error
		if secret, err = bridge.NewSecret(); err != nil {
			return nil, fmt.Errorf("trust secret: %w", err)
		}
		ns := bridge.DefaultNamespace()
		if v := strings.TrimSpace(opt("namespace_uuid")); v != "" {
			if u, err := uuid.Parse(v); err == nil {
				ns = u
			} else {
				log.Warn("namespace_uuid is not a valid UUID, using the default", "value", v)
			}
		}
		identity = bridge.OnlineIdentity{Secret: secret, Namespace: ns}
		namespaceStr = ns.String()
	}
	l, err := jserver.Listen(cfg.Listen, jserver.Config{
		CompressionThreshold: 256,
		Brand:                bridge.ProductName,
		OnlineMode:           javaOnline && len(veloSecret) == 0,
		VelocitySecret:       veloSecret,
		SessionServer:        opt("session_server"),
		Log:                  log.With("part", "java login"),
		Status: func() jserver.Status {
			return jserver.Status{MOTD: motd, MaxPlayers: maxPlayers, Online: bridge.Online()}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("java listener on %s: %w", cfg.Listen, err)
	}
	s := &bridgeService{logs: logs, l: l, done: make(chan struct{}), secret: secret, namespace: namespaceStr}
	bc := bridge.Config{
		Log:          log,
		Target:       cfg.Bedrock,
		DataDir:      cfg.DataDir,
		ViewDistance: view,
		LangFile:     opt("lang_file"),
		Options:      cfg.Options,
		Identity:     identity,
	}
	host.Go("bridge", func() {
		defer close(s.done)
		bridge.Run(bc, l)
	}, nil)
	log.Info("Java listener started", "addr", l.Addr().String(), "bedrock", cfg.Bedrock, "account_mode", accountMode)
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
	case "identity_secret":
		// The per-start trust secret, for the loopback plugin hook. Empty in offline mode (no
		// hook needed). Returned only through this in-process call; never logged.
		return map[string]any{"secret": s.secret, "namespace": s.namespace}, nil
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
