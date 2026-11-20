package bridge

import (
	"fmt"
	"strings"
	"sync"

	"delta/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/textcolour"
	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Items (milestone 3): the server's item ids (StartGame's item list, ItemRegistry) and the
// conversion of a Bedrock item stack to the Java stack shown for it: the item (internal/javamap,
// unknown and custom items as named paper), count, name, lore, enchantments (glint), damage,
// unbreakable, leather colour, potions and tipped arrows.

// itemTable is the item numbering of one server.
type itemTable struct {
	from  *Remote
	names map[int32]string
	ids   map[string]int32
	// custom is what the Java client needs of the custom items (customitems.go).
	custom map[int32]customItem
}

// items is the item table of the current connection (built on first use).
func (p *Player) items() *itemTable {
	w := p.win
	if w.itab != nil && w.itab.from == p.rem {
		return w.itab
	}
	t := &itemTable{from: p.rem, names: map[int32]string{}, ids: map[string]int32{}, custom: map[int32]customItem{}}
	if p.rem != nil {
		t.add(p.rem.gd.Items)
		t.logCustom(p, p.rem.gd.Items)
	}
	w.itab = t
	return t
}

// logCustom logs (debug) what the Java client gets of each custom item in entries.
func (t *itemTable) logCustom(p *Player, entries []protocol.ItemEntry) {
	n := 0
	for _, e := range entries {
		if !e.ComponentBased {
			continue
		}
		n++
		if c, ok := t.custom[int32(e.RuntimeID)]; ok {
			p.log.Debug("custom item", "name", e.Name, "java", c.String())
		}
	}
	if n > 0 {
		p.log.Debug("custom items", "n", n, "withJavaComponents", len(t.custom))
	}
}

func (t *itemTable) add(entries []protocol.ItemEntry) {
	for _, e := range entries {
		t.names[int32(e.RuntimeID)] = e.Name
		t.ids[e.Name] = int32(e.RuntimeID)
		if e.ComponentBased {
			if c, ok := parseCustomItem(e.Data); ok {
				t.custom[int32(e.RuntimeID)] = c
			} else {
				delete(t.custom, int32(e.RuntimeID))
			}
		}
	}
}

func init() {
	OnBedrock(packet.IDItemRegistry, func(p *Player, pk *packet.ItemRegistry) {
		t := p.items()
		t.add(pk.Items)
		t.logCustom(p, pk.Items)
	})
	// Entities (milestone 2) show items and use the held item through these hooks.
	javaItemHook = func(p *Player, st protocol.ItemStack, js *jitem.Stack) bool {
		p.javaStack(st, js)
		return !js.Empty()
	}
	entityHeldItem = func(p *Player) (int32, protocol.ItemInstance) { return int32(p.win.held), p.HeldItem() }
}

// stackName is the server's name of an item stack ("" for an empty one).
func (p *Player) stackName(st protocol.ItemStack) string {
	if st.NetworkID == 0 || st.Count == 0 {
		return ""
	}
	return p.items().names[st.NetworkID]
}

// Java item ids the conversion needs.
var (
	javaPaper         = mustJavaItem("minecraft:paper")
	javaTippedArrow   = mustJavaItem("minecraft:tipped_arrow")
	javaArrow         = mustJavaItem("minecraft:arrow")
	javaEnchantedBook = mustJavaItem("minecraft:enchanted_book")
	javaPotion        = mustJavaItem("minecraft:potion")
	javaSplashPotion  = mustJavaItem("minecraft:splash_potion")
	javaLingering     = mustJavaItem("minecraft:lingering_potion")
)

func mustJavaItem(name string) int32 {
	id, ok := jitem.ByName(name)
	if !ok {
		panic("bridge: no Java item " + name)
	}
	return id
}

