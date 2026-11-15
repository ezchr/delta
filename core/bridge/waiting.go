package bridge

import (
	"sync"
	"time"

	"javabridge/core/internal/javamap"

	jchunk "github.com/df-mc/dragonfly/server/java/protocol/chunk"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/go-gl/mathgl/mgl64"
)

// The waiting area: where a player without a server is. It is sent straight to the Java client,
// with no Bedrock world behind it: an empty overworld (void, fixed noon) with a small glass
// platform under the spawn, and chat lines saying how to /join.

// waitingPos is where a player waits.
var waitingPos = mgl64.Vec3{0.5, 65, 0.5}

// waitingRadius is how many chunks around the spawn the waiting area sends (all air but the
// platform), so the client is not stuck on "Loading terrain".
const waitingRadius = 2

// enterWaiting shows the waiting area (reason: why the player is there, in red; "" for none).
// p.mu is held.
func (p *Player) enterWaiting(first bool, reason string) {
	p.leaveWorld()
	p.resetState(true)
	p.rem = nil
	p.self = selfState{pos: waitingPos, mode: GameAdventure, health: 20, maxHealth: 20, food: 20, saturation: 5, speed: 0.1, flySpeed: 0.05}
	p.clock = levelClock{time: 6000}
	p.mv.reset(waitingPos, 0, 0)
	if first {
		p.sendLogin(DimOverworld, GameAdventure)
		p.sendSelfTab(GameAdventure)
	} else {
		p.sendRespawn(DimOverworld, GameAdventure, 0)
	}
	p.inWaiting = true
	p.sendSpawnPoint(0, 65, 0)
	p.showSelf(true)
	p.sendWaitingChunks()
	p.cmds.sendTree()
	if reason != "" {
		p.BridgeMessage("§c" + reason)
	}
	if p.note != "" {
		p.BridgeMessage("§c" + p.note)
		p.note = ""
	}
	p.waitingHelp()
}

// waitingHelp tells the player why they are in the waiting area.
func (p *Player) waitingHelp() {
	p.BridgeMessage("Waiting for the server...")
}

// sendWaitingChunks sends the waiting area's chunks.
func (p *Player) sendWaitingChunks() {
	glass, ok := javamap.NameDefault("minecraft:glass")
	if !ok {
		glass = javamap.JavaStone
	}
	glass = uint32ToState(p, glass)
	plains := p.biomeID(1)
	n := dimSections(DimOverworld)
	p.send(v777.ClientboundPlayChunkBatchStart, p.pk())
	count := 0
	for cx := int32(-waitingRadius); cx <= waitingRadius; cx++ {
		for cz := int32(-waitingRadius); cz <= waitingRadius; cz++ {
			col := &jchunk.Column{X: cx, Z: cz, Sections: make([]jchunk.Section, n),
				SkyLight: make([]jchunk.Light, n+2), BlockLight: make([]jchunk.Light, n+2)}
			var heights [256]uint16
			for i := range col.Sections {
				col.Sections[i].Fill(0, plains)
			}
			if cx == 0 && cz == 0 || cx == -1 && cz == 0 || cx == 0 && cz == -1 || cx == -1 && cz == -1 {
				sec := &col.Sections[(64+64)>>4] // y 64
				for x := 0; x < 16; x++ {
					for z := 0; z < 16; z++ {
						bx, bz := int(cx)*16+x, int(cz)*16+z
						if bx < -3 || bx > 3 || bz < -3 || bz > 3 {
							continue
						}
						sec.Blocks[jchunk.BlockIndex(x, 64&15, z)] = uint32(glass)
						sec.BlockCount++
						heights[z<<4|x] = uint16(64 + 64 + 1)
					}
				}
			}
			for i := range col.SkyLight {
				col.SkyLight[i] = jchunk.Light{State: jchunk.LightData, Data: fullLight}
				col.BlockLight[i].State = jchunk.LightAbsent
			}
			col.SkyLight[0].State, col.SkyLight[n+1].State = jchunk.LightAbsent, jchunk.LightAbsent
			data := jchunk.PackHeightmap(nil, &heights, n*16)
			col.Heightmaps = []jchunk.Heightmap{
				{Type: jchunk.WorldSurface, Data: data},
				{Type: jchunk.MotionBlocking, Data: data},
				{Type: jchunk.MotionBlockingNoLeaves, Data: data},
			}
			w := p.pk()
			if err := p.javaEncoder().Encode(w, col); err != nil {
				p.log.Error("encode waiting chunk", "err", err)
				continue
			}
			p.send(v777.ClientboundPlayLevelChunkWithLight, w)
			count++
		}
	}
	w := p.pk()
	w.VarInt(int32(count))
	p.send(v777.ClientboundPlayChunkBatchFinished, w)
}

// fullLight is a nibble array of light 15.
var fullLight = func() []byte {
	b := make([]byte, 2048)
	for i := range b {
		b[i] = 0xff
	}
	return b
}()

// uint32ToState is a 26.3 state in the client's numbering.
func uint32ToState(p *Player, s int32) int32 { return p.ver().BlockState(s) }

// switchMu serialises switches: one at a time per bridge process (v1's).
var switchMu sync.Mutex

// switchTo connects to target and moves the player there. Called without p.mu.
func (p *Player) switchTo(target string) {
	switchMu.Lock()
	defer switchMu.Unlock()
	if !p.connecting.CompareAndSwap(false, true) {
		p.message("§cAlready connecting.")
		return
	}
	defer p.connecting.Store(false)
	p.mu.Lock()
	if old := p.rem; old != nil && old.target == target {
		// The same server again (/jb reconnect): leave it first. A server refuses a second
		// login of an account that is still on it.
		p.rem = nil
		p.enterWaiting(false, "")
		p.mu.Unlock()
		old.close("reconnecting")
		time.Sleep(500 * time.Millisecond) // let the server see the player leave
	} else {
		p.mu.Unlock()
	}
	p.message("Reconnecting...")
	rem, err := p.dialRemote(target)
	if err != nil {
		p.log.Info("connect failed", "target", target, "err", err)
		p.message("§cCould not connect: %s", dialErrorText(err))
		return
	}
	p.mu.Lock()
	if p.gone {
		p.mu.Unlock()
		rem.close("Java player left")
		return
	}
	old := p.rem
	p.rem = nil
	if old != nil {
		old.close("switched server")
	}
	p.enterRemote(rem, false)
	p.mu.Unlock()
	rem.startReading()
	p.message("Connected.")
}
