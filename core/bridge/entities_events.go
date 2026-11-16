package bridge

import (
	"math"
	"time"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Entity events and animations, the player's own effects, riding, and the Java client's attacks
// and clicks on entities.

// selfEffect is one of the player's own effects.
type selfEffect struct {
	amplifier, duration int32 // duration in ticks, -1 infinite
	particles, ambient  bool
	at                  time.Time
}

// rideState is the vehicle the player rides on the server.
type rideState struct {
	uniq int64  // 0: not riding
	rid  uint64 // its runtime id
	// driving: the Java client drives it (move_vehicle); it moves it itself.
	driving bool
	// pos, yaw, pitch: where the Java client last put the vehicle (move_vehicle).
	pos        mgl64.Vec3
	yaw, pitch float32
	set        bool
	boat       bool
	// corrections counts the server's corrections of the vehicle.
	corrections int
}

// vehicleNudge is how far (blocks) a vehicle correction must be off the Java client's prediction
// to be sent to it.
const vehicleNudge = 0.05

// actorEvents maps Bedrock actor events to Java entity events.
var actorEvents = map[byte]byte{
	packet.ActorEventStartAttacking:      jevAttack,
	packet.ActorEventTamingFailed:        jevTamingFailed,
	packet.ActorEventTamingSucceeded:     jevTamingSucceeded,
	packet.ActorEventShakeWetness:        jevShake,
	packet.ActorEventEatGrass:            jevEatGrass,
	packet.ActorEventStartOfferFlower:    jevOfferFlower,
	packet.ActorEventStopOfferFlower:     jevStopFlower,
	packet.ActorEventLoveHearts:          jevInLove,
	packet.ActorEventInLoveHearts:        jevInLove,
	packet.ActorEventVillagerAngry:       jevVillagerAngry,
	packet.ActorEventVillagerHappy:       jevVillagerHappy,
	packet.ActorEventWitchHatMagic:       jevWitchMagic,
	packet.ActorEventFireworksExplode:    jevFirework,
	packet.ActorEventZombieConverting:    jevZombieConvert,
	packet.ActorEventSquidFleeing:        jevSquidAnim,
	packet.ActorEventGuardianAttackSound: jevGuardianSound,
	packet.ActorEventTalismanActivate:    jevTotem,
}

func init() {
	OnBedrock(packet.IDActorEvent, (*Player).actorEvent)
	OnBedrock(packet.IDAnimate, func(p *Player, pk *packet.Animate) {
		if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
			return // the Java client animates itself
		}
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok || !e.spawned {
			return
		}
		switch pk.ActionType {
		case packet.AnimateActionSwingArm:
			if e.living() {
				p.sendSwing(e.id, pk.Hand == 1)
			}
		case packet.AnimateActionCriticalHit:
			p.sendAnimate(e.id, 1)
		case packet.AnimateActionMagicCriticalHit:
			p.sendAnimate(e.id, 2)
		case packet.AnimateActionStopSleep:
			if e.t.kind&entPlayer != 0 { // the Java client takes the entity for a player
				p.sendAnimate(e.id, 0)
			}
		}
	})
	OnBedrock(packet.IDTakeItemActor, func(p *Player, pk *packet.TakeItemActor) {
		item, ok := p.ents.shown[pk.ItemEntityRuntimeID]
		if !ok || !item.spawned {
			return
		}
		taker, ok := p.ents.Lookup(pk.TakerEntityRuntimeID)
		if !ok {
			return
		}
		w := p.pk()
		w.VarInt(item.id)
		w.VarInt(taker)
		w.VarInt(int32(max(1, item.item.Count)))
		p.send(v777.ClientboundPlayTakeItemEntity, w)
	})
	OnBedrock(packet.IDMobEffect, (*Player).mobEffect)
	OnBedrock(packet.IDSetActorLink, func(p *Player, pk *packet.SetActorLink) { p.applyLink(pk.EntityLink) })
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			clear(p.ents.effects)
			return
		}
		// The Java respawn into the new dimension drops the client's effects; the server keeps
		// them without saying so again.
		p.ents.resendEffects = len(p.ents.effects) > 0
	})
	OnTick(func(p *Player) {
		if p.ents.resendEffects {
			p.ents.resendEffects = false
			for id, ef := range p.ents.effects {
				p.sendEffect(id, ef)
			}
		}
	})
	respawnHooks = append(respawnHooks, func(p *Player) { clear(p.ents.effects) })

	// Java -> Bedrock.
	OnJava(v777.ServerboundPlayAttack, func(p *Player, r *wireReader) error {
		target := r.VarInt()
		if r.Err != nil || p.rem == nil {
			return r.Err
		}
		e, ok := p.ents.byJava[target]
		if !ok {
			return nil
		}
		if e.bedrock == "minecraft:frame" {
			p.useFrame(e, true)
			return nil
		}
		if p.ver().Native() {
			// 26.2 sends swing for every swing (config.go); 26.3 only the attack.
			p.Write(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: p.rem.gd.EntityRuntimeID})
		}
		p.useOnEntity(e, protocol.UseItemOnEntityActionAttack, mgl32.Vec3{})
		return nil
	})
	OnJava(v777.ServerboundPlayInteract, func(p *Player, r *wireReader) error {
		target := r.VarInt()
		hand := r.VarInt()
		cx, cy, cz := r.LpVec3()
		r.Bool() // sneaking
		if r.Err != nil || p.rem == nil || hand != 0 {
			return r.Err
		}
		e, ok := p.ents.byJava[target]
		if !ok {
			return nil
		}
		if e.bedrock == "minecraft:frame" {
			p.useFrame(e, false)
			return nil
		}
		p.useOnEntity(e, protocol.UseItemOnEntityActionInteract, mgl32.Vec3{float32(cx), float32(cy), float32(cz)})
		return nil
	})
	OnJava(v777.ServerboundPlayPlayerInput, func(p *Player, r *wireReader) error {
		sneak := r.Byte()&keyShift != 0
		if r.Err != nil {
			return r.Err
		}
		// Sneaking gets off the vehicle (a Bedrock client asks with an Interact).
		if ride := &p.ents.ride; p.rem != nil && ride.uniq != 0 && sneak && !p.ents.rideSneak {
			p.Write(&packet.Interact{ActionType: packet.InteractActionLeaveVehicle, TargetEntityRuntimeID: ride.rid})
		}
		p.ents.rideSneak = sneak
		return nil
	})
	OnJava(v777.ServerboundPlayMoveVehicle, func(p *Player, r *wireReader) error {
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		yaw, pitch := r.Float32(), r.Float32()
		r.Bool() // on ground
		if r.Err != nil || p.ents.ride.uniq == 0 || !valid(x) || !valid(y) || !valid(z) {
			return r.Err
		}
		ride := &p.ents.ride
		ride.pos, ride.yaw, ride.pitch, ride.set, ride.driving = mgl64.Vec3{x, y, z}, yaw, pitch, true, true
		if e, ok := p.ents.shown[ride.rid]; ok {
			e.pos, e.yaw, e.pitch = ride.pos, yaw, pitch
		}
		// The player rides along: chunks follow, and the inputs carry the place.
		p.mv.pos = ride.pos
		p.self.pos = ride.pos
		p.sendCentre(ride.pos, false)
		return nil
	})
	OnInput(func(p *Player, pk *packet.PlayerAuthInput, flags *protocol.InputFlags) {
		ride := &p.ents.ride
		if ride.uniq == 0 {
			return
		}
		pk.ClientPredictedVehicle = protocol.Option(ride.uniq)
		flags.Set(packet.InputFlagClientPredictedVehicle)
		e, ok := p.ents.shown[ride.rid]
		if !ok {
			return
		}
		// In a client-predicted vehicle a Bedrock client's position is the vehicle's (Bedrock's
		// actor position: Java's plus the type's offset) and VehicleRotation its rotation; the
		// server simulates the vehicle from the inputs and corrects that prediction.
		at, yaw, pitch := e.pos, e.yaw, e.pitch
		if ride.set {
			at, yaw, pitch = ride.pos, ride.yaw, ride.pitch
		}
		if ride.boat {
			yaw += 90
		}
		pk.VehicleRotation = protocol.Option(mgl32.Vec2{pitch, yaw})
		pk.Position = vec32(at.Add(mgl64.Vec3{0, e.t.yOff, 0}))
	})
	vehicleCorrectionHooks = append(vehicleCorrectionHooks, func(p *Player, pk *packet.CorrectPlayerMovePrediction) {
		ride := &p.ents.ride
		e, ok := p.ents.shown[ride.rid]
		if ride.uniq == 0 || !ok {
			return
		}
		at := vec64(pk.Position).Sub(mgl64.Vec3{0, e.t.yOff, 0})
		pitch, yaw := pk.Rotation[0], pk.Rotation[1]
		if ride.boat {
			yaw -= 90
		}
		ride.corrections++
		if ride.set {
			// Corrections of a few hundredths (waves, rounding) would only make the vehicle
			// jitter on the Java client: keep its prediction.
			dy := math.Mod(float64(yaw-ride.yaw)+540, 360) - 180
			if at.Sub(ride.pos).Len() < vehicleNudge && math.Abs(dy) < 3 {
				return
			}
		}
		if ride.corrections <= 20 || ride.corrections%50 == 0 {
			p.log.Debug("vehicle corrected", "n", ride.corrections, "to", at, "had", ride.pos, "yaw", yaw)
		}
		ride.pos, ride.yaw, ride.pitch, ride.set = at, yaw, pitch, true
		e.pos, e.yaw, e.pitch = at, yaw, pitch
		w := p.pk()
		w.Float64(at[0])
		w.Float64(at[1])
		w.Float64(at[2])
		w.Float32(yaw)
		w.Float32(pitch)
		p.send(v777.ClientboundPlayMoveVehicle, w)
		p.mv.pos = at
	})
}

