package bridge

import (
	"math"
	"sort"

	"delta/core/internal/anim"
	"delta/core/internal/molang"

	"github.com/df-mc/dragonfly/server/block/cube"
	jitem "github.com/ezchr/go-mcjava/item"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Animated custom mobs (entities_custom.go shows them; internal/anim evaluates the pack's
// animations).
//
// An animated mob is drawn with one item_display per part of its model (internal/javapack
// decides the parts: at most anim.MaxParts). The parts ride the mob's own display, so the
// client moves them with it and nothing has to be sent for a mob that only walks straight.
// Each part's transformation (translation, left rotation, scale) is the pose of its bone,
// sent every few ticks with the display's interpolation duration set to that interval: the
// client interpolates in between. The mob's yaw is each part's own entity rotation, which the
// client interpolates over the same time (pos/rot interpolation duration).
//
// Budget, per player:
//
//   - the animNear nearest animated mobs are stepped every animNearTicks ticks, the next
//     animFar every animFarTicks;
//   - a mob further away than animRange blocks, or beyond those counts, is one display with
//     the model of the whole mob standing idle (the animation has to look right from close,
//     not from 50 blocks);
//   - a mob the player cannot see takes no slot: one behind a hill or in a cave under the
//     player's feet is static too (animSees);
//   - only the parts whose pose changed are sent, and a still mob costs nothing.
//
// The worst case is every part of every animated mob moving, all of them mobs with more moving
// bones than anim.MaxParts (32): 6*32 displays at 6.7 Hz plus 10*32 at 3.3 Hz = 2350 entity data
// packets a second (about 40 bytes each), and 512 display entities. Most mobs have far fewer
// parts (16 Pokemon around a player: about 250 displays).
const (
	animNearTicks = 3
	animFarTicks  = 6
	animRange     = 40.0 // blocks: closer mobs may be animated
	animRangeOut  = 48.0 // an animated mob stays animated up to here
	animRankTicks = 10   // how often the mobs to animate are chosen again
	// Line of sight: a mob is tested every animSightTicks ticks; an animated mob stays animated
	// until it failed animSightMisses tests in a row (a mob at the edge of a hill does not
	// flicker), a static one is animated at its first test that sees it.
	animSightTicks  = 20
	animSightMisses = 3
)

// animNear and animFar are the animation slots per player (variables for tests).
var animNear, animFar = 6, 10

// animParts is the part budget of a mob ([core.options] anim_parts: a number, or "all" =
// anim.AllParts: a part for every bone that moves). animFullMobs ([core.options]
// anim_full_mobs) is how many of the nearest animated mobs are shown in full detail when
// animParts is a number: the others get the set merged to animParts. A mob that is shown in
// full detail keeps it until it is animFullKeep places beyond that count, so two mobs at the
// same distance do not keep swapping.
var (
	animParts    = anim.MaxParts
	animFullMobs = 0
)

const animFullKeep = 2

// Display entity data indices (26.3) beyond those in entities_custom.go.
const (
	displayInterpDelay    = 8  // int: ticks until the interpolation starts (sending it restarts it)
	displayInterpDuration = 9  // int: transformation interpolation duration
	displayLeftRotation   = 13 // quaternion
	serQuaternion         = 40
)

// debugAnim ([core.options] debug_anim) logs when a mob's displays come and go and what its
// controllers play; animOff (animations = "off") shows every custom mob as its static model.
var debugAnim, animOff bool

// One-shot animations (attacks, hurt, cries). What starts them, and what the bridge has of it:
//
//   - an actor property or flag a controller's transition tests (the Cobblemon port plays its
//     attack clips on the property cobblemon:primary): nothing to do, the controllers run on
//     the actor's properties and flags;
//   - AnimateEntity, a script's playAnimation: the server names the animation and the actors;
//     it is played once on top of what the scripts play (anim.State.Play), by id or by the
//     entity file's short name, fading out over the packet's blend-out time;
//   - ActorEvent StartAttacking (a mob's arm swing): v.attack_time runs from 0 to 1 over
//     attackSwingTicks, as on a Bedrock client, and q.is_attacking holds meanwhile; the flag
//     behind q.is_delayed_attacking is entity data;
//   - ActorEvent Hurt: q.hurt_time counts down from hurtTicks; Death: q.is_alive is 0 (a dead
//     mob keeps its displays, so a faint animation can play).
//
// AnimateEntity's stop expression ends its animation (the default, "the animation finished",
// is once through, also for a looping animation). Not supported: its next state and controller
// name (the animation always returns to what the scripts play), and what a client works out by
// itself with no packet (q.is_jumping, the swing of a held item).
const (
	attackSwingTicks = 6
	hurtTicks        = 10
)

// animPart is one part display of an animated mob.
type animPart struct {
	id      int32
	sent    anim.PartPose // as sent: scaled by the mob's scale
	hasSent bool
}

// modelAnim is the animation state of a shown custom mob.
type modelAnim struct {
	model *anim.Model
	st    *anim.State
	parts []animPart // nil: shown as the one static model
	// set is the part set the displays show (the model's Parts or Full), full which.
	set  []anim.Part
	full bool
	// period is the ticks between steps while animated.
	period   int
	lastStep uint64
	// Movement since the last step, for the speed queries.
	lastPos [3]float64
	hasPos  bool
	pitch   float32 // the head pitch Bedrock sent (the display itself is kept level)
	sentYaw byte
	// attackAt and hurtAt are the animation ticks of the mob's last attack and hurt events
	// (0: none).
	attackAt, hurtAt uint64
	playing          string
	distance         float64 // to the player, at the last ranking
	// Line of sight: the last test's tick (0: never tested), its result with the hysteresis
	// applied, and the tests failed in a row.
	sightAt uint64
	seen    bool
	unseen  int
}

// newModelAnim starts the animation state of a mob whose model has a rig.
func (p *Player) newModelAnim(e *entity, m *anim.Model) *modelAnim {
	if m == nil {
		return nil
	}
	rig := m.Rig()
	if rig == nil {
		return nil
	}
	a := &modelAnim{model: m, st: rig.NewState(e.rid)}
	return a
}

// animFlags are the Bedrock entity flags behind Molang's yes/no queries.
var animFlags = map[molang.Query]int{
	molang.QIsSleeping: protocol.EntityDataFlagSleeping, molang.QIsSneaking: protocol.EntityDataFlagSneaking,
	molang.QIsSprinting: protocol.EntityDataFlagSprinting, molang.QIsSwimming: protocol.EntityDataFlagSwimming,
	molang.QIsGliding: protocol.EntityDataFlagGliding, molang.QIsBaby: protocol.EntityDataFlagBaby,
	molang.QIsSitting: protocol.EntityDataFlagSitting, molang.QIsAngry: protocol.EntityDataFlagAngry,
	molang.QIsTamed: protocol.EntityDataFlagTamed, molang.QIsSheared: protocol.EntityDataFlagSheared,
	molang.QIsCharged: protocol.EntityDataFlagCharged, molang.QIsOnFire: protocol.EntityDataFlagOnFire,
	molang.QIsInvisible: protocol.EntityDataFlagInvisible, molang.QIsStanding: protocol.EntityDataFlagStanding,
	molang.QIsDelayedAttacking: protocol.EntityDataFlagDelayedAttack, molang.QIsLeashed: protocol.EntityDataFlagLeashed,
	molang.QIsSaddled: protocol.EntityDataFlagSaddled, molang.QIsChested: protocol.EntityDataFlagChested,
	molang.QIsIgnited: protocol.EntityDataFlagIgnited, molang.QIsStunned: protocol.EntityDataFlagStunned,
	molang.QIsRoaring: protocol.EntityDataFlagRoaring, molang.QIsResting: protocol.EntityDataFlagResting,
	molang.QIsOrphaned: protocol.EntityDataFlagOrphaned, molang.QIsPowered: protocol.EntityDataFlagPowered,
	molang.QIsInLove: protocol.EntityDataFlagInLove, molang.QIsBribed: protocol.EntityDataFlagBribed,
	molang.QIsTransforming: protocol.EntityDataFlagTransforming, molang.QIsCasting: protocol.EntityDataFlagCasting,
	molang.QIsStackable: protocol.EntityDataFlagStackable, molang.QIsInterested: protocol.EntityDataFlagInterested,
	molang.QIsEating: protocol.EntityDataFlagEating, molang.QIsLayingDown: protocol.EntityDataFlagLayingDown,
	molang.QIsShaking: protocol.EntityDataFlagShaking, molang.QIsCelebrating: protocol.EntityDataFlagCelebrating,
	molang.QIsAdmiring: protocol.EntityDataFlagAdmiring, molang.QIsEmoting: protocol.EntityDataFlagEmoting,
	molang.QIsDigging: protocol.EntityDataFlagDigging, molang.QIsRolling: protocol.EntityDataFlagRolling,
	molang.QIsWallClimbing: protocol.EntityDataFlagWallClimbing, molang.QCanFly: protocol.EntityDataFlagFly,
	molang.QCanSwim: protocol.EntityDataFlagSwim, molang.QCanWalk: protocol.EntityDataFlagWalk,
	molang.QCanClimb: protocol.EntityDataFlagClimb,
}

// animInputs fills in what is known about e for a step dt seconds after the last.
func (p *Player) animInputs(e *entity, a *modelAnim, dt float64) {
	in := &a.st.In
	pos := [3]float64{e.pos[0], e.pos[1], e.pos[2]}
	if a.hasPos && dt > 0 {
		dx, dy, dz := pos[0]-a.lastPos[0], pos[1]-a.lastPos[1], pos[2]-a.lastPos[2]
		h := math.Hypot(dx, dz)
		if h > 16 { // a teleport, not a walk
			h, dy = 0, 0
		}
		// Half of the last value stays: positions come in bursts, and a pack's "is it moving"
		// threshold should not flicker with them.
		in.GroundSpeed = (in.GroundSpeed + h/dt) / 2
		in.VerticalSpeed = (in.VerticalSpeed + dy/dt) / 2
		if in.GroundSpeed < 0.05 {
			in.GroundSpeed = 0
		}
		if math.Abs(in.VerticalSpeed) < 0.05 {
			in.VerticalSpeed = 0
		}
	} else if dt > 0 {
		in.GroundSpeed, in.VerticalSpeed = 0, 0
	}
	a.lastPos, a.hasPos = pos, true
	in.OnGround = e.onGround
	in.InWater = p.inWater(e.pos)
	in.BodyYaw = float64(e.yaw)
	hy := math.Mod(float64(e.head-e.yaw), 360)
	if hy > 180 {
		hy -= 360
	} else if hy < -180 {
		hy += 360
	}
	in.HeadYaw, in.HeadPitch = hy, float64(a.pitch)
	if v, ok := e.meta[protocol.EntityDataKeyVariant].(int32); ok {
		in.Variant = float64(v)
	}
	if v, ok := e.meta[protocol.EntityDataKeyMarkVariant].(int32); ok {
		in.MarkVariant = float64(v)
	}
	if v, ok := e.meta[protocol.EntityDataKeySkinID].(int32); ok {
		in.SkinID = float64(v)
	}
	in.Health = float64(e.health)
	now := p.packs.animTicks
	in.AttackTime, in.HurtTime = 0, 0
	if a.attackAt != 0 && now-a.attackAt < attackSwingTicks {
		in.AttackTime = float64(now-a.attackAt+1) / attackSwingTicks
	}
	if a.hurtAt != 0 && now-a.hurtAt < hurtTicks {
		in.HurtTime = float64(hurtTicks - (now - a.hurtAt))
	}
	if in.Flag == nil {
		in.Flag = func(q molang.Query) bool {
			switch q {
			case molang.QIsRiding:
				return e.vehicle != 0
			case molang.QHasRider:
				return len(e.riders) > 0
			case molang.QIsAlive:
				return !e.dead
			}
			if f, ok := animFlags[q]; ok {
				return e.flag(f)
			}
			return false
		}
		in.Prop = func(name string) (float64, bool) {
			switch v := e.props[name].(type) {
			case bool:
				if v {
					return 1, true
				}
				return 0, true
			case int32:
				return float64(v), true
			case float32:
				return float64(v), true
			case string:
				return molang.Intern(v), true
			}
			return 0, false
		}
	}
}

// animTick chooses the mobs to animate and steps the ones that are due.
func (p *Player) animTick() {
	ps := &p.packs
	if len(ps.ents) == 0 {
		return
	}
	ps.animTicks++
	now := ps.animTicks
	if now%animRankTicks == 0 || ps.animRankNow {
		ps.animRankNow = false
		p.animRank()
	}
	for rid, me := range ps.ents {
		a := me.anim
		if a == nil || a.parts == nil || now-a.lastStep < uint64(a.period) {
			continue
		}
		e := p.ents.shown[rid]
		if e == nil || !e.spawned {
			continue
		}
		p.animStep(e, me, float64(now-a.lastStep)/20)
	}
}

// animRank gives the nearest animated mobs their displays and takes them from the others.
func (p *Player) animRank() {
	ps := &p.packs
	type cand struct {
		e  *entity
		me *modelEntity
	}
	var cands []cand
	for rid, me := range ps.ents {
		a := me.anim
		if a == nil {
			continue
		}
		e := p.ents.shown[rid]
		if e == nil || !e.spawned {
			continue
		}
		if !a.model.Moves() || ps.failed || animOff {
			// Nothing to animate: the scripts still give its size.
			p.animStill(e, me)
			continue
		}
		a.distance = e.pos.Sub(p.mv.pos).Len()
		limit := animRange
		if a.parts != nil {
			limit = animRangeOut
		}
		if a.distance > limit {
			if a.parts != nil {
				p.animStop(e, me)
			}
			p.animStill(e, me)
			continue
		}
		if !p.animSees(e, me) {
			if a.parts != nil {
				p.animStop(e, me)
			}
			p.animStill(e, me)
			continue
		}
		cands = append(cands, cand{e, me})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i].me.anim, cands[j].me.anim
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		return cands[i].e.rid < cands[j].e.rid
	})
	for i, c := range cands {
		a := c.me.anim
		full := a.model.Full != nil && (i < animFullMobs || (a.full && a.parts != nil && i < animFullMobs+animFullKeep))
		switch {
		case i < animNear:
			p.animStart(c.e, c.me, animNearTicks, full)
		case i < animNear+animFar:
			p.animStart(c.e, c.me, animFarTicks, full)
		default:
			if a.parts != nil {
				p.animStop(c.e, c.me)
			}
			p.animStill(c.e, c.me)
		}
	}
}