// bedrockEnchantments are the Java keys of Bedrock enchantment ids.
var bedrockEnchantments = [...]string{
	0: "protection", 1: "fire_protection", 2: "feather_falling", 3: "blast_protection",
	4: "projectile_protection", 5: "thorns", 6: "respiration", 7: "depth_strider", 8: "aqua_affinity",
	9: "sharpness", 10: "smite", 11: "bane_of_arthropods", 12: "knockback", 13: "fire_aspect",
	14: "looting", 15: "efficiency", 16: "silk_touch", 17: "unbreaking", 18: "fortune", 19: "power",
	20: "punch", 21: "flame", 22: "infinity", 23: "luck_of_the_sea", 24: "lure", 25: "frost_walker",
	26: "mending", 27: "binding_curse", 28: "vanishing_curse", 29: "impaling", 30: "riptide",
	31: "loyalty", 32: "channeling", 33: "multishot", 34: "piercing", 35: "quick_charge",
	36: "soul_speed", 37: "swift_sneak", 38: "wind_burst", 39: "density", 40: "breach", 41: "lunge",
}

// javaEnchantment is the Java minecraft:enchantment id of a Bedrock enchantment id (-1: none).
var javaEnchantment = sync.OnceValue(func() []int32 {
	t := make([]int32, len(bedrockEnchantments))
	for id, name := range bedrockEnchantments {
		t[id] = v777.RegistryID("minecraft:enchantment", "minecraft:"+name)
	}
	return t
})

// bedrockEnchantmentOf is the Bedrock enchantment id of a Java enchantment id (-1: none).
func bedrockEnchantmentOf(java int32) int {
	for b, j := range javaEnchantment() {
		if j == java && j >= 0 {
			return b
		}
	}
	return -1
}

// bedrockPotions are the Java potions of Bedrock potion ids (-1: none, handled apart).
var bedrockPotions = [...]int32{
	jitem.PotionWater, jitem.PotionMundane, -1, jitem.PotionThick, jitem.PotionAwkward,
	jitem.PotionNightVision, jitem.PotionLongNightVision, jitem.PotionInvisibility, jitem.PotionLongInvisibility,
	jitem.PotionLeaping, jitem.PotionLongLeaping, jitem.PotionStrongLeaping,
	jitem.PotionFireResistance, jitem.PotionLongFireResistance,
	jitem.PotionSwiftness, jitem.PotionLongSwiftness, jitem.PotionStrongSwiftness,
	jitem.PotionSlowness, jitem.PotionLongSlowness,
	jitem.PotionWaterBreathing, jitem.PotionLongWaterBreathing,
	jitem.PotionHealing, jitem.PotionStrongHealing, jitem.PotionHarming, jitem.PotionStrongHarming,
	jitem.PotionPoison, jitem.PotionLongPoison, jitem.PotionStrongPoison,
	jitem.PotionRegeneration, jitem.PotionLongRegeneration, jitem.PotionStrongRegeneration,
	jitem.PotionStrength, jitem.PotionLongStrength, jitem.PotionStrongStrength,
	jitem.PotionWeakness, jitem.PotionLongWeakness,
	-1, // 36 decay
	jitem.PotionTurtleMaster, jitem.PotionLongTurtleMaster, jitem.PotionStrongTurtleMaster,
	jitem.PotionSlowFalling, jitem.PotionLongSlowFalling, jitem.PotionStrongSlowness,
	jitem.PotionWindCharged, jitem.PotionWeaving, jitem.PotionOozing, jitem.PotionInfested,
}

func setPotion(js *jitem.Stack, id int) {
	pc := &js.PotionContents
	switch {
	case id >= 0 && id < len(bedrockPotions) && bedrockPotions[id] >= 0:
		pc.HasPotion, pc.Potion = true, bedrockPotions[id]
	case id == 2:
		pc.HasPotion, pc.Potion = true, jitem.PotionMundane
	case id == 36:
		pc.HasCustomColor, pc.CustomColor = true, 0x736156
		pc.Effects = append(pc.Effects, jitem.Effect{ID: jitem.EffectWither, Amplifier: 1, Duration: 800, ShowParticles: true, ShowIcon: true})
	default:
		pc.HasPotion, pc.Potion = true, jitem.PotionWater
	}
	js.Add(jitem.CompPotionContents)
}

