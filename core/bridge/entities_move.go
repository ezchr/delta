package bridge

import (
	"math"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Moving the server's actors on the Java client, as vanilla does: delta moves (move_entity_pos,
// _pos_rot, _rot; the client interpolates them) while they fit and at most 60 in a row, else a
// full entity_position_sync; rotate_head for the head. The 26.3 delta codec is kept per entity,
// so nothing is allocated for a move.

// maxDeltaMoves is how many delta moves vanilla sends before a full sync (to undo drift).
const maxDeltaMoves = 60

func init() {
	OnBedrock(packet.IDMoveActorAbsolute, func(p *Player, pk *packet.MoveActorAbsolute) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok {
			return
		}
		pos := vec64(pk.Position).Sub(mgl64.Vec3{0, e.t.yOff, 0})
		yaw, pitch, head := pk.Rotation[1], pk.Rotation[0], pk.Rotation[2]
		if e.t.kind&entBoat != 0 {
			yaw -= 90
			head = yaw
		}
		if e.t.kind&entPlayer != 0 {
			p.checkRevive(e, pos)
		}
		p.moveEntity(e, pos, yaw, pitch, head, pk.Flags&packet.MoveFlagOnGround != 0, pk.Flags&packet.MoveFlagTeleport != 0)
	})
	OnBedrock(packet.IDMoveActorDelta, func(p *Player, pk *packet.MoveActorDelta) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok {
			return
		}
		pos, yaw, pitch, head := e.pos, e.yaw, e.pitch, e.head
		if v, ok := pk.PositionX.Value(); ok {
			pos[0] = float64(v)
		}
		if v, ok := pk.PositionY.Value(); ok {
			pos[1] = float64(v) - e.t.yOff
		}
		if v, ok := pk.PositionZ.Value(); ok {
			pos[2] = float64(v)
		}
		if v, ok := pk.RotationX.Value(); ok {
			pitch = v
		}
		if v, ok := pk.RotationY.Value(); ok {
			yaw = v
			if e.t.kind&entBoat != 0 {
				yaw -= 90
				head = yaw
			}
		}
		if v, ok := pk.RotationYHead.Value(); ok && e.t.kind&entBoat == 0 {
			head = v
		}
		if e.t.kind&entPlayer != 0 {
			p.checkRevive(e, pos)
		}
		p.moveEntity(e, pos, yaw, pitch, head, pk.OnGround, pk.ForceMove)
	})
	otherPlayerMoveHooks = append(otherPlayerMoveHooks, func(p *Player, pk *packet.MovePlayer) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok {
			return
		}
		pos := vec64(pk.Position).Sub(mgl64.Vec3{0, eyeHeight, 0})
		if pk.Mode == packet.MoveModeReset {
			p.revivePlayer(e) // a respawn
		} else {
			p.checkRevive(e, pos)
		}
		p.moveEntity(e, pos, pk.Yaw, pk.Pitch, pk.HeadYaw, pk.OnGround, pk.Mode == packet.MoveModeTeleport || pk.Mode == packet.MoveModeReset)
	})
	OnBedrock(packet.IDSetActorMotion, func(p *Player, pk *packet.SetActorMotion) {
		if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
			return // movement.go
		}
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok {
			return
		}
		e.vel = vec64(pk.Velocity)
		if e.spawned {
			p.sendMotion(e.id, e.vel)
		}
	})
}