// animStill keeps a mob that is not animated up to date: its scripts run (once a ranking), for
// the scale they give it.
func (p *Player) animStill(e *entity, me *modelEntity) {
	a := me.anim
	now := p.packs.animTicks
	p.animInputs(e, a, float64(now-a.lastStep)/20)
	a.st.Step(float64(now-a.lastStep) / 20)
	a.lastStep = now
	if s := float32(a.st.Scale()); s != me.script {
		me.script = s
		p.sendModelData(e, me, false)
	}
}

// animStart makes a mob animated (if it is not) with a step every period ticks.
func (p *Player) animStart(e *entity, me *modelEntity, period int, full bool) {
	a := me.anim
	typ := p.ver().BuiltinID("minecraft:entity_type", "minecraft:item_display")
	if typ < 0 {
		return
	}
	set := a.model.Parts
	if full = full && a.model.Full != nil; full {
		set = a.model.Full
	}
	if a.parts != nil && a.full != full {
		// The other part set: the old displays go and the new ones come in one bundle, so the
		// client never draws both or neither.
		p.animBundle(true)
		defer p.animBundle(false)
		p.animStop(e, me)
	}
	if a.parts != nil {
		if a.period != period {
			a.period = period
			for i := range a.parts {
				w := p.pk()
				w.VarInt(a.parts[i].id)
				writeIntData(w, displayInterpDuration, int32(period))
				writeIntData(w, displayPosInterp, int32(period))
				w.Byte(0xff)
				p.send(v777.ClientboundPlaySetEntityData, w)
			}
		}
		return
	}
	now := p.packs.animTicks
	p.animInputs(e, a, float64(now-a.lastStep)/20)
	a.st.Step(float64(now-a.lastStep) / 20)
	a.lastStep, a.period = now, period
	me.script = float32(a.st.Scale())
	scale := me.scale * me.script
	a.set, a.full = set, full
	a.parts = make([]animPart, len(set))
	a.sentYaw = angleByte(e.yaw)
	p.animBundle(true)
	for i, mp := range set {
		pt := &a.parts[i]
		pt.id = p.ents.next
		p.ents.next++
		w := p.pk()
		w.VarInt(pt.id)
		w.UUID(entityUUID(uint64(pt.id) | 1<<62))
		w.VarInt(typ)
		w.Float64(e.pos[0])
		w.Float64(e.pos[1])
		w.Float64(e.pos[2])
		w.LpVec3(0, 0, 0)
		w.Angle(0)
		w.Angle(e.yaw)
		w.Angle(e.yaw)
		w.VarInt(0)
		p.send(v777.ClientboundPlayAddEntity, w)

		pose := scaledPose(a.st.Pose(mp.Bone, mp.K), scale)
		pt.sent, pt.hasSent = pose, true
		w = p.pk()
		w.VarInt(pt.id)
		writeIntData(w, displayInterpDuration, int32(period))
		writeIntData(w, displayPosInterp, int32(period))
		writePose(w, pose, true)
		w.Byte(displayItemIdx)
		w.VarInt(serItemStack)
		var js jitem.Stack
		js.Reset()
		js.ID, js.Count = javaPaper, 1
		js.ItemModel = mp.Model
		js.Add(jitem.CompItemModel)
		js.CustomModelData.Floats = append(js.CustomModelData.Floats[:0], float32(mp.Index))
		js.Add(jitem.CompCustomModelData)
		js.EncodeFor(w, p.win.proto())
		w.Byte(0xff)
		p.send(v777.ClientboundPlaySetEntityData, w)
		e.parts = append(e.parts, pt.id)
	}
	p.sendPassengers(e)
	p.sendModelData(e, me, true) // the mob's own display now shows nothing
	p.animBundle(false)
	if debugAnim {
		p.log.Info("anim start", "type", e.bedrock, "id", e.id, "parts", len(a.parts), "full", full, "period", period, "distance", math.Round(a.distance))
	}
}

