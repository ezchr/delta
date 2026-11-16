package bridge

import (
	"bytes"
	"math"
	"strings"

	"delta/core/internal/javamap"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Entity data (set_entity_data) from the actors' Bedrock metadata, equipment (set_equipment) and
// the item stacks both need. Only the indices of the Java classes an entity type has are sent
// (entityKind): a wrong index or serializer makes the Java client drop the connection.

// Java entity data serializer ids (26.2 and 26.3 have the same).
const (
	serByte          = 0
	serInt           = 1
	serLong          = 2
	serFloat         = 3
	serOptComponent  = 6
	serItemStack     = 7
	serBool          = 8
	serRotations     = 9
	serOptBlockPos   = 11
	serDirection     = 12
	serOptBlockState = 15
	serVillagerData  = 18
	serPose          = 20
	serCatVariant    = 21
	serWolfVariant   = 25
	serFrogVariant   = 27
	serPainting      = 34
)

// Java poses.
const (
	poseStanding   = 0
	poseFallFlying = 1
	poseSleeping   = 2
	poseSwimming   = 3
	poseCrouching  = 5
	poseRoaring    = 11
	poseSniffing   = 12
	poseEmerging   = 13
	poseDigging    = 14
)

// Java equipment slots.
const (
	slotMainHand = iota
	slotOffHand
	slotFeet
	slotLegs
	slotChest
	slotHead
	slotBody
	slotSaddle
)

// javaItemHook, when set, converts a Bedrock item stack for entities (milestone 3 can point it
// at its own, fuller conversion). It reports false for an empty or unknown stack.
var javaItemHook func(p *Player, st protocol.ItemStack, js *jitem.Stack) bool

func init() {
	OnBedrock(packet.IDSetActorData, func(p *Player, pk *packet.SetActorData) {
		if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
			return // self.go
		}
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok {
			return
		}
		for k, v := range pk.EntityMetadata {
			e.meta[k] = v
		}
		e.flags, _ = e.meta[protocol.EntityDataKeyFlags].(int64)
		e.flags2, _ = e.meta[protocol.EntityDataKeyFlagsTwo].(int64)
		propsChanged := p.applyProps(e, pk.EntityProperties)
		if _, ok := pk.EntityMetadata[protocol.EntityDataKeyLeashHolder]; ok {
			p.updateLeash(e) // also before the spawn: showEntity sends what it has then
		}
		if !e.spawned {
			return
		}
		p.sendEntityData(e, false)
		p.sendScale(e)
		if propsChanged && heldByProperty(e) {
			p.sendEquipment(e)
		}
		if e.t.kind&entPlayer != 0 {
			p.updateNameTag(e)
		}
		if _, ok := pk.EntityMetadata[protocol.EntityDataKeyFlags]; ok && e.t.kind&entMob != 0 {
			p.sendSaddle(e) // saddled is a flag on Bedrock, equipment on Java
		}
	})
	OnBedrock(packet.IDUpdateAttributes, func(p *Player, pk *packet.UpdateAttributes) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok || !e.living() {
			return
		}
		for _, a := range pk.Attributes {
			if a.Name != "minecraft:health" {
				continue
			}
			if e.hasHealth && e.health == a.Value {
				continue // servers repeat attributes
			}
			e.health, e.hasHealth = a.Value, true
			if a.Value > 0 && e.dead && e.t.kind&entPlayer != 0 {
				p.revivePlayer(e)
				continue
			}
			if e.spawned && !e.dead {
				p.sendEntityData(e, false)
			}
		}
	})
	OnBedrock(packet.IDMobEquipment, func(p *Player, pk *packet.MobEquipment) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok || !e.living() {
			return
		}
		slot := slotMainHand
		if pk.WindowID == protocol.WindowIDOffHand {
			slot = slotOffHand
		}
		if e.hasEquip && sameStack(e.equip[slot], pk.NewItem.Stack) {
			return // servers repeat equipment
		}
		e.equip[slot], e.hasEquip = pk.NewItem.Stack, true
		if e.spawned {
			p.sendEquipment(e)
		}
	})
	OnBedrock(packet.IDMobArmourEquipment, func(p *Player, pk *packet.MobArmourEquipment) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok || !e.living() {
			return
		}
		if e.hasEquip && sameStack(e.equip[slotHead], pk.Helmet.Stack) && sameStack(e.equip[slotChest], pk.Chestplate.Stack) &&
			sameStack(e.equip[slotLegs], pk.Leggings.Stack) && sameStack(e.equip[slotFeet], pk.Boots.Stack) &&
			sameStack(e.equip[slotBody], pk.Body.Stack) {
			return
		}
		e.equip[slotHead] = pk.Helmet.Stack
		e.equip[slotChest] = pk.Chestplate.Stack
		e.equip[slotLegs] = pk.Leggings.Stack
		e.equip[slotFeet] = pk.Boots.Stack
		e.equip[slotBody] = pk.Body.Stack
		e.hasEquip = true
		if e.spawned {
			p.sendEquipment(e)
		}
	})
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			p.ents.itemNames = nil
		}
	})
}

