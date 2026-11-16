package bridge

import (
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Sleeping in a bed.
//
// BDS (Player::startSleepInBed, the 1.21.3 dump; 1.26.60 does the same) puts a sleeper at the
// bed's head block + (0.5, 0.90625, 0.5) with a 0.2 x 0.2 box whose position is 0.2 above the
// feet, sets the SLEEPING flag (flags 2), the bed position (actor data 28, the head block; the
// spawn bed, so it is only sent when it changes) and the respawn point, and tells the player's
// own client the new position with a CorrectPlayerMovePrediction. A Bedrock client then sends
// PlayerAction StartSleeping (which makes BDS tell everyone "X is sleeping in a bed"), keeps
// sending inputs at that position, and leaves with PlayerAction StopSleeping. On waking
// (Player::stopSleepInBed: the leave button, morning, damage) BDS clears the flag, animates
// StopSleep and puts the player next to the bed, eyes 1.72 above the block.
//
// A Java client lies in its bed by itself once its own entity data has the sleeping position
// (LivingEntity.setPosToBed: the bed's top + 0.125, then it falls onto it) and opens the bed
// screen, whose leave button sends player_command STOP_SLEEPING. So the bridge gives it that
// data and never moves it to the server's sleeping position: put there (1.62 below the server's
// eyes, under the bed) the Java client fell through the floor, its positions went to the server
// and the server corrected it back, over and over. The inputs while asleep carry the server's
// own position instead.

// sleepEye is how far above the bed block BDS puts a sleeper's position (its eyes).
const sleepEye = 0.90625

// sleeperYOff is how far above the Java position of another sleeping player BDS's position is:
// Java lays a sleeper on the bed's top, 0.5625 above the block.
const sleeperYOff = sleepEye - 0.5625

func init() {
	OnReset(func(p *Player, newConn bool) {
		p.self.sleeping, p.mv.waking = false, false
	})
}

// setSleeping applies the server's sleeping flag of the player.
func (p *Player) setSleeping(on bool) {
	s := &p.self
	if on == s.sleeping {
		return
	}
	s.sleeping = on
	m := &p.mv
	r := p.rem
	p.log.Debug("sleeping", "on", on, "bed", s.bed, "pos", m.pos)
	if on {
		m.waking = false
		// Where the server has the player now (corrections and teleports to the bed replace
		// it with the server's own numbers).
		m.sleepAt = mgl64.Vec3{float64(s.bed[0]) + 0.5, float64(s.bed[1]) + sleepEye, float64(s.bed[2]) + 0.5}
		p.sendSelfSleep()
		r.write(&packet.PlayerAction{EntityRuntimeID: r.gd.EntityRuntimeID, ActionType: protocol.PlayerActionStartSleeping,
			BlockPosition: s.bed, BlockFace: -1})
		return
	}
	// Awake: the client stands up (animate WAKE_UP: its stopSleepInBed, which also closes the bed
	// screen), and goes where the server put the player when the server says so (correction).
	p.sendAnimate(SelfEntityID, 0)
	p.sendSelfSleep()
	m.waking = true
	m.lastPos = m.pos
}

// leaveBed is the Java player pressing "Leave Bed" (or escape) on the bed screen.
func (p *Player) leaveBed() {
	r := p.rem
	if r == nil || !p.self.sleeping {
		return
	}
	r.write(&packet.PlayerAction{EntityRuntimeID: r.gd.EntityRuntimeID, ActionType: protocol.PlayerActionStopSleeping,
		BlockPosition: p.self.bed, BlockFace: -1})
}

// sendSelfSleep gives the Java client its own sleeping state: the pose and the bed position
// (LivingEntity's sleeping_pos, 14), which puts the client in its bed or takes it out.
func (p *Player) sendSelfSleep() {
	s := &p.self
	w := p.pk()
	w.VarInt(SelfEntityID)
	d := dataWriter{w, &dataTable{}}
	pose := int32(poseStanding)
	if s.sleeping {
		pose = poseSleeping
	}
	d.varVal(6, serPose, pose)
	d.head(14, serOptBlockPos)
	w.Bool(s.sleeping)
	if s.sleeping {
		w.Position(int(s.bed[0]), int(s.bed[1]), int(s.bed[2]))
	}
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// sleepCorrection takes a server position of the player (eyes, Bedrock's) while asleep or just
// awake. It reports whether it was handled here.
func (p *Player) sleepCorrection(pos mgl64.Vec3, vel mgl64.Vec3) bool {
	m := &p.mv
	switch {
	case p.self.sleeping:
		// The server keeps the sleeper where it put it; the Java client lies in its bed.
		m.sleepAt = pos
		return true
	case m.waking:
		if pos.Sub(m.sleepAt).Len() < 0.01 {
			return true // still the bed (sent before the wake-up)
		}
		// Out of bed: the server has put the player next to the bed.
		m.waking = false
		p.placeAt(pos.Sub(mgl64.Vec3{0, eyeHeight, 0}), vel, m.yaw, m.pitch)
		return true
	}
	return false
}

// yOffset is how far above the Java position of e the server's position is.
func (e *entity) yOffset() float64 {
	if e.t.kind&entPlayer != 0 && e.flag(protocol.EntityDataFlagSleeping) {
		return sleeperYOff
	}
	return e.t.yOff
}
