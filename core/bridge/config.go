// Package bridge is the translator: a Java Edition client on one side, a gophertunnel Bedrock
// client on the other (connected to the Bedrock server the plugin runs in, over loopback), and
// per player only the state translation needs. See docs/ARCHITECTURE.md.
//
// The package started as a copy of an existing standalone Java-to-Bedrock bridge; see
// docs/ORIGIN.md for what was taken over and what changed.
package bridge

import (
	"context"
	"github.com/google/uuid"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
	v776 "github.com/df-mc/dragonfly/server/java/protocol/v776"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// ProductName is how the bridge names itself to players and in logs.
const ProductName = "javabridge"

// Config configures the bridge.
type Config struct {
	Log *slog.Logger
	// Target is the Bedrock server every Java player is connected to ("host:port", RakNet;
	// "tcp:host:port" for gophertunnel's TCP transport).
	Target string
	// DataDir holds what the bridge keeps on disk (skins, device ids, pack cache). "" = none:
	// nothing is cached.
	DataDir      string
	ViewDistance int
	// LangFile is an optional en_US.lang to translate server texts with (the built-in copy
	// otherwise).
	LangFile string
	// Identity produces the Bedrock login of a Java player (identity.go). nil = offline logins
	// as the Java name.
	Identity IdentityProvider
	// Options are free-form settings (packs, debug switches); see option().
	Options map[string]string
}

var cfg Config

// option is a setting from Config.Options (lower-case key), or the environment variable
// JB_<KEY> for debugging, or def.
func option(key, def string) string {
	if v, ok := cfg.Options[key]; ok && v != "" {
		return v
	}
	if v := os.Getenv("JB_" + strings.ToUpper(key)); v != "" {
		return v
	}
	return def
}

// running is what Run started, for Stop.
var running struct {
	mu      sync.Mutex
	l       *jserver.Listener
	players sync.WaitGroup
	stopped chan struct{}
}

// Run serves the Java clients the listener accepts until it is closed (Stop).
func Run(c Config, l *jserver.Listener) {
	cfg = c
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Identity == nil {
		cfg.Identity = OfflineIdentity{}
	}
	if cfg.ViewDistance < 2 || cfg.ViewDistance > 32 {
		cfg.ViewDistance = 10
	}
	resetPackConfig()
	debugChunks = option("debug_chunks", "") == "1"
	debugPackets = option("debug_packets", "") != ""
	captureDir = option("capture_dir", "")
	running.mu.Lock()
	running.l = l
	running.stopped = make(chan struct{})
	running.mu.Unlock()
	defer close(running.stopped)
	for {
		jp, err := l.Accept()
		if err != nil {
			return
		}
		running.players.Add(1)
		goSafe("java player", func() {
			defer running.players.Done()
			serve(jp)
		}, nil)
	}
}

// Stop closes the listener, disconnects every player with reason and waits until their
// connections ended or ctx is done.
func Stop(ctx context.Context, reason string) error {
	running.mu.Lock()
	l, stopped := running.l, running.stopped
	running.l = nil
	running.mu.Unlock()
	if l != nil {
		_ = l.Close()
	}
	players.Range(func(_, v any) bool {
		p := v.(*Player)
		p.java.Disconnect(reason)
		p.mu.Lock()
		r := p.rem
		p.mu.Unlock()
		if r != nil {
			r.close("stopping")
		}
		return true
	})
	stopPackServer()
	done := make(chan struct{})
	go func() {
		running.players.Wait()
		if stopped != nil {
			<-stopped
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Debug switches.
var (
	debugChunks  bool
	debugPackets bool
)

// wireReader is the Java packet reader handlers get.
type wireReader = wire.Reader

// handleLegacyJava handles packets of older client versions that 26.3 does not have.
func (p *Player) handleLegacyJava(id int32, body []byte) error {
	if p.ver().Protocol == 776 && id == v776.ServerboundPlaySwing && p.rem != nil {
		// 26.2 sends swing for every arm swing (26.3 sends punch for a swing at the air only): show
		// the swing, without the missed-swing flag.
		p.Write(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: p.rem.gd.EntityRuntimeID})
	}
	return nil
}

// Online is the number of Java players on the bridge.
func Online() int {
	n := 0
	players.Range(func(_, _ any) bool { n++; return true })
	return n
}

// PlayerInfo describes a connected Java player (for the host).
type PlayerInfo struct {
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
	Protocol  int32  `json:"protocol"`
	Address   string `json:"address"`
	Connected bool   `json:"connected"` // logged in on the Bedrock server
}

// Players lists the connected Java players.
func Players() []PlayerInfo {
	var out []PlayerInfo
	players.Range(func(_, v any) bool {
		p := v.(*Player)
		p.mu.Lock()
		connected := p.rem != nil
		p.mu.Unlock()
		out = append(out, PlayerInfo{Name: p.name, UUID: uuid.UUID(p.profile.UUID).String(), Protocol: p.jp.Protocol,
			Address: p.jp.Conn.NetConn().RemoteAddr().String(), Connected: connected})
		return true
	})
	return out
}

// goSafe runs f on a new goroutine; a panic is logged (with the stack) and passed to onPanic
// instead of ending the process the bridge runs in.
func goSafe(name string, f func(), onPanic func(any)) {
	go func() {
		defer recoverTo(name, onPanic)
		f()
	}()
}

// recoverTo is deferred at the top of goroutines and callbacks: it logs a panic and calls
// onPanic (may be nil).
func recoverTo(name string, onPanic func(any)) {
	r := recover()
	if r == nil {
		return
	}
	cfg.Log.Error("panic", "in", name, "panic", r, "stack", string(debug.Stack()))
	if onPanic != nil {
		defer func() { _ = recover() }()
		onPanic(r)
	}
}

// goPlayer runs f on a goroutine for p: a panic disconnects only p.
func (p *Player) goSafe(name string, f func()) {
	goSafe(name, f, func(any) { p.internalError() })
}

// after runs f after d on its own goroutine like time.AfterFunc; a panic disconnects only p.
func (p *Player) after(d time.Duration, name string, f func()) *time.Timer {
	return time.AfterFunc(d, func() {
		defer recoverTo(name, func(any) { p.internalError() })
		f()
	})
}

// internalError ends the player's session after a panic in its translator.
func (p *Player) internalError() {
	p.java.Disconnect(ProductName + ": internal error (logged on the server)")
	// The panic may have left p.mu locked (a handler that locks without defer): never wait
	// for it here.
	if !p.mu.TryLock() {
		p.log.Error("translator lock still held after a panic")
		return
	}
	r := p.rem
	p.mu.Unlock()
	if r != nil {
		r.close("internal error")
	}
}
