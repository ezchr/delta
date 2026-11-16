package bridge

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Spawning and removing the server's actors on the Java client.

// entity is one actor the Java client was shown (or, for a player, is about to be: see
// entities_players.go). Everything is guarded by p.mu.
type entity struct {
	rid     uint64
	uniq    int64
	id      int32 // Java entity id
	bedrock string
	t       entityType
	java    string // Java type name (boats: by wood)
	uuid    uuid.UUID
	data    int32 // add_entity data (26.3 ids)

	pos, vel         mgl64.Vec3 // Java position (feet), velocity in blocks per tick
	yaw, pitch, head float32
	onGround         bool
	// The client's view of the position (its 26.3 delta codec base), the angle bytes it has and
	// the moves since the last full sync (move.go).
	base                         mgl64.Vec3
	sentYaw, sentPitch, sentHead byte
	sinceSync                    int

	meta          protocol.EntityMetadata // all metadata the server sent, merged
	flags, flags2 int64
	health        float32
	hasHealth     bool
	dead          bool // the server said it died (Java keeps it lying down until removed)
	diedPos       mgl64.Vec3
	diedAt        time.Time

	equip    [7]protocol.ItemStack // main hand, off hand, feet, legs, chest, head, body
	hasEquip bool
	item     protocol.ItemStack // item entities

	riders  []int64 // unique ids of the riders, in seat order
	vehicle int64   // unique id of what it rides (0: none)
	leash   int64   // unique id of what holds its lead (0: none; entities_leash.go)
	// leashSent: the client may have a lead on it (the last set_entity_link named a holder).
	leashSent bool

	painting string
	frameNBT map[string]any // item frames: the block entity
	// saddle is the saddle shown (-1: not sent yet).
	saddle int8

	// data0 is the entity data last sent, dataStart/dataEnd where each index is in it.
	data0              []byte
	dataStart, dataEnd [32]uint16

	// Players: spawned once their tab entry (and skin) is on the client.
	spawned bool
	ownTab  bool // the tab entry was made for the entity (no player list entry): removed with it

	// props are the actor property values by name (entities_props.go).
	props map[string]any
	// sentScale is the scale attribute the client has (0: none sent since the spawn).
	sentScale float64
	// attrSpeed, attrJump: the server's movement and horse jump strength (rideable mobs).
	attrSpeed, attrJump float32
	// tag is how a player's name tag is shown (entities_nametags.go).
	tag nameTag
}

// flag reports a Bedrock entity flag.
func (e *entity) flag(i int) bool {
	if i >= 64 {
		return e.flags2&(1<<(i-64)) != 0
	}
	return e.flags&(1<<i) != 0
}

func (e *entity) living() bool { return e.t.kind&entLiving != 0 }

// loggedTypes are the actor types already logged as having no Java equivalent.
var loggedTypes sync.Map

func init() {
	OnBedrock(packet.IDAddActor, (*Player).addActor)
	OnBedrock(packet.IDAddItemActor, (*Player).addItemActor)
	OnBedrock(packet.IDAddPainting, (*Player).addPainting)
	OnBedrock(packet.IDRemoveActor, func(p *Player, pk *packet.RemoveActor) {
		if rid, ok := p.ents.byUnique[pk.EntityUniqueID]; ok {
			p.removeEntity(rid, true)
		}
	})
}

// entityUUID is the UUID of a non-player entity: unique per runtime id (the Java client refuses an
// entity whose UUID it already has).
func entityUUID(rid uint64) uuid.UUID {
	var u uuid.UUID
	copy(u[:], "bb2e")
	binary.BigEndian.PutUint64(u[8:], rid)
	u[6] = u[6]&0x0f | 0x40
	return u
}