// animStop takes a mob's part displays away: it is the one static model again.
func (p *Player) animStop(e *entity, me *modelEntity) {
	a := me.anim
	if a == nil || a.parts == nil {
		return
	}
	p.animBundle(true)
	w := p.pk()
	w.VarInt(int32(len(a.parts)))
	for _, pt := range a.parts {
		w.VarInt(pt.id)
	}
	p.send(v777.ClientboundPlayRemoveEntities, w)
	a.parts, e.parts = nil, nil
	if e.spawned {
		p.sendModelData(e, me, true)
	}
	p.animBundle(false)
	if debugAnim {
		p.log.Info("anim stop", "type", e.bedrock, "id", e.id)
	}
}

// animStep advances an animated mob by dt seconds and sends the parts that moved.
func (p *Player) animStep(e *entity, me *modelEntity, dt float64) {
	a := me.anim
	p.animInputs(e, a, dt)
	a.st.Step(dt)
	a.lastStep = p.packs.animTicks
	me.script = float32(a.st.Scale())
	scale := me.scale * me.script
	if debugAnim {
		s := a.st.Playing()
		if o := a.st.Overlaid(); o != "" {
			s += " + " + o
		}
		if s != a.playing {
			a.playing = s
			p.log.Info("anim", "type", e.bedrock, "id", e.id, "playing", s, "speed", math.Round(a.st.In.GroundSpeed*10)/10,
				"ground", a.st.In.OnGround, "water", a.st.In.InWater)
		}
	}
	yaw := angleByte(e.yaw)
	turned := yaw != a.sentYaw
	a.sentYaw = yaw
	bundled := false
	for i, mp := range a.set {
		pt := &a.parts[i]
		pose := scaledPose(a.st.Pose(mp.Bone, mp.K), scale)
		moved := !pt.hasSent || !pose.Near(pt.sent)
		if !moved && !turned {
			continue
		}
		if !bundled {
			bundled = true
			p.send(v777.ClientboundPlayBundleDelimiter, p.pk())
		}
		if moved {
			w := p.pk()
			w.VarInt(pt.id)
			writeIntData(w, displayInterpDelay, 0)
			writePose(w, pose, !pt.hasSent || pose.S != pt.sent.S)
			w.Byte(0xff)
			p.send(v777.ClientboundPlaySetEntityData, w)
			pt.sent, pt.hasSent = pose, true
		}
		if turned {
			w := p.pk()
			w.VarInt(pt.id)
			if p.ver().Native() {
				w.Bool(false)
				w.Byte(yaw)
				w.Byte(0)
			} else {
				w.Byte(yaw)
				w.Byte(0)
				w.Bool(false)
			}
			p.send(v777.ClientboundPlayMoveEntityRot, w)
		}
	}
	if bundled {
		p.send(v777.ClientboundPlayBundleDelimiter, p.pk())
	}
}