// actorEvent shows an ActorEvent.
func (p *Player) actorEvent(pk *packet.ActorEvent) {
	if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
		switch pk.EventType {
		case packet.ActorEventHurt:
			p.sendHurt(SelfEntityID) // the camera shake and hurt sound
		case packet.ActorEventTalismanActivate:
			p.sendEntityEvent(SelfEntityID, jevTotem)
		}
		return
	}
	e, ok := p.ents.shown[pk.EntityRuntimeID]
	if !ok || !e.spawned {
		return
	}
	switch pk.EventType {
	case packet.ActorEventHurt:
		if e.bedrock == "minecraft:armor_stand" {
			p.sendEntityEvent(e.id, jevArmourStandHit)
		} else if e.living() {
			p.sendHurt(e.id)
		}
	case packet.ActorEventDeath:
		if !e.living() || e.dead {
			return
		}
		e.dead, e.diedPos, e.diedAt = true, e.pos, time.Now()
		// Health 0 lays it down and turns it red; the event plays the death sound.
		if !e.hasHealth {
			e.health, e.hasHealth = 0, true
		}
		p.sendEntityData(e, false)
		p.sendEntityEvent(e.id, jevDeath)
	default:
		if ev, ok := actorEvents[pk.EventType]; ok {
			p.sendEntityEvent(e.id, ev)
		}
	}
}

