package bridge

import (
	"time"

	"github.com/go-gl/mathgl/mgl64"
)

// Holding the player in the air until the ground under them is there: when BDS spawns a player
// (above the world, spawn search, then the real position) the Java client gets that position
// before the column, and the Java client keeps simulating its player without the blocks: it fell
// through the floor for a few seconds until the server's corrections pulled it back. While held,
// the client is flying with no fly speed: it cannot fall or move; the column ends the hold.

func (p *Player) holdUntilColumn(pos mgl64.Vec3) {
	w := p.world
	if w == nil {
		return
	}
	cp := ChunkPos{int32(int(mfloor(pos[0])) >> 4), int32(int(mfloor(pos[2])) >> 4)}
	if col := w.cols[cp]; col != nil && col.sent {
		return
	}
	s := &p.self
	s.held = true
	p.sendAbilities(s.mode, true, true, 1e-6)
	r := p.rem
	p.after(holdTimeout, "hold timeout", func() { // a server that sends no column there
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.rem == r {
			p.releaseHold()
		}
	})
}

// holdTimeout ends a hold whose column does not come.
const holdTimeout = 8 * time.Second

func (p *Player) releaseHold() {
	s := &p.self
	if !s.held {
		return
	}
	s.held = false
	p.sendAbilities(s.mode, s.mayFly, s.flying, s.flySpeed)
}

func init() {
	chunkSentHooks = append(chunkSentHooks, func(p *Player, pos ChunkPos, _ *column) {
		s := &p.self
		if s.held && pos == (ChunkPos{int32(int(mfloor(s.pos[0])) >> 4), int32(int(mfloor(s.pos[2])) >> 4)}) {
			p.releaseHold()
		}
	})
}