// dropAnimParts removes a mob's part displays from the client (the mob itself goes or went).
func (p *Player) dropAnimParts(e *entity, me *modelEntity) {
	a := me.anim
	if a == nil || len(a.parts) == 0 {
		return
	}
	w := p.pk()
	w.VarInt(int32(len(a.parts)))
	for _, pt := range a.parts {
		w.VarInt(pt.id)
	}
	p.send(v777.ClientboundPlayRemoveEntities, w)
	a.parts = nil
	if e != nil {
		e.parts = nil
	}
}

func scaledPose(ps anim.PartPose, scale float32) anim.PartPose {
	for i := 0; i < 3; i++ {
		ps.T[i] *= scale
		ps.S[i] *= scale
	}
	return ps
}

// writePose writes a part's transformation as display entity data.
func writePose(w *wire.Writer, ps anim.PartPose, withScale bool) {
	w.Byte(displayTranslation)
	w.VarInt(serVector3Data)
	w.Float32(ps.T[0])
	w.Float32(ps.T[1])
	w.Float32(ps.T[2])
	w.Byte(displayLeftRotation)
	w.VarInt(serQuaternion)
	w.Float32(ps.Q[0])
	w.Float32(ps.Q[1])
	w.Float32(ps.Q[2])
	w.Float32(ps.Q[3])
	if withScale {
		w.Byte(displayScaleIdx)
		w.VarInt(serVector3Data)
		w.Float32(ps.S[0])
		w.Float32(ps.S[1])
		w.Float32(ps.S[2])
	}
}

