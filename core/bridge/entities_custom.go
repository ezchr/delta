package bridge

import (
	"delta/core/internal/javapack"

	jitem "github.com/ezchr/go-mcjava/item"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Custom entities (actor types Java has no entity for) that the converted resource pack has a
// model for (packs.go, internal/javapack) are shown as an item_display carrying that item model,
// with an interaction entity riding it as the hitbox (attacks and uses on it reach the actor:
// p.ents.byJava has the hitbox too). The display is the actor's Java entity, so moves, removal,
// names and riders go through the normal entity code; the display transformation puts the model
// where the Bedrock model is and follows the actor's scale. A model with animations is shown part
// by part instead while the player is near (entities_anim.go). The display's
// pitch is kept at 0 (Bedrock sends the head's pitch; a display would tilt the whole model).
//
// This file's handlers run before entities_spawn/_move/_meta's (file order), so they can change
// the packets those see.

// Display and interaction entity data indices (26.3).
const (
	displayPosInterp   = 10 // int: position/rotation interpolation duration
	displayTranslation = 11 // vector3
	displayScaleIdx    = 12 // vector3
	displayItemIdx     = 23 // item stack
	interactionWidth   = 8  // float
	interactionHeight  = 9  // float
	interactionRespond = 10 // bool
	serVector3Data     = 39
)

// modelEntity is a shown custom entity (p.packs.ents by runtime id).
type modelEntity struct {
	m      javapack.Entity
	hitbox int32      // the interaction entity's Java id
	scale  float32    // Bedrock scale metadata
	w, h   float32    // Bedrock bounding box
	script float32    // the entity file's scripts.scale (1: none)
	anim   *modelAnim // nil: the model has no scripts (entities_anim.go)
	// tintUntil is the tick (packState.fxTicks) the hurt flash ends at (0: not hurt); nameTag the
	// Java id of the text display that shows its name while the player looks at it (0: none)
	// (entities_modelfx.go).
	tintUntil uint64
	nameTag   int32
}

func init() {
	OnBedrock(packet.IDAddActor, (*Player).addModelActor)
	OnBedrock(packet.IDMoveActorAbsolute, func(p *Player, pk *packet.MoveActorAbsolute) {
		if me, ok := p.packs.ents[pk.EntityRuntimeID]; ok {
			me.setPitch(pk)
			pk.Rotation[0] = 0
		}
	})
	OnBedrock(packet.IDMoveActorDelta, func(p *Player, pk *packet.MoveActorDelta) {
		if me, ok := p.packs.ents[pk.EntityRuntimeID]; ok {
			me.setPitch(pk)
			if _, has := pk.RotationX.Value(); has {
				pk.RotationX = protocol.Option(float32(0))
			}
		}
	})
	OnBedrock(packet.IDSetActorData, func(p *Player, pk *packet.SetActorData) {
		me, ok := p.packs.ents[pk.EntityRuntimeID]
		if !ok {
			return
		}
		e := p.ents.shown[pk.EntityRuntimeID]
		if e == nil {
			return
		}
		scale, w, h := me.scale, me.w, me.h
		readModelMeta(pk.EntityMetadata, &scale, &w, &h)
		resize := scale != me.scale || w != me.w || h != me.h
		if scale != me.scale {
			me.scale = scale
			p.sendModelData(e, me, false)
		}
		if resize {
			me.w, me.h = w, h
			p.sendHitboxData(me)
		}
	})
	OnBedrock(packet.IDRemoveActor, func(p *Player, pk *packet.RemoveActor) {
		if rid, ok := p.ents.byUnique[pk.EntityUniqueID]; ok {
			p.dropModelEntity(rid)
		}
	})
	OnReset(func(p *Player, newConn bool) { clear(p.packs.ents) })
}

func readModelMeta(m protocol.EntityMetadata, scale, w, h *float32) {
	if v, ok := m[protocol.EntityDataKeyScale].(float32); ok && v > 0 {
		*scale = v
	}
	if v, ok := m[protocol.EntityDataKeyWidth].(float32); ok && v > 0 {
		*w = v
	}
	if v, ok := m[protocol.EntityDataKeyHeight].(float32); ok && v > 0 {
		*h = v
	}
}

// addModelActor shows a custom actor that has a model. Actor types Java has are left to
// entities_spawn.go.
func (p *Player) addModelActor(pk *packet.AddActor) {
	if _, ok := entityTypes[pk.EntityType]; ok {
		return
	}
	jp := p.packFor()
	if jp == nil {
		return
	}
	m, ok := jp.res.Entities[pk.EntityType]
	if !ok {
		return
	}
	p.dropModelEntity(pk.EntityRuntimeID) // the same runtime id again: its old hitbox goes
	meta := pk.EntityMetadata
	if meta == nil {
		meta = protocol.EntityMetadata{}
	}
	t := entityType{java: "minecraft:item_display"}
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.EntityUniqueID, bedrock: pk.EntityType, t: t, java: t.java,
		uuid: entityUUID(pk.EntityRuntimeID), meta: meta}
	e.flags, _ = meta[protocol.EntityDataKeyFlags].(int64)
	e.flags2, _ = meta[protocol.EntityDataKeyFlagsTwo].(int64)
	for _, a := range pk.Attributes {
		if a.Name == "minecraft:health" {
			e.health, e.hasHealth = a.Value, true
		}
	}
	e.pos = vec64(pk.Position)
	e.vel = vec64(pk.Velocity)
	e.yaw, e.head = pk.Yaw, pk.Yaw
	if pk.BodyYaw != 0 {
		e.yaw = pk.BodyYaw
	}
	if !p.trackEntity(e) {
		return
	}
	me := &modelEntity{m: m, scale: 1, script: 1, w: 0.6, h: 1.8}
	me.anim = p.newModelAnim(e, m.Anim)
	p.packs.animRankNow = p.packs.animRankNow || me.anim != nil
	readModelMeta(meta, &me.scale, &me.w, &me.h)
	if p.packs.ents == nil {
		p.packs.ents = map[uint64]*modelEntity{}
	}
	p.packs.ents[e.rid] = me
	p.showEntity(e)
	p.sendModelData(e, me, true)
	if typ := p.ver().BuiltinID("minecraft:entity_type", "minecraft:interaction"); typ >= 0 {
		me.hitbox = p.ents.next
		p.ents.next++
		w := p.pk()
		w.VarInt(me.hitbox)
		w.UUID(entityUUID(uint64(me.hitbox) | 1<<61))
		w.VarInt(typ)
		w.Float64(e.pos[0])
		w.Float64(e.pos[1])
		w.Float64(e.pos[2])
		w.LpVec3(0, 0, 0)
		w.Angle(0)
		w.Angle(0)
		w.Angle(0)
		w.VarInt(0)
		p.send(v777.ClientboundPlayAddEntity, w)
		p.sendHitboxData(me)
		p.ents.byJava[me.hitbox] = e
		e.tag.display = me.hitbox // rides the display (sendPassengers sends it with the riders)
		p.sendPassengers(e)
	}
	for _, l := range pk.EntityLinks {
		p.applyLink(l)
	}
}