// dataWriter writes entity data entries, noting where each starts (for sending only the ones
// that changed).
type dataWriter struct {
	w *wire.Writer
	t *dataTable
}

type dataTable struct {
	n     int
	index [48]byte
	start [48]uint16
}

func (d dataWriter) head(index byte, ser int32) {
	if d.t.n < len(d.t.index) {
		d.t.index[d.t.n], d.t.start[d.t.n] = index, uint16(len(d.w.B))
		d.t.n++
	}
	d.w.Byte(index)
	d.w.VarInt(ser)
}
func (d dataWriter) byteVal(index byte, v byte) { d.head(index, serByte); d.w.Byte(v) }
func (d dataWriter) intVal(index byte, v int32) { d.head(index, serInt); d.w.VarInt(v) }
func (d dataWriter) boolVal(index byte, v bool) { d.head(index, serBool); d.w.Bool(v) }
func (d dataWriter) floatVal(index byte, v float32) {
	d.head(index, serFloat)
	d.w.Float32(v)
}
func (d dataWriter) varVal(index byte, ser, v int32) { d.head(index, ser); d.w.VarInt(v) }

// sendEntityData sends e's entity data: all of it (full, a spawn), else the entries that changed
// since the last time (servers resend unchanged metadata often).
func (p *Player) sendEntityData(e *entity, full bool) {
	s := &p.ents.scratch
	s.Reset()
	var t dataTable
	p.writeEntityData(dataWriter{s, &t}, e)
	var starts, ends [32]uint16
	var changed uint64
	for i := 0; i < t.n; i++ {
		idx, st, en := t.index[i], t.start[i], uint16(len(s.B))
		if i+1 < t.n {
			en = t.start[i+1]
		}
		if idx >= 32 {
			changed |= 1 << i
			continue
		}
		starts[idx], ends[idx] = st, en
		if full || e.dataEnd[idx] == 0 || !bytes.Equal(e.data0[e.dataStart[idx]:e.dataEnd[idx]], s.B[st:en]) {
			changed |= 1 << i
		}
	}
	e.data0 = append(e.data0[:0], s.B...)
	e.dataStart, e.dataEnd = starts, ends
	if changed == 0 {
		return
	}
	w := p.pk()
	w.VarInt(e.id)
	for i := 0; i < t.n; i++ {
		if changed&(1<<i) == 0 {
			continue
		}
		en := uint16(len(s.B))
		if i+1 < t.n {
			en = t.start[i+1]
		}
		w.Raw(s.B[t.start[i]:en])
	}
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// writeEntityData writes e's entity data entries.
func (p *Player) writeEntityData(d dataWriter, e *entity) {
	m := e.meta
	bit := func(flag int, b byte) byte {
		if e.flag(flag) {
			return b
		}
		return 0
	}
	kind := e.t.kind
	if kind&entText != 0 {
		p.writeTextData(d, e)
		return
	}

	// Entity.
	d.byteVal(0, bit(protocol.EntityDataFlagOnFire, 0x01)|bit(protocol.EntityDataFlagSneaking, 0x02)|
		bit(protocol.EntityDataFlagSprinting, 0x08)|bit(protocol.EntityDataFlagSwimming, 0x10)|
		bit(protocol.EntityDataFlagInvisible, 0x20)|bit(protocol.EntityDataFlagGliding, 0x80))
	if v, ok := m[protocol.EntityDataKeyAirSupply].(int16); ok && e.living() {
		d.intVal(1, int32(v))
	}
	if kind&entPlayer == 0 {
		name := shownName(e, m) // npc.go: NPCs show their raw-text name
		d.head(2, serOptComponent)
		d.w.Bool(name != "")
		if name != "" {
			c := text.Legacy(name, text.Bedrock)
			c.Write(d.w)
		}
		always := e.flag(protocol.EntityDataFlagAlwaysShowName) || metaInt(m, protocol.EntityDataKeyAlwaysShowNameTag) != 0
		d.boolVal(3, name != "" && always)
	}
	d.boolVal(4, e.flag(protocol.EntityDataFlagSilent))
	if kind&(entProjectile) != 0 && e.flags != 0 {
		d.boolVal(5, !e.flag(protocol.EntityDataFlagHasGravity))
	}
	pose := int32(poseStanding)
	switch {
	case e.flag(protocol.EntityDataFlagSleeping):
		pose = poseSleeping
	case e.flag(protocol.EntityDataFlagGliding):
		pose = poseFallFlying
	case e.flag(protocol.EntityDataFlagSwimming) && kind&entPlayer != 0:
		pose = poseSwimming
	case e.flag(protocol.EntityDataFlagSneaking) && kind&entPlayer != 0:
		pose = poseCrouching
	case e.bedrock == "minecraft:warden" && e.flag(protocol.EntityDataFlagEmerging):
		pose = poseEmerging
	case e.bedrock == "minecraft:warden" && e.flag(protocol.EntityDataFlagDigging):
		pose = poseDigging
	case e.bedrock == "minecraft:warden" && e.flag(protocol.EntityDataFlagRoaring):
		pose = poseRoaring
	case e.bedrock == "minecraft:warden" && e.flag(protocol.EntityDataFlagSniffing):
		pose = poseSniffing
	}
	d.varVal(6, serPose, pose)
	if f := metaFloat(m, protocol.EntityDataKeyFreezingEffectStrength); f > 0 && e.living() {
		d.intVal(7, int32(f*140))
	}

	if e.living() {
		d.byteVal(8, bit(protocol.EntityDataFlagUsingItem, 0x01))
		if e.hasHealth {
			h := e.health
			if e.dead {
				h = 0
			}
			d.floatVal(9, h)
		}
		p.writeEffectData(d, e)
		bp, ok := m[protocol.EntityDataKeyBedPosition].(protocol.BlockPos)
		sleeping := ok && e.flag(protocol.EntityDataFlagSleeping)
		d.head(14, serOptBlockPos)
		d.w.Bool(sleeping)
		if sleeping {
			d.w.Position(int(bp[0]), int(bp[1]), int(bp[2]))
		}
	}
	if kind&entMob != 0 {
		target, _ := m[protocol.EntityDataKeyTarget].(int64)
		d.byteVal(15, bit(protocol.EntityDataFlagNoAI, 0x01)|bit(protocol.EntityDataFlagAngry, 0x04)|aggressive(target))
	}
	baby := e.flag(protocol.EntityDataFlagBaby)
	if kind&(entAgeable|entZombieBaby) != 0 {
		d.boolVal(16, baby)
	}
	if kind&entPiglinBaby != 0 {
		d.boolVal(17, baby)
	}
	switch {
	case kind&entPlayer != 0:
		d.byteVal(16, 0x7f) // skin layers
	case e.bedrock == "minecraft:armor_stand":
		d.byteVal(15, 0x04) // arms: Bedrock stands always have them
		pose := armourStandPoses[0]
		if i := metaInt(m, protocol.EntityDataKeyPoseIndex); i >= 0 && int(i) < len(armourStandPoses) {
			pose = armourStandPoses[i]
		}
		for i, r := range pose {
			d.head(byte(16+i), serRotations)
			d.w.Float32(r[0])
			d.w.Float32(r[1])
			d.w.Float32(r[2])
		}
	case e.bedrock == "minecraft:item":
		var js jitem.Stack
		if p.javaItem(e.item, &js) {
			d.head(8, serItemStack)
			js.EncodeFor(d.w, jitem.For(p.ver()))
		}
	case e.bedrock == "minecraft:xp_orb":
		d.intVal(8, max(1, metaInt(m, protocol.EntityDataKeyValue)))
	case e.bedrock == "minecraft:tnt":
		d.intVal(8, max(0, metaInt(m, protocol.EntityDataKeyFuseTime)))
	case kind&entArrow != 0:
		d.byteVal(8, bit(protocol.EntityDataFlagCritical, 0x01))
		if e.bedrock == "minecraft:thrown_trident" {
			d.boolVal(12, e.flag(protocol.EntityDataFlagEnchanted))
		}
	case e.bedrock == "minecraft:painting":
		d.varVal(8, serDirection, e.data)
		if id := p.ver().RegistryID("minecraft:painting_variant", "minecraft:"+e.painting); id >= 0 {
			d.varVal(9, serPainting, id+1)
		}
	case e.bedrock == "minecraft:frame":
		d.varVal(8, serDirection, e.data)
		var js jitem.Stack
		d.head(9, serItemStack)
		if frameItem(e.frameNBT, &js) {
			if it, ok := e.frameNBT["Item"].(map[string]any); ok {
				// A custom item: paper with the converted pack's model and name, as in inventories.
				if name, _ := it["Name"].(string); name != "" && !strings.HasPrefix(name, "minecraft:") {
					js.ID = javaPaper
					p.packItem(name, &js)
				}
			}
			js.EncodeFor(d.w, jitem.For(p.ver()))
		} else {
			jitem.WriteEmpty(d.w)
		}
		d.intVal(10, frameRotation(e.frameNBT))
	case e.bedrock == "minecraft:wither_skull_dangerous":
		d.boolVal(8, true)
	case e.bedrock == "minecraft:egg":
		p.writePropData(d, e)
	case kind&entMob != 0:
		p.writeMobData(d, e)
		p.writePropData(d, e)
	}
}

// aggressive is the Mob flag of a mob with an attack target (raised arms, drawn weapons).
func aggressive(target int64) byte {
	if target != 0 && target != -1 {
		return 0x04
	}
	return 0
}

// writeMobData writes the type-specific data of a mob (v1's mobdata.go).
func (p *Player) writeMobData(d dataWriter, e *entity) {
	m := e.meta
	bit := func(flag int, b byte) byte {
		if e.flag(flag) {
			return b
		}
		return 0
	}
	variant := metaInt(m, protocol.EntityDataKeyVariant)
	mark := metaInt(m, protocol.EntityDataKeyMarkVariant)
	colour := metaInt(m, protocol.EntityDataKeyColorIndex)
	tame := bit(protocol.EntityDataFlagSitting, 0x01) | bit(protocol.EntityDataFlagTamed, 0x04)
	switch e.bedrock {
	case "minecraft:horse", "minecraft:donkey", "minecraft:mule", "minecraft:llama", "minecraft:trader_llama",
		"minecraft:skeleton_horse", "minecraft:zombie_horse", "minecraft:camel", "minecraft:camel_husk":
		d.byteVal(18, bit(protocol.EntityDataFlagTamed, 0x02)|bit(protocol.EntityDataFlagEating, 0x10)|bit(protocol.EntityDataFlagStanding, 0x20))
		switch e.bedrock {
		case "minecraft:horse":
			d.intVal(19, variant&0xff|(mark&0xff)<<8)
		case "minecraft:donkey", "minecraft:mule":
			d.boolVal(19, e.flag(protocol.EntityDataFlagChested))
		case "minecraft:llama", "minecraft:trader_llama":
			d.boolVal(19, e.flag(protocol.EntityDataFlagChested))
			d.intVal(20, max(1, min(5, metaInt(m, protocol.EntityDataKeyStrength))))
			d.intVal(21, max(0, min(3, variant)))
		}
	case "minecraft:sheep":
		d.byteVal(18, byte(colour&0x0f)|bit(protocol.EntityDataFlagSheared, 0x10))
	case "minecraft:cat":
		d.byteVal(18, tame)
		if variant >= 0 && int(variant) < len(catVariants) {
			d.varVal(20, serCatVariant, catVariants[variant])
		}
		d.boolVal(21, e.flag(protocol.EntityDataFlagResting))
		if e.flag(protocol.EntityDataFlagTamed) {
			d.intVal(23, colour&0x0f)
		}
	case "minecraft:wolf":
		d.byteVal(18, tame)
		d.boolVal(20, e.flag(protocol.EntityDataFlagInterested))
		if e.flag(protocol.EntityDataFlagTamed) {
			d.intVal(21, colour&0x0f)
		}
		if variant >= 0 && int(variant) < len(wolfVariants) {
			d.varVal(23, serWolfVariant, wolfVariants[variant])
		}
	case "minecraft:parrot":
		d.byteVal(18, tame)
		d.intVal(20, max(0, min(4, variant)))
	case "minecraft:frog":
		if variant >= 0 && int(variant) < len(frogVariants) {
			d.varVal(18, serFrogVariant, frogVariants[variant])
		}
	case "minecraft:villager_v2", "minecraft:villager", "minecraft:npc", "minecraft:zombie_villager_v2", "minecraft:zombie_villager":
		index := byte(19)
		if e.bedrock == "minecraft:zombie_villager_v2" || e.bedrock == "minecraft:zombie_villager" {
			index = 20
			d.boolVal(19, e.flag(protocol.EntityDataFlagConverting))
		}
		typ, prof := int32(2), int32(0)
		if e.bedrock == "minecraft:villager_v2" || e.bedrock == "minecraft:zombie_villager_v2" {
			if mark >= 0 && int(mark) < len(villagerType) {
				typ = villagerType[mark]
			}
			if variant >= 0 && int(variant) < len(villagerProfession) {
				prof = villagerProfession[variant]
			}
		}
		d.head(index, serVillagerData)
		d.w.VarInt(typ)
		d.w.VarInt(prof)
		d.w.VarInt(max(1, metaInt(m, protocol.EntityDataKeyTradeTier)+1))
	case "minecraft:fox":
		d.intVal(18, max(0, min(1, variant)))
		d.byteVal(19, bit(protocol.EntityDataFlagSitting, 0x01)|bit(protocol.EntityDataFlagSneaking, 0x04)|
			bit(protocol.EntityDataFlagInterested, 0x08)|bit(protocol.EntityDataFlagSleeping, 0x20))
	case "minecraft:rabbit":
		d.intVal(18, max(0, min(5, variant)))
	case "minecraft:mooshroom":
		d.intVal(18, max(0, min(1, variant)))
	case "minecraft:axolotl":
		v := max(0, min(4, variant))
		if v == 1 || v == 3 {
			v = 4 - v // Bedrock 1 cyan / 3 wild, Java 1 wild / 3 cyan
		}
		d.intVal(18, v)
		d.boolVal(19, e.flag(protocol.EntityDataFlagPlayingDead))
	case "minecraft:tropicalfish":
		d.intVal(17, variant&0xff|(mark&0xff)<<8|(colour&0xff)<<16|(metaInt(m, protocol.EntityDataKeyColorTwoIndex)&0xff)<<24)
	case "minecraft:pufferfish":
		d.intVal(17, max(0, min(2, metaInt(m, protocol.EntityDataKeyPuffedState))))
	case "minecraft:slime", "minecraft:magma_cube":
		size := int32(math.Round(float64(metaFloat(m, protocol.EntityDataKeyWidth)) / 0.52))
		d.intVal(18, max(1, size))
	case "minecraft:goat":
		horns := metaInt(m, protocol.EntityDataKeyGoatHornCount)
		d.boolVal(19, horns >= 1)
		d.boolVal(20, horns >= 2)
	case "minecraft:creeper":
		swell := int32(-1)
		if e.flag(protocol.EntityDataFlagIgnited) || metaInt(m, protocol.EntityDataKeySwell) > 0 {
			swell = 1
		}
		d.intVal(16, swell)
		d.boolVal(17, e.flag(protocol.EntityDataFlagPowered))
		d.boolVal(18, e.flag(protocol.EntityDataFlagIgnited))
	case "minecraft:enderman":
		state := int32(0)
		if rid := metaInt(m, protocol.EntityDataKeyCarryBlockRuntimeID); rid != 0 {
			state = p.ver().BlockState(p.blockState(uint32(rid)))
		}
		d.varVal(16, serOptBlockState, state)
		d.boolVal(18, e.flag(protocol.EntityDataFlagAngry))
	case "minecraft:bat":
		d.byteVal(16, bit(protocol.EntityDataFlagResting, 0x01))
	case "minecraft:spider", "minecraft:cave_spider":
		d.byteVal(16, bit(protocol.EntityDataFlagWallClimbing, 0x01))
	case "minecraft:blaze":
		d.byteVal(16, bit(protocol.EntityDataFlagOnFire, 0x01)|bit(protocol.EntityDataFlagCharged, 0x01))
	case "minecraft:snow_golem":
		d.byteVal(16, 0x10&^bit(protocol.EntityDataFlagSheared, 0x10))
	case "minecraft:polar_bear":
		d.boolVal(18, e.flag(protocol.EntityDataFlagStanding))
	case "minecraft:turtle":
		d.boolVal(18, e.flag(protocol.EntityDataFlagPregnant))
		d.boolVal(19, e.flag(protocol.EntityDataFlagLayingEgg))
	case "minecraft:ghast":
		d.boolVal(16, metaInt(m, protocol.EntityDataKeyChargeAmount) > 0)
	case "minecraft:pillager":
		d.boolVal(16, e.flag(protocol.EntityDataFlagCelebrating))
		d.boolVal(17, e.flag(protocol.EntityDataFlagCharging))
	case "minecraft:vindicator", "minecraft:ravager", "minecraft:evocation_illager":
		d.boolVal(16, e.flag(protocol.EntityDataFlagCelebrating))
	case "minecraft:witch":
		d.boolVal(16, e.flag(protocol.EntityDataFlagCelebrating))
		d.boolVal(17, e.flag(protocol.EntityDataFlagUsingItem))
	case "minecraft:vex":
		d.byteVal(16, bit(protocol.EntityDataFlagCharging, 0x01))
	case "minecraft:piglin":
		d.boolVal(18, e.flag(protocol.EntityDataFlagCharging))
		d.boolVal(19, e.flag(protocol.EntityDataFlagDancing))
	}
}

// itemName is the name of a server item network id.
func (p *Player) itemName(id int32) string {
	if p.rem == nil {
		return ""
	}
	if p.ents.itemNames == nil {
		p.ents.itemNames = make(map[int32]string, len(p.rem.gd.Items))
		for _, it := range p.rem.gd.Items {
			p.ents.itemNames[int32(it.RuntimeID)] = it.Name
		}
	}
	return p.ents.itemNames[id]
}

// javaItem converts a Bedrock item stack: the Java item, count, enchantment glint and leather
// colour (what an entity shows). It reports false for an empty or unknown stack.
func (p *Player) javaItem(st protocol.ItemStack, js *jitem.Stack) bool {
	if javaItemHook != nil {
		return javaItemHook(p, st, js)
	}
	js.Reset()
	if st.NetworkID == 0 || st.Count == 0 {
		return false
	}
	name := p.itemName(st.NetworkID)
	if name == "" {
		return false
	}
	id, _ := javamap.ItemID(name, int16(st.MetadataValue))
	js.ID, js.Count = id, int32(max(1, min(99, st.Count)))
	if nbt := st.NBTData; nbt != nil {
		if _, ok := nbt["ench"]; ok {
			js.EnchantmentGlintOverride = true
			js.Add(jitem.CompEnchantmentGlintOverride)
		}
		if c, ok := nbt["customColor"].(int32); ok {
			js.DyedColor = c & 0xffffff
			js.Add(jitem.CompDyedColor)
		}
	}
	return true
}

// sameStack reports whether two stacks look the same (item, count, data, no NBT both or the
// same NBT size).
func sameStack(a, b protocol.ItemStack) bool {
	if a.NetworkID == 0 && b.NetworkID == 0 {
		return true
	}
	return a.NetworkID == b.NetworkID && a.MetadataValue == b.MetadataValue && a.Count == b.Count &&
		a.BlockRuntimeID == b.BlockRuntimeID && len(a.NBTData) == len(b.NBTData)
}

// writeItem writes an item stack (the empty stack if it converts to none).
func (p *Player) writeItem(w *wire.Writer, st protocol.ItemStack) {
	var js jitem.Stack
	if !p.javaItem(st, &js) {
		jitem.WriteEmpty(w)
		return
	}
	js.EncodeFor(w, jitem.For(p.ver()))
}

// sendEquipment sends what a living entity holds and wears.
func (p *Player) sendEquipment(e *entity) {
	w := p.pk()
	w.VarInt(e.id)
	for slot := range e.equip {
		b := byte(slot)
		if slot < len(e.equip)-1 {
			b |= 0x80
		}
		w.Byte(b)
		if slot == slotMainHand && e.equip[slot].NetworkID == 0 && propHeldItem(p, w, e) {
			continue
		}
		p.writeItem(w, e.equip[slot])
	}
	p.send(v777.ClientboundPlaySetEquipment, w)
	p.sendSaddle(e)
}

// saddleable are the Bedrock mobs whose saddle is a flag.
var saddleable = map[string]bool{
	"minecraft:pig": true, "minecraft:strider": true, "minecraft:horse": true, "minecraft:donkey": true,
	"minecraft:mule": true, "minecraft:camel": true, "minecraft:camel_husk": true, "minecraft:skeleton_horse": true,
	"minecraft:zombie_horse": true, "minecraft:nautilus": true, "minecraft:zombie_nautilus": true,
}

// sendSaddle shows a saddle (the Bedrock saddled flag) in the Java saddle slot.
func (p *Player) sendSaddle(e *entity) {
	if !saddleable[e.bedrock] {
		return
	}
	saddled := int8(0)
	if e.flag(protocol.EntityDataFlagSaddled) {
		saddled = 1
	}
	if saddled == e.saddle {
		return
	}
	e.saddle = saddled
	w := p.pk()
	w.VarInt(e.id)
	w.Byte(slotSaddle)
	if e.flag(protocol.EntityDataFlagSaddled) {
		var js jitem.Stack
		js.ID, js.Count = v777.BuiltinID("minecraft:item", "minecraft:saddle"), 1
		js.EncodeFor(w, jitem.For(p.ver()))
	} else {
		jitem.WriteEmpty(w)
	}
	p.send(v777.ClientboundPlaySetEquipment, w)
}
