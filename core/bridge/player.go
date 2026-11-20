package bridge

import (
	"context"
	"delta/core/internal/javapack"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	jchunk "github.com/ezchr/go-mcjava/chunk"
	jserver "github.com/ezchr/go-mcjava/server"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Player is one Java player: their Java connection, their connection to a Bedrock server (if
// any) and the translator state between the two. Everything the translator keeps is guarded by
// mu: every Bedrock and Java packet handler and every tick runs with it held, in order, so
// handlers never need locks of their own. Handlers must not block (writes to either side are
// queued); anything slow (dialing, HTTP) runs on its own goroutine and takes mu when done.
type Player struct {
	// hudMarkers are the current server pack's HUD markers (javapack.Result.HudMarkers): titles
	// holding one are not shown. Set when the server is dialled.
	hudMarkers []string
	// formScreens are the screens that pack draws for forms (javapack.Result.FormScreens).
	formScreens []javapack.FormScreen

	log     *slog.Logger
	java    *JavaConn
	jp      *jserver.Player
	profile jserver.Profile
	info    jserver.ClientInfo
	name    string
	// login is the Bedrock login the identity provider made for this player (identity.go).
	login *BedrockLogin

	viewDistance int32

	// connecting serialises connects.
	connecting atomic.Bool
	ping       pingState // ping.go (p.mu)
	packs      packState // resource packs (packs.go, p.mu)

	mu sync.Mutex
	// rem is the Bedrock connection (nil while waiting); target what it was dialled with.
	rem    *Remote
	target string
	note   string // a line shown once in the waiting area
	gone   bool   // the Java player left

	// The Java client's view of the game.
	javaDim       string // Java dimension the client is in
	javaMode      int32  // Java game mode last sent
	tpID          int32  // last player_position id
	centre        [2]int32
	centreSent    bool
	chunkRate     float64 // chunks per tick the client asked for
	batchInFlight bool
	batchSeq      uint64    // chunk batches sent
	batchAcked    uint64    // the last chunk batch the client acknowledged (it has its columns)
	keepAlive     int64     // id of the keep-alive waiting for an answer (0: none)
	keepAliveAt   time.Time // when it was sent
	javaLoaded    bool      // the client sent player_loaded since the last login/respawn
	inWaiting     bool      // the client is in the waiting area

	// Translator state, reset for every connection (and the world for every dimension).
	world   *World        // chunks of the dimension the player is in (world.go)
	self    selfState     // own state on the server (self.go)
	mv      movement      // own movement (movement.go)
	ents    *Entities     // entity id map (entities.go, milestone 2)
	win     *Windows      // windows and inventories (windows.go, milestone 3)
	cmds    *Commands     // the server's commands (commands.go, milestone 4)
	sb      *scoreboard   // the server's scoreboard objectives (scoreboard.go)
	text    textState     // titles and chat state (text.go)
	clock   levelClock    // time and weather (self.go)
	respawn respawnState  // a death waiting for its respawn (respawn.go)
	snd     soundState    // sound translation state (sounds.go)
	npc     npcState      // the open NPC dialogue (npc.go)
	cmdb    cmdBlockState // command blocks (cmdblock.go)

	// dimLoaded tells the server the client loaded the dimension it changed to (self.go).
	dimLoaded func()

	// afterInput are packets sent right after the next PlayerAuthInput (AfterInput).
	afterInput []packet.Packet

	// Java chunk encoding scratch (world.go).
	enc *jchunk.Encoder
	col jchunk.Column
	hm  []uint64
}

func newPlayer(jp *jserver.Player) *Player {
	log := cfg.Log.With("player", jp.Profile.Name)
	p := &Player{
		log:       log,
		java:      newJavaConn(jp, log),
		jp:        jp,
		profile:   jp.Profile,
		info:      jp.Info,
		name:      jp.Profile.Name,
		chunkRate: 9,
	}
	p.viewDistance = int32(cfg.ViewDistance)
	if v := int32(jp.Info.ViewDistance); v > 1 && v < p.viewDistance {
		p.viewDistance = v
	}
	p.ents = newEntities(p)
	p.win = newWindows(p)
	p.cmds = newCommands(p)
	return p
}

var players sync.Map // lowercase Java name -> *Player

// playerByName is the online player with this Java name.
func playerByName(name string) *Player {
	if v, ok := players.Load(strings.ToLower(name)); ok {
		return v.(*Player)
	}
	return nil
}

// message tells the player something from the bridge (safe without mu).
func (p *Player) message(format string, a ...any) { p.BridgeMessage(fmt.Sprintf(format, a...)) }

// serve runs a Java player from the end of configuration until they leave.
func serve(jp *jserver.Player) {
	p := newPlayer(jp)
	if old, loaded := players.Swap(strings.ToLower(p.name), p); loaded {
		old.(*Player).java.Disconnect("You logged in from somewhere else.")
	}
	defer players.CompareAndDelete(strings.ToLower(p.name), p)
	p.log.Info("java player joined", "protocol", jp.Protocol, "addr", jp.Conn.NetConn().RemoteAddr())

	login, err := cfg.Identity.BedrockLogin(context.Background(), JavaPlayer{
		Name: p.name, UUID: uuid.UUID(p.profile.UUID), Properties: p.profile.Properties, Locale: p.info.Locale,
		Address: jp.Conn.NetConn().RemoteAddr().String(),
	})
	if err != nil {
		p.log.Info("refused: no Bedrock login", "err", err)
		refuse(jp, err.Error())
		return
	}
	p.login = login
	defer p.shareTextures()()

	// Connect before the Java login, so the player spawns straight into the server's world; a
	// server that takes longer than firstDialWait (busy, still starting) is waited for in the
	// waiting area instead, where the Java client is not left without packets long enough to
	// time out.
	target := cfg.Target
	var rem *Remote
	loggedIn := false
	p.connecting.Store(true)
	type result struct {
		r   *Remote
		err error
	}
	dialed := make(chan result, 1)
	p.goSafe("dial", func() {
		r, err := p.dialRemote(target)
		dialed <- result{r, err}
	})
	select {
	case res := <-dialed:
		p.connecting.Store(false)
		if res.err != nil {
			p.log.Info("could not join the server", "why", dialErrorText(res.err), "err", res.err)
			refuse(jp, "Could not join the server: "+dialErrorText(res.err))
			return
		}
		rem = res.r
	case <-time.After(firstDialWait):
		p.mu.Lock()
		p.enterWaiting(true, "")
		p.mu.Unlock()
		loggedIn = true
		p.message("Joining the server...")
		p.goSafe("dial wait", func() {
			res := <-dialed
			p.connecting.Store(false)
			if res.err != nil {
				p.log.Info("could not join the server", "why", dialErrorText(res.err), "err", res.err)
				p.java.Disconnect("Could not join the server: " + dialErrorText(res.err))
				return
			}
			p.mu.Lock()
			if p.gone || p.rem != nil {
				p.mu.Unlock()
				res.r.close("not needed any more")
				return
			}
			p.enterRemote(res.r, false)
			p.mu.Unlock()
			res.r.startReading()
		})
	}
	if !loggedIn {
		p.mu.Lock()
		p.enterRemote(rem, true)
		p.mu.Unlock()
		rem.startReading()
	}
	p.run()

	p.mu.Lock()
	p.gone = true
	rem = p.rem
	p.rem = nil
	p.mu.Unlock()
	if rem != nil {
		rem.close("Java player left")
		rem.wait(5 * time.Second)
	}
	p.log.Info("java player left")
}

// refuse disconnects a player in the play state before the login packet.
func refuse(jp *jserver.Player, reason string) {
	cfg.Log.Info("refused java login", "name", jp.Profile.Name, "reason", reason)
	var w wire.Writer
	writeText(&w, reason)
	_ = jp.Conn.Send(jp.Version.ClientboundPlay(v777.ClientboundPlayDisconnect), w.B)
	_ = jp.Conn.Close()
}

// profileProperty is the value of the login profile's property name ("" if it has none).
func profileProperty(pr jserver.Profile, name string) string {
	for _, p := range pr.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// firstDialWait is how long a login waits for its server before going to the waiting area. It stays
// under the Java client's 30s no-packet timeout; a normal join takes a few seconds, so players
// spawn straight into the world and only a server that is still starting shows the waiting area.
const firstDialWait = 20 * time.Second

// keep-alive timing, as vanilla.
const (
	keepAliveInterval = 5 * time.Second
	keepAliveTimeout  = 30 * time.Second
)

// run handles the Java client's packets and the 20 Hz tick until the connection closes.
func (p *Player) run() {
	in := make(chan javaPacket, 256)
	done := make(chan struct{})
	defer close(done)
	p.goSafe("java read", func() { p.java.readLoop(in, done) })
	tick := time.NewTicker(time.Second / 20)
	defer tick.Stop()
	ka := time.NewTicker(keepAliveInterval)
	defer ka.Stop()
	for {
		select {
		case <-p.java.Closed():
			return
		case pk := <-in:
			if pk.err != nil {
				p.log.Debug("java read", "err", pk.err)
				p.java.Close()
				return
			}
			if pk.id == v777.ServerboundPlayKeepAlive {
				p.keepAliveAnswered(pk.body)
				continue
			}
			var err error
			if !p.locked(fmt.Sprintf("java packet 0x%02x", pk.legacyID), func() { err = p.handleJava(pk) }) {
				return
			}
			if err != nil {
				p.log.Info("bad java packet", "id", pk.legacyID, "err", err)
				p.java.Disconnect("Bad packet")
				return
			}
		case <-tick.C:
			if !p.locked("tick", p.tick) {
				return
			}
		case <-ka.C:
			timedOut := false
			if !p.locked("keep-alive", func() {
				if p.keepAlive != 0 && time.Since(p.keepAliveAt) > keepAliveTimeout {
					timedOut = true
					return
				}
				if p.keepAlive == 0 {
					p.keepAlive, p.keepAliveAt = time.Now().UnixNano(), time.Now()
					p.sendKeepAlive(p.keepAlive)
				}
			}) {
				return
			}
			if timedOut {
				p.log.Info("keep-alive timed out")
				p.java.Disconnect("Timed out")
				return
			}
		}
	}
}

// locked runs f with p.mu held. A panic in f is logged, p.mu released, the player disconnected
// (only this player: the process and everyone else carry on) and false returned.
func (p *Player) locked(what string, f func()) (ok bool) {
	p.mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			p.mu.Unlock()
			p.log.Error("panic", "in", what, "panic", r, "stack", string(debug.Stack()))
			p.internalError()
			ok = false
			return
		}
		p.mu.Unlock()
	}()
	f()
	return true
}

func (p *Player) keepAliveAnswered(body []byte) {
	id := wire.NewReader(body).Int64()
	p.mu.Lock()
	if id == p.keepAlive {
		p.keepAlive = 0
		p.ping.java = time.Since(p.keepAliveAt) // the Java leg of /ping (ping.go)
	}
	p.mu.Unlock()
}

// handleJava handles one Java packet. p.mu is held.
func (p *Player) handleJava(pk javaPacket) error {
	if pk.id < 0 {
		return p.handleLegacyJava(pk.legacyID, pk.body)
	}
	return p.dispatchJava(pk.id, pk.body)
}

// tick runs 20 times a second with p.mu held.
func (p *Player) tick() {
	p.sendChunkBatch()
	if p.rem != nil {
		p.releaseTeleport()
		p.movementTick()
	}
	for _, f := range tickHooks {
		f(p)
	}
}

// tickHooks run every tick with p.mu held (entities, windows... register theirs from init).
var tickHooks []func(p *Player)

// OnTick registers f to run every tick. Only call it from init.
func OnTick(f func(p *Player)) { tickHooks = append(tickHooks, f) }

// resetHooks run when the translator state is reset (a new connection or dimension), with p.mu
// held: forget per-connection state there. newConn is false for a dimension change.
var resetHooks []func(p *Player, newConn bool)

// OnReset registers f to run when the player's per-connection state is reset.
func OnReset(f func(p *Player, newConn bool)) { resetHooks = append(resetHooks, f) }

// errNotConnected is for actions that need a Bedrock server.
var errNotConnected = errors.New("not connected to a server")