// sendHurt plays the hurt animation and sound of an entity (damage_event).
func (p *Player) sendHurt(id int32) {
	dt := p.ver().RegistryID("minecraft:damage_type", "minecraft:generic")
	if dt < 0 {
		return
	}
	w := p.pk()
	w.VarInt(id)
	w.VarInt(dt)
	w.VarInt(0) // no cause entity
	w.VarInt(0) // no direct entity
	w.Bool(false)
	p.send(v777.ClientboundPlayDamageEvent, w)
}

// sendSwing swings an entity's arm (26.3 swing_animation; 26.2 animate 0 or 3).
func (p *Player) sendSwing(id int32, offHand bool) {
	if !p.ver().Native() {
		w := p.pk()
		w.VarInt(id)
		if offHand {
			w.Byte(3)
		} else {
			w.Byte(0)
		}
		p.send(v777.ClientboundPlayAnimate, w)
		return
	}
	w := p.pk()
	w.VarInt(id)
	if offHand {
		w.VarInt(1)
	} else {
		w.VarInt(0)
	}
	w.VarInt(1) // SwingAnimationType.WHACK
	w.VarInt(6) // ticks
	p.send(v777.ClientboundPlaySwingAnimation, w)
}

// sendAnimate sends animate (26.3: 0 wake up, 1 critical hit, 2 magic critical hit; 26.2 numbers
// them 2, 4, 5).
func (p *Player) sendAnimate(id int32, action byte) {
	if !p.ver().Native() {
		action = [...]byte{2, 4, 5}[action]
	}
	w := p.pk()
	w.VarInt(id)
	w.Byte(action)
	p.send(v777.ClientboundPlayAnimate, w)
}

// mobEffect shows the player's own effects.
func (p *Player) mobEffect(pk *packet.MobEffect) {
	if pk.EntityRuntimeID != p.rem.gd.EntityRuntimeID {
		return // others' effects show as particles from their data (entities_props.go)
	}
	p.log.Debug("server effect", "op", pk.Operation, "type", pk.EffectType, "java", bedrockEffects[pk.EffectType], "amplifier", pk.Amplifier, "duration", pk.Duration, "particles", pk.Particles, "ambient", pk.Ambient)
	switch pk.Operation {
	case packet.MobEffectAdd, packet.MobEffectModify:
		ef := selfEffect{amplifier: pk.Amplifier, duration: pk.Duration, particles: pk.Particles, ambient: pk.Ambient, at: time.Now()}
		p.ents.effects[pk.EffectType] = ef
		p.sendEffect(pk.EffectType, ef)
	case packet.MobEffectRemove:
		delete(p.ents.effects, pk.EffectType)
		id := p.effectID(pk.EffectType)
		if id < 0 {
			return
		}
		w := p.pk()
		w.VarInt(SelfEntityID)
		w.VarInt(id)
		p.send(v777.ClientboundPlayRemoveMobEffect, w)
	}
}

