package bridge

import (
	"math"
	"strings"

	"delta/core/internal/anim"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// What a Java client does by itself for a living mob and cannot do for a custom mob drawn with
// item displays: the red flash when it is hurt, and its name when the player looks at it.
//
// The flash: every entity model's faces have tintindex 0 and its item definition a tint from
// the item's custom model data (first colour, white when there is none), so the bridge can
// colour a mob by sending its displays their item again with a colour. On ActorEvent Hurt it
// sends hurtTint, and the plain item again hurtTicks ticks later.
//
// The name: a display entity draws no name tag, and the client's "name of the mob under the
// crosshair" is for living entities only. The bridge works out which custom mob the player
// looks at (a ray from the eyes along the view, against the mobs' boxes, up to aimNameRange
// blocks) and shows that mob's name on a text display riding its hitbox, which puts it above
// the box (the box at the mob's scale: modelEntity.box) and moves it with the mob; it goes when
// the player looks away. A name the server always shows is on such a
// display all the time (the mob's own display has the name in its entity data as well, which
// the client does not draw for a display entity).
const (
	hurtTint     = 0xFF5A5A // what the item's colours are multiplied with while hurt
	aimNameRange = 8.0      // blocks: how far the name of a mob looked at shows
	aimNameTicks = 2        // how often the crosshair is tested

	textDisplayText  = 23 // text component
	displayBillboard = 15 // byte: 3 = faces the camera
)

// hurtFlashTicks is how long a hurt mob stays red ([core.options] hurt_flash_ticks; half a
// second as on Java unless set, and longer to look at it at leisure).
var hurtFlashTicks uint64 = hurtTicks

// tintStack colours a model's item while its mob is hurt.
func tintStack(js *jitem.Stack, me *modelEntity) {
	if me.tintUntil == 0 {
		return
	}
	js.CustomModelData.Colors = append(js.CustomModelData.Colors[:0], hurtTint)
}

// partStack is the item of a part display.
func (p *Player) writePartItem(w *wire.Writer, mp anim.Part, me *modelEntity) {
	w.Byte(displayItemIdx)
	w.VarInt(serItemStack)
	var js jitem.Stack
	js.Reset()
	js.ID, js.Count = javaPaper, 1
	js.ItemModel = mp.Model
	js.Add(jitem.CompItemModel)
	js.CustomModelData.Floats = append(js.CustomModelData.Floats[:0], float32(mp.Index))
	tintStack(&js, me)
	js.Add(jitem.CompCustomModelData)
	js.EncodeFor(w, p.win.proto())
}

// sendModelTint sends a mob's displays their item again, with or without the hurt colour.
func (p *Player) sendModelTint(e *entity, me *modelEntity) {
	if !e.spawned {
		return
	}
	if me.anim == nil || me.anim.parts == nil {
		p.sendModelData(e, me, true)
		return
	}
	p.animBundle(true)
	for i, mp := range me.anim.set {
		w := p.pk()
		w.VarInt(me.anim.parts[i].id)
		p.writePartItem(w, mp, me)
		w.Byte(0xff)
		p.send(v777.ClientboundPlaySetEntityData, w)
	}
	p.animBundle(false)
}

// modelFxTick ends hurt flashes and follows the crosshair.
func (p *Player) modelFxTick() {
	ps := &p.packs
	if len(ps.ents) == 0 {
		ps.aimed = 0
		return
	}
	ps.fxTicks++
	for rid, me := range ps.ents {
		if me.tintUntil != 0 && ps.fxTicks >= me.tintUntil {
			me.tintUntil = 0
			if e := p.ents.shown[rid]; e != nil {
				p.sendModelTint(e, me)
			}
		}
	}
	if ps.fxTicks%aimNameTicks != 0 {
		return
	}
	// A name shows for the mob under the crosshair, and for every mob whose name the server
	// always shows.
	ps.aimed = p.aimedModel()
	for rid, me := range ps.ents {
		e := p.ents.shown[rid]
		if e == nil {
			continue
		}
		want := rid == ps.aimed || alwaysNamed(e)
		switch {
		case want && me.nameTag == 0:
			p.showAimName(e, me)
		case !want && me.nameTag != 0:
			p.hideAimName(me)
		}
	}
}

// alwaysNamed reports whether the server shows an actor's name whether it is looked at or not.
func alwaysNamed(e *entity) bool {
	return e.flag(protocol.EntityDataFlagAlwaysShowName) || metaInt(e.meta, protocol.EntityDataKeyAlwaysShowNameTag) != 0
}

// aimedModel is the runtime id of the custom mob under the player's crosshair (0: none).
func (p *Player) aimedModel() uint64 {
	eye := p.mv.pos.Add(mgl64.Vec3{0, eyeHeight, 0})
	sy, cy := math.Sincos(float64(p.mv.yaw) * math.Pi / 180)
	sp, cp := math.Sincos(float64(p.mv.pitch) * math.Pi / 180)
	dir := mgl64.Vec3{-sy * cp, -sp, cy * cp}
	best, bt := uint64(0), aimNameRange
	for rid, me := range p.packs.ents {
		e := p.ents.shown[rid]
		if e == nil || !e.spawned || me.hitbox == 0 {
			continue
		}
		bw, bh := me.box()
		w, h := float64(bw)/2, float64(bh)
		lo := mgl64.Vec3{e.pos[0] - w, e.pos[1], e.pos[2] - w}
		hi := mgl64.Vec3{e.pos[0] + w, e.pos[1] + h, e.pos[2] + w}
		// The ray against the box, axis by axis.
		t0, t1 := 0.0, bt
		for a := 0; a < 3 && t0 <= t1; a++ {
			if math.Abs(dir[a]) < 1e-9 {
				if eye[a] < lo[a] || eye[a] > hi[a] {
					t0 = math.Inf(1)
				}
				continue
			}
			ta, tb := (lo[a]-eye[a])/dir[a], (hi[a]-eye[a])/dir[a]
			t0, t1 = max(t0, min(ta, tb)), min(t1, max(ta, tb))
		}
		if t0 <= t1 && t0 < bt {
			best, bt = rid, t0
		}
	}
	return best
}

// showAimName puts a mob's name above it.
func (p *Player) showAimName(e *entity, me *modelEntity) {
	if e == nil || me.hitbox == 0 || me.nameTag != 0 {
		return
	}
	name, _, _ := strings.Cut(shownName(e, e.meta), "\n")
	typ := p.ver().BuiltinID("minecraft:entity_type", "minecraft:text_display")
	if name == "" || typ < 0 {
		return
	}
	me.nameTag = p.ents.next
	p.ents.next++
	p.animBundle(true)
	w := p.pk()
	w.VarInt(me.nameTag)
	w.UUID(entityUUID(uint64(me.nameTag) | 1<<60))
	w.VarInt(typ)
	w.Float64(e.pos[0])
	_, bh := me.box()
	w.Float64(e.pos[1] + float64(bh))
	w.Float64(e.pos[2])
	w.LpVec3(0, 0, 0)
	w.Angle(0)
	w.Angle(0)
	w.Angle(0)
	w.VarInt(0)
	p.send(v777.ClientboundPlayAddEntity, w)
	w = p.pk()
	w.VarInt(me.nameTag)
	w.Byte(displayTranslation)
	w.VarInt(serVector3Data)
	w.Float32(0)
	w.Float32(0.3)
	w.Float32(0)
	w.Byte(displayBillboard)
	w.VarInt(serByte)
	w.Byte(3)
	w.Byte(textDisplayText)
	w.VarInt(serComponent)
	c := text.Legacy(name, text.Bedrock)
	c.Write(w)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
	w = p.pk() // it rides the hitbox: on top of the mob's box, and it moves with the mob
	w.VarInt(me.hitbox)
	w.VarInt(1)
	w.VarInt(me.nameTag)
	p.send(v777.ClientboundPlaySetPassengers, w)
	p.animBundle(false)
}

// hideAimName takes a mob's name away again.
func (p *Player) hideAimName(me *modelEntity) {
	if me.nameTag == 0 {
		return
	}
	w := p.pk()
	w.VarInt(1)
	w.VarInt(me.nameTag)
	p.send(v777.ClientboundPlayRemoveEntities, w)
	me.nameTag = 0
}

func init() {
	OnTick((*Player).modelFxTick)
	OnBedrock(packet.IDLevelSoundEvent, func(p *Player, pk *packet.LevelSoundEvent) {
		if debugAnim && pk.EntityType != "" && !strings.HasPrefix(pk.EntityType, "minecraft:") {
			p.log.Info("anim level sound", "type", pk.EntityType, "sound", pk.SoundType, "uniq", pk.EntityUniqueID)
		}
	})
	OnBedrock(packet.IDActorEvent, func(p *Player, pk *packet.ActorEvent) {
		me := p.packs.ents[pk.EntityRuntimeID]
		e := p.ents.shown[pk.EntityRuntimeID]
		if me != nil && debugAnim {
			p.log.Info("anim actor event", "rid", pk.EntityRuntimeID, "event", pk.EventType, "data", pk.EventData)
		}
		if me == nil || e == nil {
			return
		}
		// BDS sends no hurt or death level sound for a custom mob without sounds of its own
		// (seen: a hit Charizard gives ActorEvent 2 and nothing else), so the generic Java
		// sounds are played from the events.
		switch pk.EventType {
		case packet.ActorEventHurt:
			p.sendSound("entity.generic.hurt", entitySource(""), e.pos, 1, 1)
		case packet.ActorEventDeath:
			p.sendSound("entity.generic.death", entitySource(""), e.pos, 1, 1)
			return
		default:
			return
		}
		me.tintUntil = p.packs.fxTicks + hurtFlashTicks
		p.sendModelTint(e, me)
	})
}