// moveEntity moves an entity to pos (Java coordinates) with a rotation.
func (p *Player) moveEntity(e *entity, pos mgl64.Vec3, yaw, pitch, head float32, onGround, teleport bool) {
	if !valid(pos[0]) || !valid(pos[1]) || !valid(pos[2]) {
		return
	}
	e.pos, e.yaw, e.pitch, e.head, e.onGround = pos, yaw, pitch, head, onGround
	if !e.spawned {
		return
	}
	if p.ents.ride.uniq != 0 && p.ents.ride.uniq == e.uniq && p.ents.ride.driving {
		return // the Java client moves the vehicle it drives (riding: corrections only)
	}
	by, bp, bh := angleByte(yaw), angleByte(pitch), angleByte(head)
	var d [3]int64
	fits := !teleport && e.sinceSync < maxDeltaMoves
	for i := range d {
		d[i] = encodeDelta(pos[i]) - encodeDelta(e.base[i])
		if d[i] < math.MinInt16 || d[i] > math.MaxInt16 {
			fits = false
		}
	}
	moved := d[0] != 0 || d[1] != 0 || d[2] != 0
	turned := by != e.sentYaw || bp != e.sentPitch
	headTurned := bh != e.sentHead
	e.sentYaw, e.sentPitch, e.sentHead = by, bp, bh
	if !fits {
		e.base, e.sinceSync = pos, 0
		p.sendPositionSync(e.id, pos, yaw, pitch, onGround)
		w := p.pk()
		w.VarInt(e.id)
		w.Byte(bh)
		p.send(v777.ClientboundPlayRotateHead, w)
		return
	}
	if !moved && !turned && !headTurned {
		return
	}
	if moved || turned {
		e.sinceSync++
	}
	for i := range d {
		if d[i] != 0 { // the client's VecDeltaCodec.decode
			e.base[i] = float64(encodeDelta(e.base[i])+d[i]) / 4096
		}
	}
	native := p.ver().Native()
	switch {
	case moved && turned:
		w := p.pk()
		w.VarInt(e.id)
		if native {
			w.VarInt(groundProps(onGround))
		}
		w.Int16(int16(d[0]))
		w.Int16(int16(d[1]))
		w.Int16(int16(d[2]))
		w.Byte(by)
		w.Byte(bp)
		if !native {
			w.Bool(onGround)
		}
		p.send(v777.ClientboundPlayMoveEntityPosRot, w)
	case moved:
		w := p.pk()
		w.VarInt(e.id)
		if native {
			w.VarInt(groundProps(onGround))
		}
		w.Int16(int16(d[0]))
		w.Int16(int16(d[1]))
		w.Int16(int16(d[2]))
		if !native {
			w.Bool(onGround)
		}
		p.send(v777.ClientboundPlayMoveEntityPos, w)
	case turned:
		w := p.pk()
		w.VarInt(e.id)
		if native {
			w.Bool(onGround)
			w.Byte(by)
			w.Byte(bp)
		} else {
			w.Byte(by)
			w.Byte(bp)
			w.Bool(onGround)
		}
		p.send(v777.ClientboundPlayMoveEntityRot, w)
	}
	if headTurned {
		w := p.pk()
		w.VarInt(e.id)
		w.Byte(bh)
		p.send(v777.ClientboundPlayRotateHead, w)
	}
}

// groundProps is the 26.3 move properties VarInt: bit 0 on ground, no steps (a linear move).
func groundProps(onGround bool) int32 {
	if onGround {
		return 1
	}
	return 0
}

func encodeDelta(v float64) int64 { return int64(math.Floor(v*4096 + 0.5)) }

// sendPositionSync sends entity_position_sync (a full position; 26.2 has a velocity in it).
func (p *Player) sendPositionSync(id int32, pos mgl64.Vec3, yaw, pitch float32, onGround bool) {
	w := p.pk()
	w.VarInt(id)
	if p.ver().Native() {
		w.VarInt(0) // PositionPath.LINEAR
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
	} else {
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float64(0)
		w.Float64(0)
		w.Float64(0)
	}
	w.Float32(yaw)
	w.Float32(pitch)
	w.Bool(onGround)
	p.send(v777.ClientboundPlayEntityPositionSync, w)
}

// sendMotion sends set_entity_motion.
func (p *Player) sendMotion(id int32, vel mgl64.Vec3) {
	w := p.pk()
	w.VarInt(id)
	w.LpVec3(vel[0], vel[1], vel[2])
	p.send(v777.ClientboundPlaySetEntityMotion, w)
}