// Most a Java client reads: past them it drops the connection on the packet.
const (
	javaMaxCount        = 99
	javaMaxEnchantLevel = 255
)

// javaStack converts a server item stack to the Java stack shown for it, reusing js's slices.
func (p *Player) javaStack(st protocol.ItemStack, js *jitem.Stack) {
	js.Reset()
	if st.NetworkID == 0 || st.Count == 0 {
		return
	}
	name := p.items().names[st.NetworkID]
	meta := int16(st.MetadataValue)
	js.Count = min(int32(st.Count), javaMaxCount)
	id, ok := javamap.ItemID(name, meta)
	if !ok {
		// A custom item (or one javamap does not know): paper named after it.
		js.ID = javaPaper
		js.ItemName = jitem.Text{Text: prettyName(name)}
		js.Add(jitem.CompItemName)
		p.packItem(name, js) // packs.go: the server pack's name and model
		if c, ok := p.items().custom[st.NetworkID]; ok {
			c.apply(js) // customitems.go: durability, stack size, food, wearing
		}
	} else {
		js.ID = id
	}
	switch name {
	case "minecraft:potion", "minecraft:splash_potion", "minecraft:lingering_potion":
		setPotion(js, int(meta))
	case "minecraft:arrow":
		if meta > 5 { // Bedrock tipped arrows: potion id + 1
			js.ID = javaTippedArrow
			setPotion(js, int(meta)-1)
		}
	}
	nbt := st.NBTData
	if len(nbt) == 0 {
		return
	}
	if dmg, ok := nbtNum(nbt["Damage"]); ok && dmg > 0 {
		md := jitem.DefaultMaxDamage(js.ID)
		if js.Has(jitem.CompMaxDamage) {
			md = js.MaxDamage // a custom item's own durability (above)
		}
		if md <= 0 || int32(dmg) >= md {
			js.MaxDamage = max(md, int32(dmg)+1)
			js.Add(jitem.CompMaxDamage)
			// Damageable items must not stack on Java (the client refuses the item otherwise).
			js.MaxStackSize = 1
			js.Add(jitem.CompMaxStackSize)
		}
		js.Damage = int32(dmg)
		js.Add(jitem.CompDamage)
	}
	if u, ok := nbtNum(nbt["Unbreakable"]); ok && u != 0 {
		js.Add(jitem.CompUnbreakable)
	}
	if c, ok := nbtNum(nbt["customColor"]); ok {
		js.DyedColor = int32(c) & 0xffffff
		js.Add(jitem.CompDyedColor)
	}
	if t, ok := nbt["Trim"].(map[string]any); ok {
		// Armour trims (smithing table): minecraft:trim with registry holders (id + 1).
		mat, _ := t["Material"].(string)
		pat, _ := t["Pattern"].(string)
		mi, pi := v777.RegistryID("minecraft:trim_material", "minecraft:"+mat), v777.RegistryID("minecraft:trim_pattern", "minecraft:"+pat)
		if mi >= 0 && pi >= 0 {
			var w wire.Writer
			w.VarInt(mi + 1)
			w.VarInt(pi + 1)
			js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompTrim, Data: w.B})
		}
	}
	if layers, ok := nbt["Patterns"].([]any); ok && len(layers) > 0 && name == "minecraft:banner" {
		// Banner layers (loom): minecraft:banner_patterns, Bedrock colours (0 black) as Java dye ids.
		var w wire.Writer
		n := 0
		var body wire.Writer
		for _, l := range layers {
			m, _ := l.(map[string]any)
			code, _ := m["Pattern"].(string)
			col, _ := nbtNum(m["Color"])
			id := v777.RegistryID("minecraft:banner_pattern", "minecraft:"+javaBannerPatterns[code])
			if id < 0 || col < 0 || col > 15 {
				continue
			}
			body.VarInt(id + 1)
			body.VarInt(int32(15 - col))
			n++
		}
		if n > 0 {
			w.VarInt(int32(n))
			w.Raw(body.B)
			js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompBannerPatterns, Data: w.B})
		}
	}
	bookContent(js, nbt) // books.go
	if rc, ok := nbtNum(nbt["RepairCost"]); ok && rc > 0 {
		js.RepairCost = int32(rc)
		js.Add(jitem.CompRepairCost)
	}
	if d, ok := nbt["display"].(map[string]any); ok {
		if n, ok := d["Name"].(string); ok && n != "" {
			js.CustomName = itemText(n, true)
			js.Add(jitem.CompCustomName)
		}
		if lore, ok := d["Lore"].([]any); ok && len(lore) > 0 {
			for _, l := range lore {
				if s, ok := l.(string); ok {
					js.Lore = append(js.Lore, itemText(s, false))
				}
			}
			if len(js.Lore) > 0 {
				js.Add(jitem.CompLore)
			}
		}
	}
	if es, ok := nbt["ench"].([]any); ok && len(es) > 0 {
		dst, comp := &js.Enchantments, jitem.CompEnchantments
		if js.ID == javaEnchantedBook {
			dst, comp = &js.StoredEnchantments, jitem.CompStoredEnchantments
		}
		t := javaEnchantment()
		for _, e := range es {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			eid, _ := nbtNum(m["id"])
			lvl, _ := nbtNum(m["lvl"])
			if eid < 0 || int(eid) >= len(t) || t[eid] < 0 || lvl <= 0 {
				continue
			}
			*dst = append(*dst, jitem.Enchantment{ID: t[eid], Level: min(int32(lvl), javaMaxEnchantLevel)})
		}
		if len(*dst) > 0 {
			js.Add(comp)
		} else if len(es) > 0 && js.ID != javaEnchantedBook {
			js.EnchantmentGlintOverride = true // enchantments Java has not: still shiny
			js.Add(jitem.CompEnchantmentGlintOverride)
		}
	}
}