func writeIntData(w *wire.Writer, index byte, v int32) {
	w.Byte(index)
	w.VarInt(serInt)
	w.VarInt(v)
}

func init() {
	OnTick((*Player).animTick)
	OnBedrock(packet.IDAnimateEntity, func(p *Player, pk *packet.AnimateEntity) {
		for _, rid := range pk.EntityRuntimeIDs {
			me := p.packs.ents[rid]
			if me == nil || me.anim == nil {
				continue
			}
			ok := me.anim.model.Play(me.anim.st, pk.Animation, float64(pk.BlendOutTime), pk.StopCondition)
			if debugAnim {
				p.log.Info("anim play", "rid", rid, "animation", pk.Animation, "found", ok, "controller", pk.Controller, "next", pk.NextState,
					"stop", pk.StopCondition, "blend_out", pk.BlendOutTime)
			}
		}
	})
	OnBedrock(packet.IDActorEvent, func(p *Player, pk *packet.ActorEvent) {
		me := p.packs.ents[pk.EntityRuntimeID]
		if me == nil || me.anim == nil {
			return
		}
		switch pk.EventType {
		case packet.ActorEventStartAttacking:
			me.anim.attackAt = max(p.packs.animTicks, 1)
		case packet.ActorEventHurt:
			me.anim.hurtAt = max(p.packs.animTicks, 1)
		}
	})
}