// sendModelData sends the display's item and transformation (full: also the item and the
// interpolation duration).
func (p *Player) sendModelData(e *entity, me *modelEntity, full bool) {
	w := p.pk()
	w.VarInt(e.id)
	if full {
		w.Byte(displayPosInterp)
		w.VarInt(serInt)
		w.VarInt(3)
		w.Byte(displayItemIdx)
		w.VarInt(serItemStack)
		var js jitem.Stack
		js.Reset()
		js.ID, js.Count = javaPaper, 1
		if me.anim != nil && me.anim.parts != nil {
			js.Count = 0 // its parts are displays of their own
		}
		js.ItemModel = me.m.Model
		js.Add(jitem.CompItemModel)
		if me.tintUntil != 0 {
			tintStack(&js, me)
			js.Add(jitem.CompCustomModelData)
		}
		js.EncodeFor(w, p.win.proto())
	}
	s := float32(me.m.Scale) * me.scale * me.script
	w.Byte(displayTranslation)
	w.VarInt(serVector3Data)
	for _, v := range me.m.Translation {
		w.Float32(float32(v) * me.scale * me.script)
	}
	w.Byte(displayScaleIdx)
	w.VarInt(serVector3Data)
	w.Float32(s)
	w.Float32(s)
	w.Float32(s)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// box is the mob's collision box: the width and height of its actor data times its scale. The
// server sends the box of the entity file as it is (a Decidueye's 0.9 x 3) and the scale next
// to it (0.65), and a Bedrock client and the server size the box by the scale: asked with a
// selector volume, the server has that Decidueye's box end 1.95 above its feet, not 3.
func (me *modelEntity) box() (w, h float32) {
	s := me.scale
	if s <= 0 {
		s = 1
	}
	return me.w * s, me.h * s
}

// sendHitboxData sizes the interaction entity as the Bedrock bounding box.
func (p *Player) sendHitboxData(me *modelEntity) {
	if me.hitbox == 0 {
		return
	}
	bw, bh := me.box()
	w := p.pk()
	w.VarInt(me.hitbox)
	w.Byte(interactionWidth)
	w.VarInt(serFloat)
	w.Float32(bw)
	w.Byte(interactionHeight)
	w.VarInt(serFloat)
	w.Float32(bh)
	w.Byte(interactionRespond)
	w.VarInt(serBool)
	w.Bool(true)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// dropModelEntity removes a custom entity's hitbox (the display goes with the actor).
func (p *Player) dropModelEntity(rid uint64) {
	me, ok := p.packs.ents[rid]
	if !ok {
		return
	}
	delete(p.packs.ents, rid)
	p.dropAnimParts(p.ents.shown[rid], me)
	p.hideAimName(me)
	if me.hitbox == 0 {
		return
	}
	if e := p.ents.shown[rid]; e != nil && e.tag.display == me.hitbox {
		e.tag.display = 0
	}
	delete(p.ents.byJava, me.hitbox)
	w := p.pk()
	w.VarInt(1)
	w.VarInt(me.hitbox)
	p.send(v777.ClientboundPlayRemoveEntities, w)
}

// setPitch keeps the head pitch of a move for the animation (the display stays level).
func (me *modelEntity) setPitch(pk any) {
	if me.anim == nil {
		return
	}
	switch pk := pk.(type) {
	case *packet.MoveActorAbsolute:
		me.anim.pitch = pk.Rotation[0]
	case *packet.MoveActorDelta:
		if v, ok := pk.RotationX.Value(); ok {
			me.anim.pitch = v
		}
	}
}
