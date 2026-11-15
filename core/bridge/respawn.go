package bridge

import (
	"time"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Respawning as a Bedrock client does (v1's respawn.go): the server says where (Respawn,
// searching then ready to spawn), the player presses respawn (Respawn, client ready), and once
// both happened the player is back (PlayerAction respawn). Servers differ in the order: vanilla
// sends "ready" once the client said it is, some send it before. Either way the Java client
// respawns when both are in, with full health (the server sends its health after the respawn; a
// Java client given 0 would show the death screen again).

// respawnState is a death waiting for its respawn.
type respawnState struct {
	ready   *mgl64.Vec3 // where the server spawns the player, once it said so
	clicked bool        // the Java player pressed respawn
}

func init() {
	OnBedrock(packet.IDRespawn, (*Player).serverRespawn)
	OnJava(v777.ServerboundPlayClientCommand, func(p *Player, r *wireReader) error {
		if action := r.VarInt(); r.Err == nil && action == 0 && p.rem != nil {
			p.javaRespawn()
		}
		return r.Err
	})
}

// serverRespawn handles the server's Respawn packet.
func (p *Player) serverRespawn(pk *packet.Respawn) {
	pos := vec64(pk.Position).Sub(mgl64.Vec3{0, eyeHeight, 0})
	p.log.Debug("respawn from server", "state", pk.State, "pos", pos, "dead", p.self.dead)
	if pk.State != packet.RespawnStateReadyToSpawn {
		return
	}
	if !p.self.dead {
		// Joining (or a server that respawns on its own): just be there.
		p.respawned(pos, false)
		return
	}
	p.respawn.ready = &pos
	if p.respawn.clicked {
		p.respawned(pos, true)
	}
}

// javaRespawn is the Java player pressing respawn.
func (p *Player) javaRespawn() {
	if !p.self.dead {
		p.log.Info("java respawn request while alive: ignored")
		return
	}
	p.log.Debug("respawn pressed")
	r := p.rem
	at := p.self.pos
	r.write(&packet.Respawn{State: packet.RespawnStateClientReadyToSpawn, EntityRuntimeID: r.gd.EntityRuntimeID, Position: vec32(at.Add(mgl64.Vec3{0, eyeHeight, 0}))})
	p.respawn.clicked = true
	if ready := p.respawn.ready; ready != nil {
		p.respawned(*ready, true)
		return
	}
	// Servers that wait for the respawn action before saying where (vanilla BDS takes either).
	p.after(2*time.Second, "respawn action", func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.rem == r && p.respawn.clicked && !r.closed() {
			p.log.Info("respawn: no answer yet, sending the respawn action")
			r.write(&packet.PlayerAction{EntityRuntimeID: r.gd.EntityRuntimeID, ActionType: protocol.PlayerActionRespawn, BlockFace: -1})
		}
	})
}

// respawned puts the player at pos, alive. died: after a death (the Java client gets a respawn,
// the server the respawn action).
func (p *Player) respawned(pos mgl64.Vec3, died bool) {
	p.respawn = respawnState{}
	p.javaLoaded = false // the client shows its loading screen again
	s := &p.self
	wasDead := s.dead
	s.pos, s.dead = pos, false
	if s.health <= 0 {
		s.health = max(s.maxHealth, 1)
	}
	if wasDead {
		// A Java respawn: off the death screen, a new player entity in the same level.
		p.sendRespawn(p.javaDim, s.mode, 0)
		p.showSelf(true)
		p.mv.teleported = true
		for _, f := range respawnHooks {
			f(p)
		}
		p.log.Debug("java: respawn sent", "pos", pos, "health", s.health)
	} else {
		p.teleportTo(pos, p.mv.yaw, p.mv.pitch)
		p.sendHealth(s.health, s.food, s.saturation)
		p.holdUntilColumn(pos)
	}
	r := p.rem
	if died {
		r.write(&packet.PlayerAction{EntityRuntimeID: r.gd.EntityRuntimeID, ActionType: protocol.PlayerActionRespawn, BlockFace: -1})
		p.log.Debug("respawned", "pos", pos)
	} else {
		r.write(&packet.Respawn{State: packet.RespawnStateClientReadyToSpawn, EntityRuntimeID: r.gd.EntityRuntimeID})
	}
	r.finishLoading()
	s.searching = true
	if p.world != nil && p.world.HasColumn(int(mfloor(pos[0])), int(mfloor(pos[2]))) {
		p.columnArrived(ChunkPos{int32(int(mfloor(pos[0])) >> 4), int32(int(mfloor(pos[2])) >> 4)})
	}
}

// respawnHooks run after a Java respawn was sent (inventories, effects: later milestones).
var respawnHooks []func(p *Player)