// animSees reports whether the player can see a mob, well enough to spend an animation slot on
// it: the line of sight test, run every animSightTicks ticks per mob, with hysteresis.
func (p *Player) animSees(e *entity, me *modelEntity) bool {
	a := me.anim
	now := p.packs.animTicks
	if a.sightAt != 0 && now-a.sightAt < animSightTicks {
		return a.seen
	}
	a.sightAt = max(now, 1)
	if p.sightLine(e, me) {
		a.seen, a.unseen = true, 0
		return true
	}
	a.unseen++
	if a.parts != nil && a.unseen < animSightMisses {
		return true
	}
	a.seen = false
	return false
}

// sightLine reports whether a straight line from the player's eyes reaches the mob's box
// without crossing an opaque block. Five points of the box are tried (low, middle and high on
// its axis, and its two sides at half height as the player sees them), so a mob half behind a
// hill or a wall's corner counts as seen. Blocks of chunks the bridge does not have count as
// see-through.
func (p *Player) sightLine(e *entity, me *modelEntity) bool {
	if p.world == nil {
		return true
	}
	eye := p.mv.pos.Add(mgl64.Vec3{0, eyeHeight, 0})
	scale := float64(me.scale * me.script)
	if scale <= 0 {
		scale = 1
	}
	w, h := float64(me.w)*scale, float64(me.h)*scale
	// The side direction: horizontal, across the line from the player.
	dx, dz := e.pos[0]-eye[0], e.pos[2]-eye[2]
	side := mgl64.Vec3{}
	if l := math.Hypot(dx, dz); l > 1e-6 {
		side = mgl64.Vec3{-dz / l * w / 2, 0, dx / l * w / 2}
	}
	mid := e.pos.Add(mgl64.Vec3{0, h * 0.5, 0})
	for _, pt := range [5]mgl64.Vec3{mid, e.pos.Add(mgl64.Vec3{0, h * 0.95, 0}), e.pos.Add(mgl64.Vec3{0, h * 0.1, 0}), mid.Add(side), mid.Sub(side)} {
		if p.clearLine(eye, pt) {
			return true
		}
	}
	return false
}

