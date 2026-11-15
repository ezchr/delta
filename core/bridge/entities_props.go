package bridge

import (
	"math"
	"strings"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Actor properties, the scale attribute, others' effect particles and the attributes of
// rideable mobs.
//
// Bedrock keeps some entity state in actor properties instead of metadata: the server lists them
// per entity type once (SyncActorProperty: int/float/bool/enum properties, in order) and
// AddActor/SetActorData carry values by index into that list. The ones Java has entity data for
// become entity data (writePropData): climate variants of cows/pigs/chickens (and the egg item),
// the sound variants of cows/pigs/chickens/cats/wolves, the zombie nautilus variant, armadillo
// state, creaking state, bee nectar, copper golem oxidation and chest state, happy ghast
// "stays still".
//
// Bedrock's scale metadata (EntityDataKeyScale) is the Java minecraft:scale attribute, relative to
// the size the Java client already gives babies (Bedrock babies carry their baby scale, 0.5 for
// most) and slime-like mobs (their size is entity data). Others' effects come as the Bedrock
// visible-effects metadata (up to 8 effects, 7 bits each: effect id << 1 | ambient), shown as the
// Java effect particle list (LivingEntity data 10) and its ambient flag (11).

// Java entity data serializers used here (26.2 and 26.3 have the same ids).
const (
	serParticles      = 17
	serCatSound       = 22
	serCowVariant     = 23
	serCowSound       = 24
	serWolfSound      = 26
	serPigVariant     = 28
	serPigSound       = 29
	serChickenVariant = 30
	serChickenSound   = 31
	serNautilusVar    = 32
	serArmadillo      = 36
	serCopperGolem    = 37
	serWeathering     = 38
)

// SyncActorProperty property types.
const (
	propInt   = 0
	propFloat = 1
	propBool  = 2
	propEnum  = 3
)

// effectAmbientAlpha is the particle alpha of ambient (beacon) effects (MobEffect.AMBIENT_ALPHA).
const effectAmbientAlpha = 38

// propDef is one property an entity type has.
type propDef struct {
	name string
	kind int32
	enum []string
}

// actorProps are the property lists of a server, by entity type.
type actorProps struct {
	rem   *Remote
	types map[string][]propDef
}

func init() {
	OnBedrock(packet.IDSyncActorProperty, func(p *Player, pk *packet.SyncActorProperty) {
		pr := &p.ents.props
		if pr.rem != p.rem {
			*pr = actorProps{rem: p.rem, types: map[string][]propDef{}}
		}
		typ, _ := pk.PropertyData["type"].(string)
		list, _ := pk.PropertyData["properties"].([]any)
		if typ == "" {
			return
		}
		defs := make([]propDef, 0, len(list))
		for _, v := range list {
			m, _ := v.(map[string]any)
			d := propDef{kind: nbtInt32(m["type"])}
			d.name, _ = m["name"].(string)
			if en, ok := m["enum"].([]any); ok {
				for _, s := range en {
					str, _ := s.(string)
					d.enum = append(d.enum, str)
				}
			}
			defs = append(defs, d)
		}
		pr.types[typ] = defs
		if debugPackets {
			names := make([]string, 0, len(defs))
			for _, d := range defs {
				names = append(names, d.name+"("+strings.Join(d.enum, "|")+")")
			}
			p.log.Debug("actor properties", "type", typ, "props", strings.Join(names, " "))
		}
	})
	OnBedrock(packet.IDUpdateAttributes, func(p *Player, pk *packet.UpdateAttributes) {
		e, ok := p.ents.shown[pk.EntityRuntimeID]
		if !ok || !e.living() || !saddleable[e.bedrock] {
			return
		}
		changed := false
		for _, a := range pk.Attributes {
			switch a.Name {
			case "minecraft:movement":
				changed = changed || e.attrSpeed != a.Value
				e.attrSpeed = a.Value
			case "minecraft:horse.jump_strength":
				changed = changed || e.attrJump != a.Value
				e.attrJump = a.Value
			}
		}
		if changed && e.spawned {
			p.sendMountAttributes(e)
		}
	})
}

func nbtInt32(v any) int32 {
	switch v := v.(type) {
	case int32:
		return v
	case int16:
		return int32(v)
	case uint8:
		return int32(v)
	case int8:
		return int32(v)
	case int64:
		return int32(v)
	}
	return -1
}

// applyProps merges actor property values into e (names from the server's property list). It
// reports whether any changed.
func (p *Player) applyProps(e *entity, ps protocol.EntityProperties) bool {
	if len(ps.IntegerProperties) == 0 && len(ps.FloatProperties) == 0 {
		return false
	}
	pr := &p.ents.props
	if pr.rem != p.rem {
		return false
	}
	defs := pr.types[e.bedrock]
	if e.props == nil {
		e.props = map[string]any{}
	}
	changed := false
	set := func(name string, v any) {
		if e.props[name] != v {
			e.props[name], changed = v, true
		}
	}
	for _, ip := range ps.IntegerProperties {
		if int(ip.Index) >= len(defs) {
			continue
		}
		d := defs[ip.Index]
		switch d.kind {
		case propBool:
			set(d.name, ip.Value != 0)
		case propEnum:
			if ip.Value >= 0 && int(ip.Value) < len(d.enum) {
				set(d.name, d.enum[ip.Value])
			}
		default:
			set(d.name, ip.Value)
		}
	}
	for _, fp := range ps.FloatProperties {
		if int(fp.Index) < len(defs) {
			set(defs[fp.Index].name, fp.Value)
		}
	}
	return changed
}

// propEnum is an enum property's value ("" if the server gave none).
func (e *entity) propEnum(name string) string {
	s, _ := e.props[name].(string)
	return s
}

// propBool is a bool property's value and whether the server gave one.
func (e *entity) propBool(name string) (bool, bool) {
	b, ok := e.props[name].(bool)
	return b, ok
}

// soundVariant is the Java sound variant name of a Bedrock sound_variant value.
func soundVariant(v string) string {
	switch v {
	case "default":
		return "classic"
	case "mad":
		return "angry"
	}
	return v
}

// registryVal writes a variant holder (the plain network id of a synchronised registry entry),
// if the client's registry has the entry.
func (p *Player) registryVal(d dataWriter, index byte, ser int32, registry, entry string) {
	if entry == "" {
		return
	}
	if id := p.ver().RegistryID(registry, "minecraft:"+entry); id >= 0 {
		d.varVal(index, ser, id)
	}
}

// writePropData writes the entity data that comes from a mob's actor properties (indices the
// type's other data does not use).
func (p *Player) writePropData(d dataWriter, e *entity) {
	if e.bedrock == "minecraft:salmon" {
		// Bedrock salmon sizes are scales (0.5, 1, 1.5); Java's are a type (small, medium,
		// large).
		s, ok := e.meta[protocol.EntityDataKeyScale].(float32)
		if ok {
			d.intVal(17, max(0, min(2, int32(math.Round(float64(s)*2))-1)))
		}
		return
	}
	if len(e.props) == 0 {
		return
	}
	climate := e.propEnum("minecraft:climate_variant")
	sound := soundVariant(e.propEnum("minecraft:sound_variant"))
	switch e.bedrock {
	case "minecraft:cow":
		p.registryVal(d, 18, serCowVariant, "minecraft:cow_variant", climate)
		p.registryVal(d, 19, serCowSound, "minecraft:cow_sound_variant", sound)
	case "minecraft:pig":
		p.registryVal(d, 19, serPigVariant, "minecraft:pig_variant", climate)
		p.registryVal(d, 20, serPigSound, "minecraft:pig_sound_variant", sound)
	case "minecraft:chicken":
		p.registryVal(d, 18, serChickenVariant, "minecraft:chicken_variant", climate)
		p.registryVal(d, 19, serChickenSound, "minecraft:chicken_sound_variant", sound)
	case "minecraft:cat":
		p.registryVal(d, 24, serCatSound, "minecraft:cat_sound_variant", sound)
	case "minecraft:wolf":
		p.registryVal(d, 24, serWolfSound, "minecraft:wolf_sound_variant", sound)
	case "minecraft:zombie_nautilus":
		v := map[string]string{"default": "temperate", "coral": "warm"}[e.propEnum("minecraft:variant")]
		p.registryVal(d, 21, serNautilusVar, "minecraft:zombie_nautilus_variant", v)
	case "minecraft:armadillo":
		if st, ok := armadilloStates[e.propEnum("minecraft:armadillo_state")]; ok {
			d.varVal(18, serArmadillo, st)
		}
	case "minecraft:creaking":
		st := e.propEnum("minecraft:creaking_state")
		if st == "" {
			break
		}
		// Java: can move (nobody looks at it), active (hostile: the eyes glow), tearing down
		// (crumbling after its heart broke).
		d.boolVal(16, st == "neutral" || st == "hostile_unobserved")
		d.boolVal(17, st != "neutral")
		d.boolVal(18, st == "crumbling")
	case "minecraft:bee":
		if v, ok := e.propBool("minecraft:has_nectar"); ok {
			var fl byte
			if v {
				fl = 0x08
			}
			d.byteVal(18, fl)
		}
	case "minecraft:copper_golem":
		if st, ok := copperOxidation[e.propEnum("minecraft:oxidation_level")]; ok {
			d.varVal(16, serWeathering, st)
		}
		if st, ok := copperGolemStates[e.propEnum("minecraft:chest_interaction")]; ok {
			d.varVal(17, serCopperGolem, st)
		}
	case "minecraft:happy_ghast":
		if v, ok := e.propBool("minecraft:can_move"); ok {
			d.boolVal(19, !v)
		}
	case "minecraft:egg":
		// A thrown egg shows its item: blue (cold) and brown (warm) eggs.
		item := map[string]string{"cold": "minecraft:blue_egg", "warm": "minecraft:brown_egg"}[climate]
		if id := v777.BuiltinID("minecraft:item", item); item != "" && id >= 0 {
			var js jitem.Stack
			js.ID, js.Count = id, 1
			d.head(8, serItemStack)
			js.EncodeFor(d.w, jitem.For(p.ver()))
		}
	}
}

// heldByProperty reports whether e holds an item Bedrock gives as an actor property (a copper
// golem's flower), which Java has as the main hand item.
func heldByProperty(e *entity) bool {
	v, _ := e.propBool("minecraft:has_flower")
	return e.bedrock == "minecraft:copper_golem" && (v || e.props["minecraft:has_flower"] != nil)
}

// propHeldItem writes the main hand item an actor property gives (false: none).
func propHeldItem(p *Player, w *wire.Writer, e *entity) bool {
	if v, _ := e.propBool("minecraft:has_flower"); !v || e.bedrock != "minecraft:copper_golem" {
		return false
	}
	var js jitem.Stack
	js.ID, js.Count = v777.BuiltinID("minecraft:item", "minecraft:poppy"), 1
	js.EncodeFor(w, jitem.For(p.ver()))
	return true
}

// Bedrock armadillo_state -> Java ArmadilloState (0 idle, 1 rolling, 2 scared, 3 unrolling):
// rolling plays the roll-up and stays in the shell; scared is the one that peeks out.
var armadilloStates = map[string]int32{
	"unrolled": 0, "rolled_up": 1, "rolled_up_peeking": 2, "rolled_up_relaxing": 2, "rolled_up_unrolling": 3,
}

// Bedrock oxidation_level -> Java WeatheringCopper.WeatherState.
var copperOxidation = map[string]int32{"unoxidized": 0, "exposed": 1, "weathered": 2, "oxidized": 3}

// Bedrock chest_interaction -> Java CopperGolemState (idle, getting item, getting no item,
// dropping item, dropping no item).
var copperGolemStates = map[string]int32{"none": 0, "take": 1, "take_fail": 2, "put": 3, "put_fail": 4}

// Effect particles.

// effectColours are the Java particle colours of the Bedrock effect ids (MobEffects), and
// effectParticles the effects with a particle of their own instead.
var (
	effectColours = map[int32]int32{
		1: 3402751, 2: 9154528, 3: 14270531, 4: 4866583, 5: 16762624, 6: 16262179, 7: 11101546,
		8: 16646020, 9: 5578058, 10: 13458603, 11: 9520880, 12: 16750848, 13: 10017472, 14: 16185078,
		15: 2039587, 16: 12779366, 17: 5797459, 18: 4738376, 19: 8889187, 20: 7561558, 21: 16284963,
		22: 2445989, 23: 16262179, 24: 13565951, 25: 8889187, 26: 1950417, 27: 15978425, 28: 745784,
		29: 4521796, 30: 2696993, 32: 12438015, 33: 7891290, 34: 10092451, 35: 9214860, 37: 65518,
	}
	effectParticles = map[int32]string{31: "minecraft:trial_omen", 36: "minecraft:raid_omen"}
)

// visibleEffects decodes the Bedrock visible-effects metadata: effect ids and ambient flags.
func visibleEffects(packed int64) (ids []int32, ambient []bool) {
	for i := 0; i < 8; i++ {
		v := packed >> (7 * i) & 0x7f
		if v>>1 == 0 {
			continue
		}
		ids = append(ids, int32(v>>1))
		ambient = append(ambient, v&1 != 0)
	}
	return ids, ambient
}

// writeEffectData writes a living entity's effect particles (10) and ambient flag (11).
func (p *Player) writeEffectData(d dataWriter, e *entity) {
	packed, ok := e.meta[protocol.EntityDataKeyVisibleMobEffects].(int64)
	if !ok {
		return
	}
	ids, ambient := visibleEffects(packed)
	colourType := p.ver().BuiltinID("minecraft:particle_type", "minecraft:entity_effect")
	type particle struct {
		typ    int32
		colour int32
		plain  bool
	}
	list := make([]particle, 0, len(ids))
	allAmbient := len(ids) > 0
	for i, id := range ids {
		allAmbient = allAmbient && ambient[i]
		if name, ok := effectParticles[id]; ok {
			if t := p.ver().BuiltinID("minecraft:particle_type", name); t >= 0 {
				list = append(list, particle{typ: t, plain: true})
			}
			continue
		}
		c, ok := effectColours[id]
		if !ok || colourType < 0 {
			continue
		}
		alpha := int32(255)
		if ambient[i] {
			alpha = effectAmbientAlpha
		}
		list = append(list, particle{typ: colourType, colour: alpha<<24 | c})
	}
	d.head(10, serParticles)
	d.w.VarInt(int32(len(list)))
	for _, pt := range list {
		d.w.VarInt(pt.typ)
		if !pt.plain {
			d.w.Int32(pt.colour)
		}
	}
	d.boolVal(11, allAmbient)
}

// Scale.

// sizeByData are the mobs whose size Java takes from their entity data (slime sizes, phantom
// size, puffer states, salmon types), not from the scale attribute.
var sizeByData = map[string]bool{
	"minecraft:slime": true, "minecraft:magma_cube": true, "minecraft:sulfur_cube": true, "minecraft:phantom": true,
	"minecraft:pufferfish": true, "minecraft:salmon": true,
}

// The scales vanilla Bedrock gives a type (its behaviour pack's minecraft:scale, checked on BDS
// 1.26.60): adults 1 and babies 0.5 unless listed. Java draws those sizes itself (its models and
// baby sizes), so only a scale other than the vanilla one becomes the scale attribute.
var (
	bedrockAdultScale = map[string]float32{
		"minecraft:cat": 0.8, "minecraft:rabbit": 0.6, "minecraft:tropicalfish": 1.3, "minecraft:wither_skeleton": 1.2,
	}
	bedrockBabyScale = map[string]float32{
		"minecraft:armadillo": 0.6, "minecraft:camel": 0.45, "minecraft:camel_husk": 0.45, "minecraft:cat": 0.4,
		"minecraft:dolphin": 0.65, "minecraft:happy_ghast": 0.2375, "minecraft:panda": 0.4, "minecraft:rabbit": 0.4,
		"minecraft:sniffer": 0.45, "minecraft:turtle": 0.16,
	}
)

// javaScale is the Java scale attribute for e's Bedrock scale.
func (e *entity) javaScale() float64 {
	s, ok := e.meta[protocol.EntityDataKeyScale].(float32)
	if !ok || s <= 0 || sizeByData[e.bedrock] || !e.living() {
		return 1
	}
	base := float32(1)
	if b, ok := bedrockAdultScale[e.bedrock]; ok {
		base = b
	}
	if e.flag(protocol.EntityDataFlagBaby) && e.t.kind&(entAgeable|entZombieBaby|entPiglinBaby) != 0 {
		base = 0.5
		if b, ok := bedrockBabyScale[e.bedrock]; ok {
			base = b
		}
	}
	v := float64(s / base)
	if math.Abs(v-1) < 0.01 {
		return 1
	}
	return max(0.0625, min(16, v)) // the attribute's range
}

// sendScale sends e's scale attribute when it changed (1 needs no packet for a new entity).
func (p *Player) sendScale(e *entity) {
	s := e.javaScale()
	if s == e.sentScale || (e.sentScale == 0 && s == 1) {
		return
	}
	e.sentScale = s
	p.sendEntityAttributes(e.id, []string{"minecraft:scale"}, []float64{s})
}

// sendMountAttributes gives a rideable mob the server's speed and jump strength, which the Java
// client uses when the player drives it.
func (p *Player) sendMountAttributes(e *entity) {
	var names []string
	var vals []float64
	if e.attrSpeed > 0 {
		names, vals = append(names, "minecraft:movement_speed"), append(vals, float64(e.attrSpeed))
	}
	if e.attrJump > 0 {
		names, vals = append(names, "minecraft:jump_strength"), append(vals, float64(e.attrJump))
	}
	p.sendEntityAttributes(e.id, names, vals)
}

// sendEntityAttributes sets base values of an entity's attributes (registry names).
func (p *Player) sendEntityAttributes(id int32, names []string, values []float64) {
	var legacy []int32
	if !p.ver().Native() {
		legacy = p.ver().BuiltinTable("minecraft:attribute")
	}
	ids := make([]int32, 0, len(names))
	vals := make([]float64, 0, len(names))
	for i, name := range names {
		aid := v777.BuiltinID("minecraft:attribute", name)
		if aid >= 0 && legacy != nil {
			aid = version.Map(legacy, aid)
		}
		if aid >= 0 {
			ids, vals = append(ids, aid), append(vals, values[i])
		}
	}
	if len(ids) == 0 {
		return
	}
	w := p.pk()
	w.VarInt(id)
	w.VarInt(int32(len(ids)))
	for i := range ids {
		w.VarInt(ids[i])
		w.Float64(vals[i])
		w.VarInt(0)
	}
	p.send(v777.ClientboundPlayUpdateAttributes, w)
}
