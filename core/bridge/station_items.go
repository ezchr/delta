package bridge

import (
	"reflect"
	"strings"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Item rules the work stations need to work out their results the way a Bedrock client does
// before it asks the server: durability, enchantments (what takes them, what excludes what, their
// anvil cost), repair materials, anvil prior-work cost, custom names. Stacks are the server's
// (Bedrock NBT): Damage, RepairCost, display.Name, ench [{id, lvl}].

// enchantInfo is one Bedrock enchantment's anvil data.
type enchantInfo struct {
	max   int
	cost  int    // anvil cost per level on an item (halved for books)
	group int    // enchantments of one group (> 0) exclude each other
	on    string // item kinds that take it ('|' separated; "*" any damageable)
	curse bool
}

// Exclusive groups.
const (
	grpNone = iota
	grpProtection
	grpDamage
	grpBoots
	grpMining
	grpBow
	grpTrident
	grpCrossbow
)

var enchantData = [...]enchantInfo{
	0:  {4, 1, grpProtection, "armour", false},           // protection
	1:  {4, 2, grpProtection, "armour", false},           // fire protection
	2:  {4, 2, 0, "boots", false},                        // feather falling
	3:  {4, 4, grpProtection, "armour", false},           // blast protection
	4:  {4, 2, grpProtection, "armour", false},           // projectile protection
	5:  {3, 8, 0, "armour", false},                       // thorns
	6:  {3, 4, 0, "helmet", false},                       // respiration
	7:  {3, 4, grpBoots, "boots", false},                 // depth strider
	8:  {1, 4, 0, "helmet", false},                       // aqua affinity
	9:  {5, 1, grpDamage, "sword|axe|spear", false},      // sharpness
	10: {5, 2, grpDamage, "sword|axe|mace|spear", false}, // smite
	11: {5, 2, grpDamage, "sword|axe|mace|spear", false}, // bane of arthropods
	12: {2, 2, 0, "sword|spear", false},                  // knockback
	13: {2, 4, 0, "sword|mace|spear", false},             // fire aspect
	14: {3, 4, 0, "sword|spear", false},                  // looting
	15: {5, 1, 0, "tool|shears", false},                  // efficiency
	16: {1, 8, grpMining, "tool", false},                 // silk touch
	17: {3, 2, 0, "*", false},                            // unbreaking
	18: {3, 4, grpMining, "tool", false},                 // fortune
	19: {5, 1, 0, "bow", false},                          // power
	20: {2, 4, 0, "bow", false},                          // punch
	21: {1, 4, 0, "bow", false},                          // flame
	22: {1, 8, grpBow, "bow", false},                     // infinity
	23: {3, 4, 0, "fishing_rod", false},                  // luck of the sea
	24: {3, 4, 0, "fishing_rod", false},                  // lure
	25: {2, 4, grpBoots, "boots", false},                 // frost walker
	26: {1, 4, grpBow, "*", false},                       // mending
	27: {1, 8, 0, "armour|elytra|head", true},            // curse of binding
	28: {1, 8, 0, "*|head|compass", true},                // curse of vanishing
	29: {5, 4, 0, "trident", false},                      // impaling
	30: {3, 4, grpTrident, "trident", false},             // riptide
	31: {3, 1, grpTrident, "trident", false},             // loyalty
	32: {1, 8, grpTrident, "trident", false},             // channeling
	33: {1, 4, grpCrossbow, "crossbow", false},           // multishot
	34: {4, 1, grpCrossbow, "crossbow", false},           // piercing
	35: {3, 2, 0, "crossbow", false},                     // quick charge
	36: {3, 8, 0, "boots", false},                        // soul speed
	37: {3, 8, 0, "leggings", false},                     // swift sneak
	38: {3, 4, 0, "mace", false},                         // wind burst
	39: {5, 2, grpDamage, "mace", false},                 // density
	40: {4, 4, grpDamage, "mace", false},                 // breach
	41: {3, 2, 0, "spear", false},                        // lunge
}

// itemKind is what kind of item a server item name is, for enchantments.
func itemKind(name string) string {
	n := strings.TrimPrefix(name, "minecraft:")
	for _, s := range []string{"helmet", "chestplate", "leggings", "boots", "sword", "pickaxe", "shovel", "hoe", "spear"} {
		if strings.HasSuffix(n, "_"+s) {
			return s
		}
	}
	switch {
	case strings.HasSuffix(n, "_axe"):
		return "axe"
	case n == "bow", n == "crossbow", n == "trident", n == "fishing_rod", n == "shears", n == "mace", n == "elytra",
		n == "shield", n == "book", n == "enchanted_book", n == "flint_and_steel", n == "brush":
		return n
	case n == "carrot_on_a_stick", n == "warped_fungus_on_a_stick":
		return "stick"
	case n == "carved_pumpkin", n == "skull", strings.HasSuffix(n, "_head"), strings.HasSuffix(n, "_skull"):
		return "head"
	case n == "compass", n == "recovery_compass", n == "lodestone_compass":
		return "compass"
	}
	return ""
}

// enchantFits reports whether enchantment id goes on an item of this kind (books take any).
func enchantFits(id int, kind string, damageable bool) bool {
	if kind == "book" || kind == "enchanted_book" {
		return true
	}
	if id < 0 || id >= len(enchantData) {
		return false
	}
	for _, on := range strings.Split(enchantData[id].on, "|") {
		switch on {
		case "*":
			if damageable {
				return true
			}
		case "armour":
			if kind == "helmet" || kind == "chestplate" || kind == "leggings" || kind == "boots" {
				return true
			}
		case "tool":
			if kind == "pickaxe" || kind == "shovel" || kind == "axe" || kind == "hoe" {
				return true
			}
		default:
			if on == kind {
				return true
			}
		}
	}
	return false
}

func enchantsExclude(a, b int) bool {
	if a == b || a < 0 || b < 0 || a >= len(enchantData) || b >= len(enchantData) {
		return false
	}
	ga, gb := enchantData[a].group, enchantData[b].group
	return ga != grpNone && ga == gb
}

// ench is one enchantment of a stack.
type ench struct{ id, lvl int }

// stackEnchants are the enchantments of a server stack.
func stackEnchants(st protocol.ItemStack) []ench {
	es, _ := st.NBTData["ench"].([]any)
	out := make([]ench, 0, len(es))
	for _, e := range es {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := nbtNum(m["id"])
		lvl, _ := nbtNum(m["lvl"])
		out = append(out, ench{int(id), int(lvl)})
	}
	return out
}

// withNBT is a copy of st with an NBT key set (val nil: removed).
func withNBT(st protocol.ItemStack, key string, val any) protocol.ItemStack {
	nbt := copyNBT(st.NBTData)
	if val == nil {
		delete(nbt, key)
	} else {
		nbt[key] = val
	}
	if len(nbt) == 0 {
		nbt = nil
	}
	st.NBTData = nbt
	return st
}

func withEnchants(st protocol.ItemStack, es []ench) protocol.ItemStack {
	if len(es) == 0 {
		return withNBT(st, "ench", nil)
	}
	list := make([]any, len(es))
	for i, e := range es {
		list[i] = map[string]any{"id": int16(e.id), "lvl": int16(e.lvl)}
	}
	return withNBT(st, "ench", list)
}

func repairCostOf(st protocol.ItemStack) int {
	if st.Count == 0 {
		return 0
	}
	v, _ := nbtNum(st.NBTData["RepairCost"])
	return int(v)
}

func withRepairCost(st protocol.ItemStack, c int) protocol.ItemStack {
	if c <= 0 {
		return withNBT(st, "RepairCost", nil)
	}
	return withNBT(st, "RepairCost", int32(c))
}

func customNameOf(st protocol.ItemStack) string {
	d, _ := st.NBTData["display"].(map[string]any)
	n, _ := d["Name"].(string)
	return n
}

func withCustomName(st protocol.ItemStack, name string) protocol.ItemStack {
	d, _ := st.NBTData["display"].(map[string]any)
	d = copyNBT(d)
	if name == "" {
		delete(d, "Name")
	} else {
		d["Name"] = name
	}
	if len(d) == 0 {
		return withNBT(st, "display", nil)
	}
	return withNBT(st, "display", d)
}

func damageOf(st protocol.ItemStack) int {
	v, _ := nbtNum(st.NBTData["Damage"])
	return int(v)
}

func withDamage(st protocol.ItemStack, d int) protocol.ItemStack {
	if d <= 0 {
		return withNBT(st, "Damage", nil)
	}
	return withNBT(st, "Damage", int32(d))
}

// maxDamage is the durability of a server stack's item (0: not damageable).
func (p *Player) maxDamage(st protocol.ItemStack) int {
	if st.Count == 0 {
		return 0
	}
	if u, _ := nbtNum(st.NBTData["Unbreakable"]); u != 0 {
		return 0
	}
	return int(jitem.DefaultMaxDamage(p.javaID(st)))
}

// repairsWith reports whether material repairs the item in the anvil (its repair material).
func repairsWith(item, material string) bool {
	n := strings.TrimPrefix(item, "minecraft:")
	m := strings.TrimPrefix(material, "minecraft:")
	prefix := func(ps ...string) bool {
		for _, p := range ps {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
		return false
	}
	switch {
	case n == "elytra":
		return m == "phantom_membrane"
	case n == "turtle_helmet":
		return m == "turtle_scute" || m == "scute"
	case n == "mace":
		return m == "breeze_rod"
	case n == "wolf_armor":
		return m == "armadillo_scute"
	case n == "shield":
		return strings.HasSuffix(m, "_planks")
	case prefix("wooden_"):
		return strings.HasSuffix(m, "_planks")
	case prefix("stone_"):
		return m == "cobblestone" || m == "cobbled_deepslate" || m == "blackstone"
	case prefix("leather_"):
		return m == "leather"
	case prefix("chainmail_", "iron_"):
		return m == "iron_ingot"
	case prefix("copper_"):
		return m == "copper_ingot"
	case prefix("golden_"):
		return m == "gold_ingot"
	case prefix("diamond_"):
		return m == "diamond"
	case prefix("netherite_"):
		return m == "netherite_ingot"
	}
	return false
}

// javaDefaultName is the name a Java client puts in the anvil's field for an item without a
// custom name (its English name, from the item id).
func (p *Player) javaDefaultName(st protocol.ItemStack) string {
	return prettyName(jitem.Name(p.javaID(st)))
}

// anvilResult is what the anvil makes of input and material renamed to name (nil: the client did
// not name it), its level cost and how many of the material a repair uses. A cost of 40 or more
// without a result is "Too Expensive!".
func (p *Player) anvilResult(input, material protocol.ItemStack, name *string) (res protocol.ItemStack, cost, repairN int) {
	if input.Count == 0 || input.NetworkID == 0 {
		return protocol.ItemStack{}, 0, 0
	}
	hasMat := material.Count > 0 && material.NetworkID != 0
	res = input
	base := repairCostOf(input)
	if hasMat {
		base += repairCostOf(material)
	}
	inName, matName := p.stackName(input), p.stackName(material)
	maxD := p.maxDamage(input)
	action, renameCost := 0, 0
	if hasMat {
		if maxD > 0 && repairsWith(inName, matName) {
			d := min(damageOf(input), maxD/4)
			if d <= 0 {
				return protocol.ItemStack{}, 0, 0
			}
			dmg := damageOf(input)
			for ; d > 0 && repairN < int(material.Count); repairN++ {
				dmg -= d
				action++
				d = min(dmg, maxD/4)
			}
			res = withDamage(res, dmg)
		} else {
			book := matName == "minecraft:enchanted_book" && len(stackEnchants(material)) > 0
			if !book && (inName != matName || input.MetadataValue != material.MetadataValue || maxD <= 0) {
				return protocol.ItemStack{}, 0, 0
			}
			if maxD > 0 && !book {
				rem := (maxD - damageOf(input)) + (maxD - damageOf(material)) + maxD*12/100
				if nd := max(0, maxD-rem); nd < damageOf(input) {
					res = withDamage(res, nd)
					action += 2
				}
			}
			kind := itemKind(inName)
			have := stackEnchants(input)
			out := append([]ench(nil), have...)
			compatible, incompatible := false, false
			for _, e := range stackEnchants(material) {
				if e.id < 0 || e.id >= len(enchantData) {
					continue
				}
				ok := enchantFits(e.id, kind, maxD > 0)
				for _, o := range have {
					if enchantsExclude(e.id, o.id) {
						ok = false
						action++
					}
				}
				if !ok {
					incompatible = true
					continue
				}
				lvl, levelCost := e.lvl, e.lvl
				idx := -1
				for i, o := range out {
					if o.id == e.id {
						idx = i
					}
				}
				if idx >= 0 {
					cur := out[idx].lvl
					if cur > lvl || (cur == lvl && lvl >= enchantData[e.id].max) {
						incompatible = true
						continue
					}
					if cur == lvl {
						lvl++
					}
					levelCost = lvl - cur
					out[idx].lvl = lvl
				} else {
					out = append(out, ench{e.id, lvl})
				}
				compatible = true
				mult := enchantData[e.id].cost
				if book {
					mult = max(1, mult/2)
				}
				action += mult * levelCost
				if input.Count > 1 {
					action = 40
				}
			}
			if maxD <= 0 && incompatible && !compatible {
				return protocol.ItemStack{}, 0, 0
			}
			res = withEnchants(res, out)
		}
	}
	if name != nil {
		cur := customNameOf(input)
		switch want := *name; {
		case strings.TrimSpace(want) == "" || (cur == "" && want == p.javaDefaultName(input)):
			if cur != "" {
				renameCost = 1
				res = withCustomName(res, "")
			}
		case want != cur:
			renameCost = 1
			res = withCustomName(res, want)
		}
		action += renameCost
	}
	if action <= 0 {
		return protocol.ItemStack{}, 0, 0
	}
	cost = action + base
	if renameCost == action && cost >= 40 {
		cost = 39 // renaming alone is never too expensive
	}
	if cost >= 40 && p.self.mode != GameCreative {
		return protocol.ItemStack{}, cost, 0
	}
	rc := repairCostOf(input)
	if hasMat {
		rc = max(rc, repairCostOf(material))
	}
	if renameCost != action {
		rc = rc*2 + 1
	}
	return withRepairCost(res, rc), cost, repairN
}

// grindResult is what the grindstone makes: the enchantments but curses taken off, two items of
// a kind joined (with 5% durability on top), no prior-work cost.
func (p *Player) grindResult(first, second protocol.ItemStack) protocol.ItemStack {
	e1, e2 := first.Count > 0 && first.NetworkID != 0, second.Count > 0 && second.NetworkID != 0
	if !e1 && !e2 || first.Count > 1 || second.Count > 1 {
		return protocol.ItemStack{}
	}
	res := first
	if !e1 {
		res = second
	}
	es := stackEnchants(res)
	if e1 && e2 {
		if first.NetworkID != second.NetworkID || first.MetadataValue != second.MetadataValue {
			return protocol.ItemStack{}
		}
		maxD := p.maxDamage(first)
		if maxD <= 0 {
			return protocol.ItemStack{}
		}
		for _, e := range stackEnchants(second) {
			found := false
			for i, o := range es {
				if o.id == e.id {
					es[i].lvl = max(o.lvl, e.lvl)
					found = true
				}
			}
			if !found {
				es = append(es, e)
			}
		}
		rem := min((maxD-damageOf(first))+(maxD-damageOf(second))+maxD*5/100, maxD)
		res = withDamage(res, maxD-rem)
	} else if len(es) == 0 {
		return protocol.ItemStack{} // a single item without enchantments: nothing to take off
	}
	var kept []ench
	for _, e := range es {
		if e.id >= 0 && e.id < len(enchantData) && enchantData[e.id].curse {
			kept = append(kept, e)
		}
	}
	// BDS (CraftHandlerGrindstone): the prior-work cost becomes 0, 1 or 3 by the curses kept.
	if len(kept) > 2 {
		return protocol.ItemStack{}
	}
	res = withRepairCost(withEnchants(res, kept), []int{0, 1, 3}[len(kept)])
	if p.stackName(res) == "minecraft:enchanted_book" && len(kept) == 0 {
		if id, ok := p.items().ids["minecraft:book"]; ok {
			res = protocol.ItemStack{ItemType: protocol.ItemType{NetworkID: id}, Count: 1}
		}
	}
	res.Count = 1
	return res
}

// Trim materials and patterns by their items.
var trimMaterials = map[string]string{
	"iron_ingot": "iron", "copper_ingot": "copper", "gold_ingot": "gold", "lapis_lazuli": "lapis", "emerald": "emerald",
	"diamond": "diamond", "netherite_ingot": "netherite", "redstone": "redstone", "quartz": "quartz",
	"amethyst_shard": "amethyst", "resin_brick": "resin",
}

func trimPattern(template string) string {
	n := strings.TrimPrefix(template, "minecraft:")
	if p, ok := strings.CutSuffix(n, "_armor_trim_smithing_template"); ok {
		return p
	}
	return ""
}

// Banner patterns: the Bedrock codes of the Java patterns that need no item, in the order of the
// Java tag banner_pattern no_item_required (the loom's list and its button numbers), and the
// pattern items' own.
var loomNoItem = []string{
	"bl", "br", "tl", "tr", "bs", "ts", "ls", "rs", "cs", "ms", "drs", "dls", "ss", "cr", "sc",
	"bt", "tt", "bts", "tts", "ld", "rud", "lud", "rd", "mc", "mr", "vh", "hh", "vhr", "hhb",
	"bo", "gra", "gru",
}

var loomItemPatterns = map[string]string{
	"flower_banner_pattern": "flo", "creeper_banner_pattern": "cre", "skull_banner_pattern": "sku",
	"mojang_banner_pattern": "moj", "globe_banner_pattern": "glb", "piglin_banner_pattern": "pig",
	"flow_banner_pattern": "flw", "guster_banner_pattern": "gus", "field_masoned_banner_pattern": "bri",
	"bordure_indented_banner_pattern": "cbo",
}

// javaBannerPatterns are the Java banner_pattern names of the Bedrock codes.
var javaBannerPatterns = map[string]string{
	"b": "base", "bl": "square_bottom_left", "br": "square_bottom_right", "tl": "square_top_left", "tr": "square_top_right",
	"bs": "stripe_bottom", "ts": "stripe_top", "ls": "stripe_left", "rs": "stripe_right", "cs": "stripe_center",
	"ms": "stripe_middle", "drs": "stripe_downright", "dls": "stripe_downleft", "ss": "small_stripes", "cr": "cross",
	"sc": "straight_cross", "bt": "triangle_bottom", "tt": "triangle_top", "bts": "triangles_bottom", "tts": "triangles_top",
	"ld": "diagonal_left", "rd": "diagonal_up_right", "lud": "diagonal_up_left", "rud": "diagonal_right", "mc": "circle",
	"mr": "rhombus", "vh": "half_vertical", "hh": "half_horizontal", "vhr": "half_vertical_right",
	"hhb": "half_horizontal_bottom", "bo": "border", "cbo": "curly_border", "gra": "gradient", "gru": "gradient_up",
	"bri": "bricks", "glb": "globe", "cre": "creeper", "sku": "skull", "flo": "flower", "moj": "mojang", "pig": "piglin",
	"flw": "flow", "gus": "guster",
}

// loomPatterns is the list the Java loom shows for its pattern slot (Bedrock codes).
func (p *Player) loomPatterns(pattern protocol.ItemStack) []string {
	if pattern.Count > 0 && pattern.NetworkID != 0 {
		if c, ok := loomItemPatterns[strings.TrimPrefix(p.stackName(pattern), "minecraft:")]; ok {
			return []string{c}
		}
		return nil
	}
	return loomNoItem
}

// dyeColours are the Bedrock colour numbers of the dyes (banner NBT: 0 black ... 15 white).
var dyeColours = map[string]int32{
	"black_dye": 0, "ink_sac": 0, "red_dye": 1, "green_dye": 2, "brown_dye": 3, "cocoa_beans": 3, "blue_dye": 4,
	"lapis_lazuli": 4, "purple_dye": 5, "cyan_dye": 6, "light_gray_dye": 7, "gray_dye": 8, "pink_dye": 9, "lime_dye": 10,
	"yellow_dye": 11, "light_blue_dye": 12, "magenta_dye": 13, "orange_dye": 14, "white_dye": 15, "bone_meal": 15,
}

// loomResult is the banner the loom makes with pattern code pat.
func (p *Player) loomResult(banner, dye protocol.ItemStack, pat string) protocol.ItemStack {
	if banner.Count == 0 || dye.Count == 0 || pat == "" || p.stackName(banner) != "minecraft:banner" {
		return protocol.ItemStack{}
	}
	col, ok := dyeColours[strings.TrimPrefix(p.stackName(dye), "minecraft:")]
	if !ok {
		return protocol.ItemStack{}
	}
	layers, _ := banner.NBTData["Patterns"].([]any)
	if len(layers) >= 6 {
		return protocol.ItemStack{}
	}
	layers = append(append([]any(nil), layers...), map[string]any{"Color": col, "Pattern": pat})
	res := withNBT(banner, "Patterns", layers)
	res.Count = 1
	return res
}

// cartographyResult is what the cartography table makes: a copy (an empty map), a bigger map
// (paper) or a locked map (a glass pane), shown as the same map (the server makes the real one),
// and the multi recipe it is.
func (p *Player) cartographyResult(mapItem, extra protocol.ItemStack) (protocol.ItemStack, string) {
	if mapItem.Count == 0 || extra.Count == 0 || p.stackName(mapItem) != "minecraft:filled_map" {
		return protocol.ItemStack{}, ""
	}
	res := mapItem
	switch p.stackName(extra) {
	case "minecraft:empty_map", "minecraft:map":
		res.Count = 2
		return res, "85939755-ba10-4d9d-a4cc-efb7a8e943c4"
	case "minecraft:paper":
		res.Count = 1
		return res, "d392b075-4ba1-40ae-8789-af868d56f6ce"
	case "minecraft:glass_pane":
		res.Count = 1
		return res, "602234e4-cac1-4353-8bb7-b1ebff70024b"
	}
	return protocol.ItemStack{}, ""
}

// stackable reports whether two server stacks are the same item and may stack (equal NBT).
func stackable(a, b protocol.ItemStack) bool {
	return a.NetworkID == b.NetworkID && a.MetadataValue == b.MetadataValue && reflect.DeepEqual(nbtOrNil(a.NBTData), nbtOrNil(b.NBTData))
}

func nbtOrNil(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}