// itemText is an item's name or lore line for a Java client: Bedrock's § codes as a component.
// Names are not italic on Bedrock, so they are not on Java either.
func itemText(s string, name bool) jitem.Text {
	if !name && !textcolour.Has(s) && !strings.Contains(s, "§") {
		return jitem.Text{Text: s}
	}
	c := text.Legacy(s, text.Bedrock)
	if name {
		c.Italic = text.Off
	}
	return jitem.Text{Text: s, NBT: c.Bytes()}
}

// nbtNum reads a number of any NBT integer type.
func nbtNum(v any) (int64, bool) {
	switch v := v.(type) {
	case int8:
		return int64(v), true
	case uint8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float32:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}

// prettyName makes "ns:some_item" into "Some Item".
func prettyName(id string) string {
	if _, after, ok := strings.Cut(id, ":"); ok {
		id = after
	}
	words := strings.Split(id, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// javaID is the Java item id of a server stack (0: empty).
func (p *Player) javaID(st protocol.ItemStack) int32 {
	if st.NetworkID == 0 || st.Count == 0 {
		return 0
	}
	name := p.items().names[st.NetworkID]
	if name == "minecraft:arrow" && st.MetadataValue > 5 {
		return javaTippedArrow
	}
	id, ok := javamap.ItemID(name, int16(st.MetadataValue))
	if !ok {
		return javaPaper
	}
	return id
}

// writeStack writes a server stack as a Java slot for the client's version.
func (p *Player) writeStack(w *wire.Writer, st protocol.ItemStack) {
	if st.NetworkID == 0 || st.Count == 0 {
		jitem.WriteEmpty(w)
		return
	}
	js := &p.win.scratch
	p.javaStack(st, js)
	encodeStack(w, js, p.ver()) // items_raw.go
}

// describeStack is a short text of a server stack, for logs.
func (p *Player) describeStack(st protocol.ItemStack) string {
	if st.NetworkID == 0 || st.Count == 0 {
		return "-"
	}
	return fmt.Sprintf("%s:%d x%d", strings.TrimPrefix(p.items().names[st.NetworkID], "minecraft:"), st.MetadataValue, st.Count)
}