// effectID is the client's mob_effect id of a Bedrock effect (-1: none).
func (p *Player) effectID(bedrock int32) int32 {
	name, ok := bedrockEffects[bedrock]
	if !ok {
		return -1
	}
	return p.ver().BuiltinID("minecraft:mob_effect", "minecraft:"+name)
}

// sendEffect sends update_mob_effect for one of the player's effects (its time left).
func (p *Player) sendEffect(bedrock int32, ef selfEffect) {
	id := p.effectID(bedrock)
	if id < 0 {
		return
	}
	dur := ef.duration
	if dur > 0 {
		dur -= int32(time.Since(ef.at) / (time.Second / 20))
		if dur <= 0 {
			return
		}
	}
	flags := byte(0x04 | 0x08) // icon, blend
	if ef.ambient {
		flags |= 0x01
	}
	if ef.particles {
		flags |= 0x02
	}
	w := p.pk()
	w.VarInt(SelfEntityID)
	w.VarInt(id)
	w.VarInt(ef.amplifier)
	w.VarInt(dur)
	w.Byte(flags)
	p.send(v777.ClientboundPlayUpdateMobEffect, w)
}

// applyLink applies an entity link: someone getting on or off a vehicle.
func (p *Player) applyLink(l protocol.EntityLink) {
	self := p.rem.gd.EntityUniqueID
	vehicle := p.entityByUnique(l.RiddenEntityUniqueID)
	if l.RiderEntityUniqueID == self {
		if l.Type == protocol.EntityLinkRemove {
			p.ents.ride = rideState{}
		} else if vehicle != nil {
			p.ents.ride = rideState{uniq: l.RiddenEntityUniqueID, rid: vehicle.rid, boat: vehicle.t.kind&entBoat != 0}
		}
		p.log.Debug("riding", "vehicle", l.RiddenEntityUniqueID, "type", l.Type)
	} else if rider := p.entityByUnique(l.RiderEntityUniqueID); rider != nil {
		if l.Type == protocol.EntityLinkRemove {
			rider.vehicle = 0
		} else {
			rider.vehicle = l.RiddenEntityUniqueID
		}
	}
	if vehicle == nil {
		return
	}
	vehicle.riders = removeUnique(vehicle.riders, l.RiderEntityUniqueID)
	if l.Type != protocol.EntityLinkRemove {
		vehicle.riders = append(vehicle.riders, l.RiderEntityUniqueID)
	}
	if vehicle.spawned {
		p.sendPassengers(vehicle)
	}
}

// sendPassengers sends a vehicle's riders the client has.
func (p *Player) sendPassengers(v *entity) {
	w := p.pk()
	w.VarInt(v.id)
	n := 0
	for _, u := range v.riders {
		if _, ok := p.uniqueJavaID(u); ok {
			n++
		}
	}
	if v.tag.display != 0 {
		n++ // a player's name tag display rides it too (entities_nametags.go)
	}
	n += len(v.parts)
	w.VarInt(int32(n))
	for _, u := range v.riders {
		if id, ok := p.uniqueJavaID(u); ok {
			w.VarInt(id)
		}
	}
	if v.tag.display != 0 {
		w.VarInt(v.tag.display)
	}
	for _, id := range v.parts {
		w.VarInt(id)
	}
	p.send(v777.ClientboundPlaySetPassengers, w)
}

// entityHeldItem, when set, gives the hotbar slot and item the player holds, for transactions
// (milestone 3 knows the inventory). Without it the transactions name slot 0 and no item.
var entityHeldItem func(p *Player) (slot int32, it protocol.ItemInstance)

// useOnEntity attacks or uses the held item on an entity, as a Bedrock client does.
func (p *Player) useOnEntity(e *entity, action int32, click mgl32.Vec3) {
	var slot int32
	var held protocol.ItemInstance
	if entityHeldItem != nil {
		slot, held = entityHeldItem(p)
	}
	eye := p.mv.pos.Add(mgl64.Vec3{0, eyeHeight, 0})
	p.Write(&packet.InventoryTransaction{TransactionData: &protocol.UseItemOnEntityTransactionData{
		TargetEntityRuntimeID: e.rid,
		ActionType:            action,
		HotBarSlot:            slot,
		HeldItem:              held,
		Position:              vec32(eye),
		ClickedPosition:       click,
	}})
}