// addActor shows an AddActor.
func (p *Player) addActor(pk *packet.AddActor) {
	t, ok := entityTypes[pk.EntityType]
	if !ok && p.addTextActor(pk) {
		return
	}
	if !ok || t.java == "" {
		if _, seen := loggedTypes.LoadOrStore(pk.EntityType, true); !seen {
			p.log.Debug("no Java entity for this actor type; not shown", "type", pk.EntityType)
		}
		return
	}
	meta := pk.EntityMetadata
	if meta == nil {
		meta = protocol.EntityMetadata{}
	}
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.EntityUniqueID, bedrock: pk.EntityType, t: t, java: t.java,
		uuid: entityUUID(pk.EntityRuntimeID), meta: meta}
	e.flags, _ = meta[protocol.EntityDataKeyFlags].(int64)
	e.flags2, _ = meta[protocol.EntityDataKeyFlagsTwo].(int64)
	switch pk.EntityType {
	case "minecraft:boat", "minecraft:chest_boat":
		list := boatTypes[:]
		if pk.EntityType == "minecraft:chest_boat" {
			list = chestBoatTypes[:]
		}
		v := metaInt(meta, protocol.EntityDataKeyVariant)
		if v < 0 || int(v) >= len(list) {
			v = 0
		}
		e.java = list[v]
	case "minecraft:falling_block":
		e.data = p.blockState(uint32(metaInt(meta, protocol.EntityDataKeyVariant)))
	case "minecraft:fishing_hook":
		// The client drops a bobber whose owner it does not know.
		owner, _ := meta[protocol.EntityDataKeyOwner].(int64)
		id, ok := p.uniqueJavaID(owner)
		if !ok {
			return
		}
		e.data = id
	}
	for _, a := range pk.Attributes {
		switch a.Name {
		case "minecraft:health":
			e.health, e.hasHealth = a.Value, true
		case "minecraft:movement":
			e.attrSpeed = a.Value
		case "minecraft:horse.jump_strength":
			e.attrJump = a.Value
		}
	}
	p.applyProps(e, pk.EntityProperties)
	e.pos = vec64(pk.Position).Sub(mgl64.Vec3{0, t.yOff, 0})
	e.vel = vec64(pk.Velocity)
	e.yaw, e.pitch, e.head = pk.Yaw, pk.Pitch, pk.HeadYaw
	if t.kind&entBoat != 0 {
		e.yaw -= 90
		e.head = e.yaw
	}
	if !p.trackEntity(e) {
		return
	}
	if debugPackets {
		p.log.Debug("add actor", "type", pk.EntityType, "rid", e.rid, "java", e.id, "flags", e.flags, "flags2", e.flags2, "meta", len(meta))
	}
	p.showEntity(e)
	for _, l := range pk.EntityLinks {
		p.applyLink(l)
	}
}

// addItemActor shows a dropped item.
func (p *Player) addItemActor(pk *packet.AddItemActor) {
	t := entityTypes["minecraft:item"]
	meta := pk.EntityMetadata
	if meta == nil {
		meta = protocol.EntityMetadata{}
	}
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.EntityUniqueID, bedrock: "minecraft:item", t: t, java: t.java,
		uuid: entityUUID(pk.EntityRuntimeID), meta: meta, item: pk.Item.Stack}
	e.flags, _ = meta[protocol.EntityDataKeyFlags].(int64)
	e.flags2, _ = meta[protocol.EntityDataKeyFlagsTwo].(int64)
	e.pos = vec64(pk.Position).Sub(mgl64.Vec3{0, t.yOff, 0})
	e.vel = vec64(pk.Velocity)
	if !p.trackEntity(e) {
		return
	}
	p.showEntity(e)
}