// clearLine walks the blocks from one point to another (every block the segment passes
// through) and reports whether none of them blocks the view. A block blocks it when it is
// solid and lets no light through: glass, leaves, water, fences, slabs and plants do not.
func (p *Player) clearLine(from, to mgl64.Vec3) bool {
	w, pal := p.world, p.rem.pal
	d := to.Sub(from)
	pos, end := blockPosOf(from), blockPosOf(to)
	var step [3]int
	var tMax, tDelta [3]float64
	for i := 0; i < 3; i++ {
		switch {
		case d[i] > 0:
			step[i], tMax[i], tDelta[i] = 1, (math.Floor(from[i])+1-from[i])/d[i], 1/d[i]
		case d[i] < 0:
			step[i], tMax[i], tDelta[i] = -1, (from[i]-math.Floor(from[i]))/-d[i], -1/d[i]
		default:
			tMax[i], tDelta[i] = math.Inf(1), math.Inf(1)
		}
	}
	for n := 0; n < 256 && pos != end; n++ {
		i := 0
		if tMax[1] < tMax[i] {
			i = 1
		}
		if tMax[2] < tMax[i] {
			i = 2
		}
		if tMax[i] > 1 {
			break
		}
		pos[i] += step[i]
		tMax[i] += tDelta[i]
		if b, ok := w.Block(cube.Pos(pos)); ok && pal.Solid(b) && pal.Filter(b) >= 15 {
			return false
		}
	}
	return true
}

// animBundle opens or closes a packet bundle (the client applies a bundle's packets in one
// frame). Bundles do not nest on the wire: an inner one is part of the outer. A bundle holds at
// most 4096 packets; the largest sent here is a part set swap (two packets per new part, three
// more for the rest).
func (p *Player) animBundle(open bool) {
	ps := &p.packs
	if open {
		if ps.animBundles == 0 {
			p.send(v777.ClientboundPlayBundleDelimiter, p.pk())
		}
		ps.animBundles++
		return
	}
	ps.animBundles--
	if ps.animBundles == 0 {
		p.send(v777.ClientboundPlayBundleDelimiter, p.pk())
	}
}
