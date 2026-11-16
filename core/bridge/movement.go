package bridge

import (
	"math"
	"time"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Own movement. The Java client simulates the player itself (with Bedrock's movement attributes,
// self.go) and reports positions; every client tick (client_tick_end) becomes one PlayerAuthInput
// carrying that tick's position, delta, rotation and input flags, so the server sees the same
// tick-by-tick movement a Bedrock client would send. If the client stops ticking (loading screen)
// the bridge keeps inputs going from the tick timer.
//
// Server corrections (CorrectPlayerMovePrediction) name the tick they apply to: the bridge keeps
// the positions it sent per tick, so the correction is the offset between the server's position
// and what was sent then. That offset is applied to the client's current position as a relative
// player_position (velocity and look kept: no visible snap for small corrections), and to the
// positions sent since, so a later correction of an already corrected tick does not apply twice.
// Only big corrections (and the server's teleports, MovePlayer) put the client at an absolute
// position.

// bigCorrection is how far off (blocks) a correction must be to teleport instead of nudging.
const bigCorrection = 3.0

// historyLen is how many sent ticks are remembered for corrections.
const historyLen = 128

// movement is the Java client's movement as the bridge reports it to the server.
type movement struct {
	pos        mgl64.Vec3 // feet position, in the frame the server knows
	yaw, pitch float32
	onGround   bool
	hCollision bool
	keys       byte // Java player_input flags
	sprinting  bool

	tick    uint64
	lastPos mgl64.Vec3 // position of the last input
	history [historyLen]sentTick

	// pending are teleports the client has not accepted yet. Client positions before an
	// absolute one are ignored; before relative ones, their offsets are added.
	pending []pendingTeleport
	// teleported: the next input confirms a server teleport (HandledTeleport).
	teleported bool
	// held is a server teleport waiting for the Java client to have the chunks there.
	held *heldTeleport

	wasOnGround, wasJumpKey, wasSneaking, wasSprinting bool
	glideSign                                          float32   // flight glide move input (sendInput)
	flyChange                                          int       // 1 started flying, -1 stopped
	flyRequested                                       time.Time // when the client last asked to fly (RequestAbility)
	swungAtAir                                         bool

	// shiftKey is the shift key of the last input sent: the Java client crouches by the key of
	// the tick before, the sprint ends then (sendInput).
	shiftKey bool
	// srvSprint: the server's last movement attribute has the sprint modifier (self.go).
	srvSprint bool

	lastInput   time.Time
	corrections int
	// tpLag is how many ticks the client takes to apply a teleport (measured on the timed ones).
	tpLag    float64
	unboosts int
	// sjTick is the input tick of a jump made when the sprint started, waiting for the server's
	// sprint attribute (sprintJumpVerdict); sjYaw the look then.
	sjTick uint64
	sjYaw  float32
	// barrierTick is the last tick sent before the last absolute teleport, to barrierPos.
	barrierTick uint64
	barrierPos  mgl64.Vec3
}

type sentTick struct {
	tick uint64
	pos  mgl64.Vec3
}

type heldTeleport struct {
	pos        mgl64.Vec3
	yaw, pitch float32
	since      time.Time
}

type pendingTeleport struct {
	id       int32
	absolute bool
	offset   mgl64.Vec3
	tick     uint64 // input tick it was sent after (0: not timed)
}

// Java player_input flags.
const (
	keyForward = 1 << iota
	keyBackward
	keyLeft
	keyRight
	keyJump
	keyShift
	keySprint
)

// reset starts movement afresh at pos (a new connection or dimension).
func (m *movement) reset(pos mgl64.Vec3, yaw, pitch float32) {
	tick := m.tick
	*m = movement{pos: pos, lastPos: pos, yaw: yaw, pitch: pitch, tick: tick, tpLag: max(m.tpLag, 1)}
}

// InputHook adds to a PlayerAuthInput before it is sent (block actions, item interactions, item
// stack requests, vehicle input: later milestones). It runs with p.mu held.
type InputHook func(p *Player, pk *packet.PlayerAuthInput, flags *protocol.InputFlags)

var inputHooks []InputHook

// OnInput registers an InputHook. Only call it from init.
func OnInput(h InputHook) { inputHooks = append(inputHooks, h) }

// AfterInput queues a packet to send right after the next PlayerAuthInput.
func (p *Player) AfterInput(pk packet.Packet) { p.afterInput = append(p.afterInput, pk) }

func init() {
	OnJava(v777.ServerboundPlayAcceptTeleportation, func(p *Player, r *wireReader) error {
		id := r.VarInt()
		m := &p.mv
		for len(m.pending) > 0 && m.pending[0].id <= id {
			if t := m.pending[0]; t.id == id && t.tick != 0 && m.tick >= t.tick {
				// The client applied it before the movement of the tick after this one.
				m.tpLag = 0.7*m.tpLag + 0.3*float64(m.tick-t.tick+1)
			}
			m.pending = m.pending[1:]
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayMovePlayerPos, func(p *Player, r *wireReader) error {
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		flags := r.Byte()
		if r.Err == nil {
			p.clientMove(&mgl64.Vec3{x, y, z}, nil, flags)
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayMovePlayerPosRot, func(p *Player, r *wireReader) error {
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		yaw, pitch := r.Float32(), r.Float32()
		flags := r.Byte()
		if r.Err == nil {
			p.clientMove(&mgl64.Vec3{x, y, z}, &[2]float32{yaw, pitch}, flags)
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayMovePlayerRot, func(p *Player, r *wireReader) error {
		yaw, pitch := r.Float32(), r.Float32()
		flags := r.Byte()
		if r.Err == nil {
			p.clientMove(nil, &[2]float32{yaw, pitch}, flags)
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayMovePlayerStatusOnly, func(p *Player, r *wireReader) error {
		flags := r.Byte()
		if r.Err == nil {
			p.clientMove(nil, nil, flags)
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayPlayerInput, func(p *Player, r *wireReader) error {
		p.mv.keys = r.Byte()
		return r.Err
	})
	OnJava(v777.ServerboundPlayPlayerCommand, func(p *Player, r *wireReader) error {
		r.VarInt() // entity id
		switch r.VarInt() {
		case 1:
			p.mv.sprinting = true
		case 2:
			p.mv.sprinting = false
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayClientTickEnd, func(p *Player, r *wireReader) error {
		if p.rem != nil && !p.inWaiting {
			p.sendInput()
		}
		return nil
	})
	OnJava(v777.ServerboundPlayPlayerAbilities, func(p *Player, r *wireReader) error {
		flying := r.Byte()&2 != 0
		s := &p.self
		if !s.canFly() || flying == s.flying {
			return r.Err
		}
		s.flying = flying
		if flying {
			p.mv.flyChange = 1
		} else {
			p.mv.flyChange = -1
		}
		// The next input tells the server (sendInput). Until then its abilities still say the
		// old state.
		if flying {
			p.mv.flyRequested = time.Now()
		}
		return r.Err
	})
	OnJava(v777.ServerboundPlayPunch, func(p *Player, r *wireReader) error {
		p.mv.swungAtAir = true
		p.Write(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: p.rem.gd.EntityRuntimeID})
		return nil
	})
	OnJava(v777.ServerboundPlayPlayerLoaded, func(p *Player, r *wireReader) error {
		p.javaLoaded = true
		if p.rem != nil {
			p.rem.finishLoading()
		}
		if p.dimLoaded != nil {
			p.dimLoaded()
			p.dimLoaded = nil
		}
		return nil
	})

	OnBedrock(packet.IDMovePlayer, func(p *Player, pk *packet.MovePlayer) {
		if pk.EntityRuntimeID != p.rem.gd.EntityRuntimeID {
			p.ents.handleMovePlayer(pk) // another player (entities.go)
			return
		}
		pos := vec64(pk.Position).Sub(mgl64.Vec3{0, eyeHeight, 0})
		p.log.Debug("server moved us", "pos", pos, "mode", pk.Mode, "tick", pk.Tick)
		p.syncTick(pk.Tick)
		if pk.Mode == packet.MoveModeRotation {
			p.mv.yaw, p.mv.pitch = pk.Yaw, pk.Pitch
			p.sendPosition(mgl64.Vec3{}, pk.Yaw, pk.Pitch, relX|relY|relZ|relDX|relDY|relDZ)
			return
		}
		p.self.yaw, p.self.pitch = pk.Yaw, pk.Pitch
		p.teleportTo(pos, pk.Yaw, pk.Pitch)
	})
	OnBedrock(packet.IDCorrectPlayerMovePrediction, (*Player).correction)
	OnBedrockStop(packet.IDSetActorMotion, func(p *Player, pk *packet.SetActorMotion) bool {
		if pk.EntityRuntimeID != p.rem.gd.EntityRuntimeID {
			return false // another entity (entities, milestone 2)
		}
		// Knockback, explosions, launch pads: the client moves itself with the server's velocity.
		w := p.pk()
		w.VarInt(SelfEntityID)
		w.LpVec3(float64(pk.Velocity[0]), float64(pk.Velocity[1]), float64(pk.Velocity[2]))
		p.send(v777.ClientboundPlaySetEntityMotion, w)
		return true
	})
}

// clientMove applies a movement packet of the Java client.
func (p *Player) clientMove(pos *mgl64.Vec3, rot *[2]float32, flags byte) {
	m := &p.mv
	if rot != nil && valid(float64(rot[0])) && valid(float64(rot[1])) {
		m.yaw, m.pitch = rot[0], max(-90, min(90, rot[1]))
	}
	if m.held != nil {
		return // the client is not at the server's position yet (teleportTo)
	}
	m.onGround, m.hCollision = flags&1 != 0, flags&2 != 0
	if pos == nil || !valid(pos[0]) || !valid(pos[1]) || !valid(pos[2]) {
		return
	}
	var off mgl64.Vec3
	for _, t := range m.pending {
		if t.absolute {
			return // the client has not got to the server's position yet
		}
		off = off.Add(t.offset)
	}
	m.pos = pos.Add(off)
	if p.inWaiting {
		return
	}
	p.self.pos = m.pos
	p.sendCentre(m.pos, false)
}

func valid(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) < 3e7 }

func mfloor(v float64) float64 { return math.Floor(v) }

// teleportTo puts the client at an absolute position for a server teleport: the next input says
// it was handled.
//
// A teleport to where the Java client has no chunks yet (an island, a portal to another area)
// waits for them: the Java client simulates a player in a column it does not have as falling
// through air, and BDS (which stands the player on the ground) corrected it for a second or
// more after every such teleport. Until the columns are shown, the client stays where it was
// (its positions are not used), its chunk centre moves to the target so the chunks come, and
// the server gets standing-still inputs at the target.
func (p *Player) teleportTo(pos mgl64.Vec3, yaw, pitch float32) {
	m := &p.mv
	m.teleported = true
	if p.world != nil && !p.shown(pos) {
		m.held = &heldTeleport{pos: pos, yaw: yaw, pitch: pitch, since: time.Now()}
		m.barrierTick, m.barrierPos = m.tick, pos
		m.pos, m.lastPos = pos, pos
		m.yaw, m.pitch = yaw, pitch
		p.self.pos = pos
		p.sendCentre(pos, false)
		return
	}
	m.held = nil
	p.placeAt(pos, mgl64.Vec3{}, yaw, pitch)
}

// heldTimeout is how long a teleport waits for its chunks at most.
const heldTimeout = 3 * time.Second

// releaseTeleport gives the client a held teleport once its chunks are shown. p.mu is held.
func (p *Player) releaseTeleport() {
	h := p.mv.held
	if h == nil || !p.shown(h.pos) && time.Since(h.since) < heldTimeout {
		return
	}
	p.mv.held = nil
	if debugMove {
		p.log.Info("MV held teleport released", "pos", h.pos, "waited", time.Since(h.since).Round(time.Millisecond))
	}
	p.placeAt(h.pos, mgl64.Vec3{}, h.yaw, h.pitch)
}

// placeAt puts the client at an absolute position (velocity reset); its positions until it
// accepts are ignored.
func (p *Player) placeAt(pos, vel mgl64.Vec3, yaw, pitch float32) {
	m := &p.mv
	id := p.sendPositionVel(pos, vel, yaw, pitch, 0)
	// Corrections of the ticks sent before this one name the old position: they are stale.
	m.barrierTick, m.barrierPos = m.tick, pos
	m.pending = append(m.pending, pendingTeleport{id: id, absolute: true})
	m.pos, m.lastPos = pos, pos
	m.yaw, m.pitch = yaw, pitch
	p.self.pos = pos
	p.sendCentre(pos, false)
}

// nudge moves the client by off relative to where it is now, keeping its velocity and look.
func (p *Player) nudge(off mgl64.Vec3) {
	m := &p.mv
	id := p.sendPosition(off, 0, 0, relX|relY|relZ|relYaw|relPitch|relDX|relDY|relDZ)
	m.pending = append(m.pending, pendingTeleport{id: id, offset: off, tick: m.tick})
	m.pos = m.pos.Add(off)
	p.self.pos = m.pos
}

// correction applies a CorrectPlayerMovePrediction.
func (p *Player) correction(pk *packet.CorrectPlayerMovePrediction) {
	m := &p.mv
	p.syncTick(pk.Tick)
	if pk.PredictionType == packet.PredictionTypeVehicle {
		for _, f := range vehicleCorrectionHooks {
			f(p, pk)
		}
		return
	}
	srv := vec64(pk.Position).Sub(mgl64.Vec3{0, eyeHeight, 0})
	if pk.Tick <= m.barrierTick && srv.Sub(m.barrierPos).Len() < 0.5 {
		return // the server confirming a teleport the client was already given
	}
	sent, ok := m.sentAt(pk.Tick)
	if !ok {
		sent = m.lastPos
	}
	off := srv.Sub(sent)
	m.corrections++
	if m.corrections <= 30 || m.corrections%50 == 0 || debugMove {
		p.log.Debug("server corrected our movement", "n", m.corrections, "to", srv, "sent", sent, "off", off.Len(),
			"tick", pk.Tick, "ourTick", m.tick, "known", ok, "onGround", pk.OnGround, "delta", pk.Delta)
	}
	if off.Len() < 1e-3 {
		return // the positions sent since already have it
	}
	if off.Len() > bigCorrection || !ok {
		p.placeAt(srv, vec64(pk.Delta), m.yaw, m.pitch)
		return
	}
	p.nudge(off)
	m.shiftHistory(pk.Tick, off)
	m.lastPos = m.lastPos.Add(off)
}

// vehicleCorrectionHooks get vehicle corrections (riding: milestone 2).
var vehicleCorrectionHooks []func(p *Player, pk *packet.CorrectPlayerMovePrediction)

// sentAt is the position sent in the input of tick t.
func (m *movement) sentAt(t uint64) (mgl64.Vec3, bool) {
	e := m.history[t%historyLen]
	return e.pos, e.tick == t && t != 0
}

// shiftHistory adds off to the positions sent after tick t: they are corrected now.
func (m *movement) shiftHistory(t uint64, off mgl64.Vec3) {
	for i := range m.history {
		if e := &m.history[i]; e.tick > t {
			e.pos = e.pos.Add(off)
		}
	}
	if e := &m.history[t%historyLen]; e.tick == t {
		e.pos = e.pos.Add(off)
	}
}

// syncTick moves the input tick up to a tick the server named: it drops input from before it.
func (p *Player) syncTick(t uint64) {
	if t > p.mv.tick {
		p.mv.tick = t
	}
}

// movementTick keeps inputs going while the client does not tick: on the loading screen, until it
// says player_loaded. After that only client ticks make inputs (a lagging client must not get
// made-up standing-still ticks the server would correct).
func (p *Player) movementTick() {
	if p.inWaiting || p.javaLoaded || time.Since(p.mv.lastInput) < 250*time.Millisecond {
		return
	}
	p.sendInput()
}

// sendInput sends one PlayerAuthInput from the client's state.
func (p *Player) sendInput() {
	m := &p.mv
	if m.held != nil {
		// Waiting for a teleport's chunks: the player stands still at the target.
		keys, sprinting := m.keys, m.sprinting
		m.keys, m.sprinting, m.onGround, m.hCollision = 0, false, true, false
		defer func() { m.keys, m.sprinting = keys, sprinting }()
	}
	r := p.rem
	m.lastInput = time.Now()
	flags := protocol.NewInputFlags(packet.InputFlagCount)
	prevGround := m.wasOnGround // the server's player is on the ground as this input starts
	pos := m.pos
	if m.sjTick != 0 && m.tick >= m.sjTick+sprintJumpWait {
		m.sjTick = 0 // no word from the server: leave it to its correction
	}
	if m.sprintJumpStart(pos.Sub(m.lastPos)) && !p.self.flying {
		m.sjTick, m.sjYaw = m.tick+1, m.yaw
	}
	delta := pos.Sub(m.lastPos)
	if m.teleported {
		flags.Set(packet.InputFlagHandledTeleport)
		m.teleported = false
		delta = mgl64.Vec3{}
	}
	m.lastPos = pos
	m.tick++
	m.history[m.tick%historyLen] = sentTick{m.tick, pos}

	var mv mgl32.Vec2
	if m.keys&keyForward != 0 {
		mv[1]++
		flags.Set(packet.InputFlagUp)
	}
	if m.keys&keyBackward != 0 {
		mv[1]--
		flags.Set(packet.InputFlagDown)
	}
	if m.keys&keyLeft != 0 {
		mv[0]++
		flags.Set(packet.InputFlagLeft)
	}
	if m.keys&keyRight != 0 {
		mv[0]--
		flags.Set(packet.InputFlagRight)
	}
	if m.keys&keyForward != 0 && m.keys&keyLeft != 0 {
		flags.Set(packet.InputFlagUpLeft)
	}
	if m.keys&keyForward != 0 && m.keys&keyRight != 0 {
		flags.Set(packet.InputFlagUpRight)
	}
	if mv[0] != 0 && mv[1] != 0 {
		mv = mv.Mul(float32(1 / math.Sqrt2))
	}
	// The Java client crouches (and slows its move input) by the shift key of the tick before
	// (LocalPlayer.aiStep sets crouching before it reads the tick's keys); BDS too only sneaks
	// from the input after the one with Sneaking, so the flags go with the key.
	sneaking := m.keys&keyShift != 0 && !p.self.flying
	if m.shiftKey && m.sprinting && !p.self.flying {
		p.stopJavaSprint() // BDS ends a sprint when the player sneaks; Java 26 sprints on
	}
	if sneaking {
		mv = mv.Mul(0.3)
	}

	jumpKey := m.keys&keyJump != 0
	if jumpKey {
		// BDS toggles the flight of a player that may fly itself when JumpDown comes on twice
		// within a few ticks (Bedrock's double tap), without saying so in its abilities. The Java
		// client's own double tap already toggles it (player_abilities, RequestAbility): with
		// both, a double-tap take-off turned flight on and off again and BDS simulated a fall
		// under the flying client (and flew under a client that had landed). So a player that
		// may fly sends no JumpDown; jumping and flying up need Jumping and WantUp only.
		if !p.self.canFly() {
			flags.Set(packet.InputFlagJumpDown)
		}
		flags.Set(packet.InputFlagJumping)
		flags.Set(packet.InputFlagWantUp)
		flags.Set(packet.InputFlagJumpCurrentRaw)
	}
	if jumpKey && !m.wasJumpKey {
		flags.Set(packet.InputFlagJumpPressedRaw)
	}
	if !jumpKey && m.wasJumpKey {
		flags.Set(packet.InputFlagJumpReleasedRaw)
	}
	// A jump: the client left the ground going up with the jump key down. The Java client also
	// jumps in the tick it starts flying (a double tap whose second press is on the ground: under
	// a low ceiling, after a short fall); BDS does not jump once it flies, so that take-off is
	// told one input later (below) and BDS gets the jump and its sprint boost first.
	jumped := jumpKey && m.wasOnGround && !m.onGround && delta[1] > 0
	if jumped && (!p.self.flying || m.flyChange == 1) {
		flags.Set(packet.InputFlagStartJumping)
	}
	m.wasJumpKey = jumpKey
	m.wasOnGround = m.onGround
	if sneaking {
		flags.Set(packet.InputFlagSneaking)
		flags.Set(packet.InputFlagSneakDown)
		flags.Set(packet.InputFlagSneakCurrentRaw)
		flags.Set(packet.InputFlagWantDown)
	}
	if sneaking && !m.wasSneaking {
		flags.Set(packet.InputFlagStartSneaking)
		flags.Set(packet.InputFlagSneakPressedRaw)
		if m.onGround {
			// BDS stops a player that starts sneaking on the ground dead (its horizontal speed
			// goes, then it builds up the sneak speed), while the Java client slides on with
			// its momentum: a sprinting player was 0.6 blocks ahead some ticks later and
			// corrected. The Java client's speed goes too.
			w := p.pk()
			w.VarInt(SelfEntityID)
			w.LpVec3(0, 0, 0)
			p.send(v777.ClientboundPlaySetEntityMotion, w)
		}
	} else if !sneaking && m.wasSneaking {
		flags.Set(packet.InputFlagStopSneaking)
		flags.Set(packet.InputFlagSneakReleasedRaw)
	}
	m.wasSneaking = sneaking
	m.shiftKey = m.keys&keyShift != 0
	// Flying down: BDS adds -0.15 per tick for WantDownSlow, like Java's sneak key (flying speed
	// x3), but -0.22 for WantDown (VerticalFlySpeedControlSystem), and SneakDown while flying
	// makes it simulate something else again. Up (WantUp, above) is +0.15 on both.
	if m.keys&keyShift != 0 && p.self.flying {
		flags.Set(packet.InputFlagWantDownSlow)
	}
	sprinting := m.sprinting
	if !sprinting && m.wasSprinting && m.srvSprint && !prevGround {
		// The Java client stops sprinting when it runs into the side of a block (not when it
		// only grazes it), in the air or as it lands. BDS ignores a sprint stop in an input
		// that starts with the player in the air (the landing input too) and sprints on
		// (sprint speed, the next jump's boost: 0.3-0.9 blocks off within a jump, the stutter
		// at block corners), and a StopSprinting sent again later does not count either. So
		// the server is told the sprint goes on until an input that starts on the ground (the
		// one after the landing, a hop's jump input too), where the stop counts.
		sprinting = true
	}
	if sprinting {
		flags.Set(packet.InputFlagSprinting)
		flags.Set(packet.InputFlagSprintDown)
	}
	if sprinting && !m.wasSprinting {
		flags.Set(packet.InputFlagStartSprinting)
	} else if !sprinting && m.wasSprinting {
		flags.Set(packet.InputFlagStopSprinting)
	}
	m.wasSprinting = sprinting
	if m.onGround {
		flags.Set(packet.InputFlagVerticalCollision)
	}
	if m.hCollision {
		flags.Set(packet.InputFlagHorizontalCollision)
	}
	if m.swungAtAir {
		flags.Set(packet.InputFlagMissedSwing)
		m.swungAtAir = false
	}
	flags.Set(packet.InputFlagBlockBreakingDelayEnabled)
	// BDS only flies the player after a RequestAbility (as a Bedrock client sends it); the
	// StartFlying/StopFlying input flags alone are ignored and it simulates a fall.
	if m.flyChange != 0 && !(m.flyChange == 1 && jumped) {
		if m.flyChange == 1 {
			flags.Set(packet.InputFlagStartFlying)
			m.flyRequested = time.Now()
		} else {
			flags.Set(packet.InputFlagStopFlying)
		}
		r.write(&packet.RequestAbility{Ability: packet.AbilityFlying, Value: m.flyChange == 1})
		m.flyChange = 0
	}

	// BDS brakes a flying player that has no move input hard (horizontal friction x0.375 in
	// creative, x0.75 otherwise, and vertical speed x0.375: VerticalFlySpeedControlSystem), while
	// the Java client glides to a stop (x0.91, x0.6). As long as the client still glides, a move
	// input just over BDS's 0.01 dead zone, alternating in sign so that it adds no speed, keeps
	// BDS gliding with it.
	inMv := mv
	if p.self.flying && mv == (mgl32.Vec2{}) && delta.Len() > 1e-4 {
		if m.glideSign >= 0 {
			m.glideSign = -1
		} else {
			m.glideSign = 1
		}
		inMv = mgl32.Vec2{0.0101 * m.glideSign, 0}
	}
	yaw, pitch := m.yaw, m.pitch
	look := lookVec(yaw, pitch)
	pk := &packet.PlayerAuthInput{
		Pitch:              pitch,
		Yaw:                yaw,
		HeadYaw:            yaw,
		Position:           vec32(pos.Add(mgl64.Vec3{0, eyeHeight, 0})),
		MoveVector:         inMv,
		RawMoveVector:      inMv,
		AnalogueMoveVector: inMv,
		InputMode:          packet.InputModeMouse,
		PlayMode:           packet.PlayModeNormal,
		InteractionModel:   packet.InteractionModelCrosshair,
		InteractPitch:      pitch,
		InteractYaw:        yaw,
		Tick:               m.tick,
		Delta:              vec32(delta),
		CameraOrientation:  vec32(look),
	}
	for _, h := range inputHooks {
		h(p, pk, &flags)
	}
	pk.InputData = flags
	if debugMove {
		p.debugInput(pk)
	}
	r.write(pk)
	for _, after := range p.afterInput {
		r.write(after)
	}
	p.afterInput = p.afterInput[:0]
	if debugChunks && m.tick%40 == 0 {
		p.log.Debug("input", "tick", m.tick, "pos", pos, "keys", m.keys)
	}
}

// sprintJumpStart reports whether the client jumped in the tick it started sprinting (delta is
// that tick's movement).
func (m *movement) sprintJumpStart(delta mgl64.Vec3) bool {
	return m.keys&keyJump != 0 && m.wasOnGround && !m.onGround && delta[1] > 0 && m.sprinting && !m.wasSprinting
}

// sprintJumpWait is how many ticks a sprint jump waits for the server's sprint attribute.
const sprintJumpWait = 20

// sprintJumpVerdict gets the server's movement attribute while a jump made in the tick the client
// started sprinting (m.sjTick) waits for it. Java's jumpFromGround adds 0.2 along the look to a
// sprinting player, and the Java client starts sprinting before it jumps. BDS sometimes starts
// the sprint of such an input at once (its first movement attribute after it has the sprint
// modifier, and its jump the boost) and sometimes only some ticks later (the first attributes say
// not sprinting): then its jump has no boost and the client would be ~0.75 blocks ahead, corrected
// a few ticks later. In that case the boost is taken back from the client at once instead.
func (p *Player) sprintJumpVerdict(sprinting bool) {
	m := &p.mv
	if m.sjTick == 0 || m.tick < m.sjTick {
		return
	}
	n := m.sjTick
	m.sjTick = 0
	if !sprinting {
		p.unboostJump(n)
	}
}

// unboostJump takes the sprint-jump boost of the jump of input tick n back: the inputs from now
// on go without it (the history of the ones sent since too, so that the server's corrections of
// them are not applied twice) and the client gets a relative teleport without the boost's
// displacement and velocity as they will be when it applies it, tpLag ticks from now (airborne:
// friction 0.546 after the jump tick, 0.91 after that).
func (p *Player) unboostJump(n uint64) {
	m := &p.mv
	a := m.sjYaw * float32(math.Pi/180)
	b := mgl64.Vec3{-float64(float32(math.Sin(float64(a)))) * 0.2, 0, float64(float32(math.Cos(float64(a)))) * 0.2}
	// extra(t): the boost's displacement after tick t; vel: its velocity after it.
	extra := func(t uint64) (disp, vel mgl64.Vec3) {
		disp, vel = b, b.Mul(0.546)
		for i := n + 1; i <= t; i++ {
			disp = disp.Add(vel)
			vel = vel.Mul(0.91)
		}
		return disp, vel
	}
	for i := range m.history {
		if e := &m.history[i]; e.tick >= n && e.tick <= m.tick {
			d, _ := extra(e.tick)
			e.pos = e.pos.Sub(d)
		}
	}
	now, _ := extra(m.tick)
	m.pos, m.lastPos = m.pos.Sub(now), m.lastPos.Sub(now)
	k := max(1, uint64(math.Round(m.tpLag)))
	disp, vel := extra(m.tick + k - 1)
	id := p.sendPositionVel(disp.Mul(-1), vel.Mul(-1), 0, 0, relX|relY|relZ|relYaw|relPitch|relDX|relDY|relDZ)
	// Client positions until it applies it: without the boost's displacement.
	m.pending = append(m.pending, pendingTeleport{id: id, offset: disp.Mul(-1), tick: m.tick})
	p.self.pos = m.pos
	m.unboosts++
	if debugMove {
		p.log.Info("MV sprint jump boost taken back", "jumpTick", n, "tick", m.tick, "lag", k, "teleport", disp.Mul(-1), "velocity", vel.Mul(-1))
	}
}

// lookVec is the direction of a look (yaw, pitch in degrees, Minecraft's convention).
func lookVec(yaw, pitch float32) mgl64.Vec3 {
	y, pt := float64(yaw)*math.Pi/180, float64(pitch)*math.Pi/180
	return mgl64.Vec3{-math.Cos(pt) * math.Sin(y), -math.Sin(pt), math.Cos(pt) * math.Cos(y)}
}