// addPainting shows a painting: Bedrock sends its centre, Java the block it hangs in.
func (p *Player) addPainting(pk *packet.AddPainting) {
	name := paintingName(pk.Title)
	dir := pk.Direction & 3
	t := entityTypes["minecraft:painting"]
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.EntityUniqueID, bedrock: "minecraft:painting", t: t, java: t.java,
		uuid: entityUUID(pk.EntityRuntimeID), meta: protocol.EntityMetadata{}, painting: name,
		data: paintingDirections[dir]}
	size, ok := paintingSizes[name]
	if !ok {
		size = [2]int{1, 1}
	}
	c := vec64(pk.Position)
	ccw := [4][2]float64{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}[dir]
	if size[0]%2 == 0 {
		c[0] -= ccw[0] * 0.5
		c[2] -= ccw[1] * 0.5
	}
	if size[1]%2 == 0 {
		c[1] -= 0.5
	}
	e.pos = mgl64.Vec3{math.Floor(c[0]) + 0.5, math.Floor(c[1]) + 0.5, math.Floor(c[2]) + 0.5}
	if !p.trackEntity(e) {
		return
	}
	p.showEntity(e)
}

// trackEntity gives e its Java id and remembers it, replacing an entity with the same runtime id.
// It reports false for a type the client's version does not have.
func (p *Player) trackEntity(e *entity) bool {
	if p.ver().BuiltinID("minecraft:entity_type", e.java) < 0 {
		if _, seen := loggedTypes.LoadOrStore(e.java, true); !seen {
			p.log.Info("the client's version has no such entity type; not shown", "type", e.java)
		}
		return false
	}
	ents := p.ents
	if old, ok := ents.shown[e.rid]; ok {
		p.removeEntity(old.rid, false)
	}
	e.id = ents.JavaID(e.rid)
	if e.id == SelfEntityID {
		return false
	}
	if e.t.kind&entDragon != 0 {
		ents.reserve(8)
	}
	ents.shown[e.rid] = e
	ents.byJava[e.id] = e
	ents.SetUnique(e.uniq, e.rid)
	return true
}

// showEntity sends a tracked entity to the client: add_entity, its data, equipment, riders and
// leash.
func (p *Player) showEntity(e *entity) {
	typ := p.ver().BuiltinID("minecraft:entity_type", e.java)
	if typ < 0 {
		return
	}
	data := e.data
	if e.bedrock == "minecraft:falling_block" {
		data = p.ver().BlockState(data)
	}
	w := p.pk()
	w.VarInt(e.id)
	w.UUID(e.uuid)
	w.VarInt(typ)
	w.Float64(e.pos[0])
	w.Float64(e.pos[1])
	w.Float64(e.pos[2])
	w.LpVec3(e.vel[0], e.vel[1], e.vel[2])
	w.Angle(e.pitch)
	w.Angle(e.yaw)
	w.Angle(e.head)
	w.VarInt(data)
	p.send(v777.ClientboundPlayAddEntity, w)
	e.base, e.sinceSync = e.pos, 0
	e.sentYaw, e.sentPitch, e.sentHead = angleByte(e.yaw), angleByte(e.pitch), angleByte(e.head)
	e.spawned = true
	e.saddle = -1

	p.sendEntityData(e, true)
	e.sentScale = 0
	p.sendScale(e)
	if e.attrSpeed > 0 && saddleable[e.bedrock] {
		p.sendMountAttributes(e)
	}
	if e.hasEquip || heldByProperty(e) {
		p.sendEquipment(e)
	}
	if e.t.kind&entProjectile != 0 && e.vel != (mgl64.Vec3{}) {
		p.sendMotion(e.id, e.vel)
	}
	// Riders the client already has (a player's name tag display too), and the vehicle it
	// rides.
	if len(e.riders) > 0 || e.tag.display != 0 {
		p.sendPassengers(e)
	}
	if e.t.kind&entPlayer != 0 {
		p.updateNameTag(e)
	}
	if e.vehicle != 0 {
		if v := p.entityByUnique(e.vehicle); v != nil {
			p.sendPassengers(v)
		}
	}
	// Its lead (a new entity on the client has none), and the leads of the mobs it holds.
	e.leash, e.leashSent = leashHolder(e.meta), false
	p.sendLeash(e)
	p.relinkLeashed(e)
}

// removeEntity removes an actor from the client. poof: the server removed it (a dead mob turns
// into smoke as on Java).
func (p *Player) removeEntity(rid uint64, poof bool) {
	e, ok := p.ents.shown[rid]
	if !ok {
		return
	}
	if e.spawned {
		if poof && e.dead && e.t.kind&entMob != 0 {
			p.sendEntityEvent(e.id, jevPoof)
		}
		w := p.pk()
		w.VarInt(1)
		w.VarInt(e.id)
		p.send(v777.ClientboundPlayRemoveEntities, w)
	}
	if e.vehicle != 0 {
		if v := p.entityByUnique(e.vehicle); v != nil {
			v.riders = removeUnique(v.riders, e.uniq)
		}
	}
	p.ents.Forget(rid)
	p.relinkLeashed(e) // the mobs it held lose their lead on the client until it is back
	if _, ok := p.ents.bosses[e.uniq]; ok {
		// A Bedrock client drops a boss's bar with the boss.
		delete(p.ents.bosses, e.uniq)
		p.sendBossRemove(e.uniq)
	}
	if e.t.kind&entPlayer != 0 {
		p.dropNameTag(e)
		p.playerGone(e)
	}
}

// entityByUnique is the shown entity with this unique id.
func (p *Player) entityByUnique(uniq int64) *entity {
	if rid, ok := p.ents.byUnique[uniq]; ok {
		return p.ents.shown[rid]
	}
	return nil
}

// uniqueJavaID is the Java id of an actor by unique id (the player itself included).
func (p *Player) uniqueJavaID(uniq int64) (int32, bool) {
	if p.rem != nil && uniq == p.rem.gd.EntityUniqueID {
		return SelfEntityID, true
	}
	if e := p.entityByUnique(uniq); e != nil && e.spawned {
		return e.id, true
	}
	return 0, false
}

// blockState is the 26.3 Java state of a server block runtime id.
func (p *Player) blockState(rid uint32) int32 {
	if p.rem == nil {
		return 0
	}
	pal := p.rem.pal
	return int32(pal.Java(pal.ServerToIndex(rid), false))
}

// sendEntityEvent sends entity_event.
func (p *Player) sendEntityEvent(id int32, event byte) {
	w := p.pk()
	w.Int32(id)
	w.Byte(event)
	p.send(v777.ClientboundPlayEntityEvent, w)
}

// angleByte is an angle in degrees as the 1/256-turn byte Java uses.
func angleByte(deg float32) byte { return byte(int32(math.Floor(float64(deg) * 256 / 360))) }

// metaInt is an integer of the metadata (any integer type), 0 if absent.
func metaInt(m protocol.EntityMetadata, key uint32) int32 {
	switch v := m[key].(type) {
	case int32:
		return v
	case int64:
		return int32(v)
	case int16:
		return int32(v)
	case uint8:
		return int32(v)
	case int8:
		return int32(v)
	case uint32:
		return int32(v)
	case float32:
		return int32(v)
	}
	return 0
}

// metaFloat is a float of the metadata, 0 if absent.
func metaFloat(m protocol.EntityMetadata, key uint32) float32 {
	v, _ := m[key].(float32)
	return v
}

func removeUnique(list []int64, u int64) []int64 {
	for i, v := range list {
		if v == u {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// javaName is a Bedrock name as a Java profile name: no formatting, at most 16 characters.
func javaName(s string) string {
	var b strings.Builder
	n := 0
	skip := false
	for _, c := range s {
		if skip {
			skip = false
			continue
		}
		if c == '§' {
			skip = true
			continue
		}
		if c < ' ' || c > 0xffff || n >= 16 {
			continue // 16 UTF-16 units at most
		}
		b.WriteRune(c)
		n++
	}
	return b.String()
}
