package packconv

import (
	"fmt"
	"image"
	"math"
	"path"
	"sort"
	"strings"
)

// itemDef is a custom item with everything known about it.
type itemDef struct {
	id     string
	comps  map[string]any // merged components (behaviour pack, then runtime data over it)
	source string
}

// gatherItems merges the runtime items, the behaviour packs' items, legacy resource pack items
// and the attachables of items nobody defined.
func (b *builder) gatherItems() []*itemDef {
	defs := map[string]*itemDef{}
	get := func(id, src string) *itemDef {
		d := defs[id]
		if d == nil {
			d = &itemDef{id: id, comps: map[string]any{}, source: src}
			defs[id] = d
		}
		return d
	}
	// Behaviour packs first: runtime data is laid over them.
	for _, s := range b.in.BehaviorPacks {
		if s.Encrypted {
			continue
		}
		for _, n := range s.Files("items/") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			var f struct {
				Item struct {
					Description struct {
						Identifier string `json:"identifier"`
					} `json:"description"`
					Components map[string]any `json:"components"`
				} `json:"minecraft:item"`
			}
			if !s.readJSON(n, &f) || f.Item.Description.Identifier == "" {
				continue
			}
			id := f.Item.Description.Identifier
			if strings.HasPrefix(id, "minecraft:") {
				continue
			}
			d := get(id, "behavior_pack")
			for k, v := range f.Item.Components {
				d.comps[k] = v
			}
		}
	}
	// Legacy resource pack items (format 1.10: items/*.json with minecraft:icon).
	for _, s := range b.in.Packs {
		if s.Encrypted {
			continue
		}
		for _, n := range s.Files("items/") {
			var f struct {
				Item struct {
					Description struct {
						Identifier string `json:"identifier"`
					} `json:"description"`
					Components map[string]any `json:"components"`
				} `json:"minecraft:item"`
			}
			if !strings.HasSuffix(n, ".json") || !s.readJSON(n, &f) || f.Item.Description.Identifier == "" {
				continue
			}
			id := f.Item.Description.Identifier
			if strings.HasPrefix(id, "minecraft:") {
				continue
			}
			d := get(id, "resource_pack")
			for k, v := range f.Item.Components {
				if _, ok := d.comps[k]; !ok {
					d.comps[k] = v
				}
			}
		}
	}
	for _, it := range b.in.Items {
		if it.ID == "" || strings.HasPrefix(it.ID, "minecraft:") {
			continue
		}
		d := get(it.ID, "identifier")
		if comps, ok := it.Data["components"].(map[string]any); ok {
			d.source = "runtime"
			for k, v := range comps {
				d.comps[k] = v
			}
		}
	}
	// Attachables of items nobody defined, when no definitions are known at all (a resource
	// pack converted on its own); with definitions, an attachable for an unknown id is unused.
	known := len(defs) > 0
	for _, id := range sortedKeys(b.attach) {
		if known {
			break
		}
		if strings.HasPrefix(id, "minecraft:") || defs[id] != nil {
			continue
		}
		get(id, "attachable")
	}
	out := make([]*itemDef, 0, len(defs))
	for _, id := range sortedKeys(defs) {
		out = append(out, defs[id])
	}
	return out
}

// itemFacts are the parts of an item's components that decide its Java form. Components are
// read in the behaviour pack form ("minecraft:max_stack_size": 1) and in the network form BDS
// sends in ItemRegistry ("item_properties": {"max_stack_size": 1, ...}).
type itemFacts struct {
	icon         string
	displayName  string
	maxStack     int
	maxDamage    int
	attackDamage float64
	hand         bool
	wearSlot     string // head, chest, legs, feet, offhand
	protection   float64
	food         *FoodMapping
	useAnim      string
	throwable    bool
	shooter      bool
	chargeOnDraw bool
	blockPlacer  string
	enchantSlot  string
	tags         []string
	digger       bool
	glint        bool
}

func (f *itemFacts) hasTag(t string) bool {
	for _, x := range f.tags {
		if x == t {
			return true
		}
	}
	return false
}

func readItemFacts(comps map[string]any) itemFacts {
	props, _ := comps["item_properties"].(map[string]any)
	get := func(names ...string) (any, bool) {
		for _, n := range names {
			if v, ok := comps[n]; ok {
				return v, true
			}
			if props != nil {
				if v, ok := props[n]; ok {
					return v, true
				}
				if v, ok := props[strings.TrimPrefix(n, "minecraft:")]; ok {
					return v, true
				}
			}
		}
		return nil, false
	}
	var f itemFacts
	if v, ok := get("minecraft:icon"); ok {
		f.icon = iconName(v)
	}
	if v, ok := get("minecraft:display_name"); ok {
		f.displayName = str(v)
	}
	if v, ok := get("minecraft:max_stack_size"); ok {
		f.maxStack = int(num(v))
		if m, ok := v.(map[string]any); ok && m["value"] == nil {
			f.maxStack = int(num(m["max_stack_size"]))
		}
	}
	if v, ok := get("minecraft:durability"); ok {
		m, _ := v.(map[string]any)
		f.maxDamage = int(num(m["max_durability"]))
		if f.maxDamage == 0 {
			f.maxDamage = int(num(m["max_durable"]))
		}
		if f.maxDamage == 0 {
			f.maxDamage = int(num(v))
		}
	}
	if v, ok := get("minecraft:damage"); ok {
		f.attackDamage = num(v)
	}
	if v, ok := get("minecraft:hand_equipped"); ok {
		f.hand = truthy(v)
	}
	if v, ok := get("minecraft:wearable"); ok {
		m, _ := v.(map[string]any)
		f.protection = num(m["protection"])
		slot := strings.ToLower(str(m["slot"]))
		if slot == "" {
			slot = strings.ToLower(fmt.Sprint(m["slot"]))
		}
		switch {
		case strings.Contains(slot, "armor.head"):
			f.wearSlot = "head"
		case strings.Contains(slot, "armor.chest"):
			f.wearSlot = "chest"
		case strings.Contains(slot, "armor.legs"):
			f.wearSlot = "legs"
		case strings.Contains(slot, "armor.feet"):
			f.wearSlot = "feet"
		case strings.Contains(slot, "offhand"):
			f.wearSlot = "offhand"
		}
	}
	if v, ok := get("minecraft:food"); ok {
		m, _ := v.(map[string]any)
		f.food = &FoodMapping{Nutrition: int(num(m["nutrition"])), CanAlwaysEat: truthy(m["can_always_eat"])}
		sat := num(m["saturation_modifier"])
		if s, ok := m["saturation_modifier"].(string); ok {
			sat = map[string]float64{"poor": 0.1, "low": 0.3, "normal": 0.6, "good": 0.8, "max": 1, "supernatural": 1.2}[s]
		}
		f.food.Saturation = r4(float64(f.food.Nutrition) * sat * 2)
		if f.food.Nutrition == 0 && sat == 0 {
			f.food.Saturation = 0
		}
	}
	if v, ok := get("minecraft:use_animation", "use_animation"); ok {
		switch t := v.(type) {
		case string:
			f.useAnim = t
		default:
			f.useAnim = map[int]string{1: "eat", 2: "drink"}[int(num(t))]
		}
		if m, ok := v.(map[string]any); ok {
			f.useAnim = str(m)
		}
	}
	if f.food != nil {
		secs := 1.6
		if v, ok := get("minecraft:use_modifiers"); ok {
			if m, ok := v.(map[string]any); ok && num(m["use_duration"]) > 0 {
				secs = num(m["use_duration"])
			}
		} else if v, ok := get("use_duration"); ok && num(v) > 0 {
			secs = num(v) / 20 // ticks in the old form
		}
		f.food.UseSeconds = r4(secs)
		f.food.Animation = "eat"
		if f.useAnim == "drink" {
			f.food.Animation = "drink"
		}
	}
	if _, ok := get("minecraft:throwable"); ok {
		f.throwable = true
	}
	if _, ok := get("minecraft:projectile"); ok {
		f.throwable = true
	}
	if v, ok := get("minecraft:shooter"); ok {
		f.shooter = true
		if m, ok := v.(map[string]any); ok {
			f.chargeOnDraw = truthy(m["charge_on_draw"])
		}
	}
	if v, ok := get("minecraft:block_placer"); ok {
		if m, ok := v.(map[string]any); ok {
			f.blockPlacer = str(m["block"])
			if bm, ok := m["block"].(map[string]any); ok {
				f.blockPlacer = str(bm["name"])
			}
		}
	}
	if v, ok := get("minecraft:enchantable"); ok {
		if m, ok := v.(map[string]any); ok {
			f.enchantSlot = str(m["slot"])
		}
	}
	if v, ok := get("enchantable_slot"); ok && f.enchantSlot == "" {
		f.enchantSlot = str(v)
	}
	if v, ok := get("minecraft:tags"); ok {
		if m, ok := v.(map[string]any); ok {
			if list, ok := m["tags"].([]any); ok {
				for _, t := range list {
					f.tags = append(f.tags, str(t))
				}
			}
		}
	}
	if v, ok := comps["item_tags"].([]any); ok {
		for _, t := range v {
			f.tags = append(f.tags, str(t))
		}
	}
	if _, ok := get("minecraft:digger"); ok {
		f.digger = true
	}
	if v, ok := get("minecraft:glint", "foil"); ok {
		f.glint = truthy(v)
	}
	return f
}

// iconName reads a minecraft:icon value: {"textures": {"default": x}}, {"texture": x} or x.
func iconName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["texture"].(string); ok {
			return s
		}
		if m, ok := t["textures"].(map[string]any); ok {
			if s, ok := m["default"].(string); ok {
				return s
			}
			for _, k := range sortedKeys(m) {
				if s, ok := m[k].(string); ok {
					return s
				}
			}
		}
		if s, ok := t["textures"].(string); ok {
			return s
		}
	}
	return ""
}

// itemKind classifies an item from its facts (and, as a last resort, its identifier).
func itemKind(id string, f *itemFacts) string {
	name := strings.ToLower(id[strings.Index(id, ":")+1:])
	switch f.wearSlot {
	case "head":
		return "helmet"
	case "chest":
		return "chestplate"
	case "legs":
		return "leggings"
	case "feet":
		return "boots"
	case "offhand":
		if strings.Contains(name, "shield") || f.hasTag("minecraft:is_shield") {
			return "shield"
		}
	}
	for _, k := range []string{"sword", "axe", "pickaxe", "shovel", "hoe", "spear"} {
		if f.hasTag("minecraft:is_"+k) || f.enchantSlot == k {
			return k
		}
	}
	switch f.enchantSlot {
	case "bow":
		return "bow"
	case "crossbow":
		return "crossbow"
	case "fishing_rod":
		return "fishing_rod"
	case "spear":
		return "spear"
	case "armor_head":
		return "helmet"
	case "armor_torso":
		return "chestplate"
	case "armor_legs":
		return "leggings"
	case "armor_feet":
		return "boots"
	}
	if f.shooter {
		if f.chargeOnDraw {
			return "crossbow"
		}
		return "bow"
	}
	if f.food != nil {
		if f.food.Animation == "drink" {
			return "drink"
		}
		return "food"
	}
	if f.throwable {
		return "throwable"
	}
	if f.blockPlacer != "" {
		return "block"
	}
	if f.hasTag("minecraft:is_tool") || f.digger || f.hand || f.attackDamage > 0 {
		// A tool or weapon of no known class: guess from the name.
		for _, k := range []struct{ suffix, kind string }{{"pickaxe", "pickaxe"}, {"_axe", "axe"}, {"shovel", "shovel"},
			{"_hoe", "hoe"}, {"spear", "spear"}} {
			if strings.Contains(name, k.suffix) {
				return k.kind
			}
		}
		if f.hand || f.attackDamage > 0 {
			return "sword"
		}
	}
	return "item"
}

// javaBase is the vanilla Java item a kind of custom item is sent as.
func javaBase(kind string) string {
	switch kind {
	case "sword", "axe", "pickaxe", "shovel", "hoe":
		return "minecraft:diamond_" + kind
	case "spear":
		return "minecraft:trident"
	case "helmet", "chestplate", "leggings", "boots":
		return "minecraft:diamond_" + kind
	case "shield":
		return "minecraft:shield"
	case "bow", "crossbow", "fishing_rod":
		return "minecraft:" + kind
	case "food":
		return "minecraft:bread"
	case "drink":
		return "minecraft:honey_bottle"
	case "throwable":
		return "minecraft:snowball"
	}
	// Plain items and block placers: paper, which the client neither places, eats nor uses.
	return "minecraft:paper"
}

// handheldKinds are drawn like vanilla tools (item/handheld) when flat.
var handheldKinds = map[string]bool{"sword": true, "axe": true, "pickaxe": true, "shovel": true, "hoe": true, "spear": true, "fishing_rod": true}

var armourSlots = map[string]string{"helmet": "head", "chestplate": "chest", "leggings": "legs", "boots": "feet"}

// items makes the item models of every custom item.
func (b *builder) items() {
	var armour []*armourPiece
	for _, d := range b.gatherItems() {
		if _, isBlock := b.m.Blocks[d.id]; isBlock {
			if d.comps["minecraft:icon"] == nil && d.comps["item_properties"] == nil {
				continue // the block's own item (blocks() drew it)
			}
		}
		f := readItemFacts(d.comps)
		a := b.attach[d.id]
		if f.wearSlot == "" && a != nil {
			// No definition says where it is worn: the game's armour geometry does.
			for _, s := range []string{"helmet", "chestplate", "leggings", "boots"} {
				if isArmourGeometry(a.geometry) && strings.HasSuffix(a.geometry, "."+s) {
					f.wearSlot = armourSlots[s]
				}
			}
		}
		kind := itemKind(d.id, &f)
		im := &ItemMapping{JavaBase: javaBase(kind), Kind: kind, Source: d.source, Model: "none", Glint: f.glint}
		if f.maxStack > 0 && f.maxStack != 64 {
			im.MaxStack = f.maxStack
		}
		im.MaxDamage = f.maxDamage
		if f.food != nil {
			im.Food = f.food
		}
		im.Name, im.NameKey = b.itemName(d.id, f.displayName)
		if im.Name != "" {
			b.res.ItemNames[d.id] = im.Name
		}
		var notes []string

		// The flat icon.
		var flat map[string]any
		var flatModel string
		icon := f.icon
		if icon == "" && d.source != "runtime" && d.source != "behavior_pack" {
			icon = b.guessIcon(d.id)
		}
		if icon != "" {
			p := icon
			if e, ok := b.itemTex[icon]; ok {
				p = e.path
				if e.variants > 1 {
					notes = append(notes, fmt.Sprintf("icon has %d variants: the first is used", e.variants))
				}
			}
			tex, ok := b.texture("item", p)
			if !ok {
				// A vanilla icon the packs do not ship: the Java client's own texture.
				vp, isVanilla := vanillaItemKeys()[icon]
				if !isVanilla && strings.HasPrefix(texKey(p), "textures/items/") {
					vp, isVanilla = texKey(p), true
				}
				if isVanilla {
					if java, jok := vanillaJava(vp); jok {
						tex, ok = texInfo{id: "minecraft:" + java, size: image.Pt(16, 16)}, true
						notes = append(notes, "icon is the vanilla texture minecraft:"+java)
					}
				}
			}
			if ok {
				parent := "minecraft:item/generated"
				if handheldKinds[kind] || (f.hand && kind == "item") {
					parent = "minecraft:item/handheld"
				}
				safe := b.safeName("model/item", d.id)
				b.json(b.assetPath("models", "item/"+safe+".json"), map[string]any{"parent": parent, "textures": map[string]any{"layer0": tex.id}})
				flatModel = b.ns + ":item/" + safe
				flat = map[string]any{"type": "minecraft:model", "model": flatModel}
				im.Model = "flat"
				if parent == "minecraft:item/handheld" {
					im.Model = "handheld"
				}
			} else {
				notes = append(notes, fmt.Sprintf("icon %q: texture %s not found", icon, p))
			}
		} else if d.source == "runtime" || d.source == "behavior_pack" {
			notes = append(notes, "no minecraft:icon")
		}
		if f.blockPlacer != "" {
			im.Block = f.blockPlacer
			if bm := b.m.Blocks[f.blockPlacer]; bm != nil && bm.ItemModel != "" && flat == nil {
				im.ItemModel, im.Model = bm.ItemModel, "block"
			}
		}

		// The model: 3D from the attachable, armour, or the flat icon.
		def := flat
		if a != nil {
			switch {
			case armourSlots[kind] != "" && (isArmourGeometry(a.geometry) || b.geos[a.geometry] != nil):
				armour = append(armour, &armourPiece{id: d.id, kind: kind, att: a, item: im})
				if kind == "helmet" && !isArmourGeometry(a.geometry) {
					// A 3D helmet: its model on the head, flat elsewhere.
					if ref, ok := b.headModel(d.id, a); ok {
						head := map[string]any{"type": "minecraft:model", "model": ref}
						if flat != nil {
							def = map[string]any{"type": "minecraft:select", "property": "minecraft:display_context",
								"cases": []any{map[string]any{"when": "head", "model": head}}, "fallback": flat}
						} else {
							def = head
						}
						im.Model = "head"
					}
				}
			case isArmourGeometry(a.geometry):
				notes = append(notes, "armour attachable on an item that is not worn: ignored")
			default:
				if ref, k, ok := b.heldModel(d.id, a, kind); ok {
					held := map[string]any{"type": "minecraft:model", "model": ref}
					if flat != nil {
						def = map[string]any{"type": "minecraft:select", "property": "minecraft:display_context",
							"cases": []any{map[string]any{"when": []any{"gui", "ground", "fixed", "on_shelf"}, "model": flat}}, "fallback": held}
					} else {
						def = held
					}
					im.Model = "3d"
					if k < 1 {
						notes = append(notes, fmt.Sprintf("3D model shrunk x%.3f to fit Java's box (display scale compensates)", k))
					}
					if len(a.animations) > 0 {
						notes = append(notes, "attachable animations not used: vanilla-like hand placement")
					}
				} else {
					notes = append(notes, fmt.Sprintf("attachable geometry %q not usable", a.geometry))
				}
			}
		}
		if def != nil {
			safe := b.safeName("item", d.id)
			b.json(b.assetPath("items", safe+".json"), map[string]any{"model": def})
			im.ItemModel = b.itemModelID(safe)
		}
		if slot := armourSlots[kind]; slot != "" {
			im.Equipment = &EquipmentMapping{Slot: slot}
		}
		if im.ItemModel != "" {
			b.res.ItemModels[d.id] = im.ItemModel
			b.res.Stats.Items++
		} else {
			b.res.Stats.miss("item %s: no model", d.id)
		}
		b.m.Items[d.id] = im
		st := Converted
		if im.ItemModel == "" {
			st = Skipped
			notes = append([]string{"no model: drawn as the plain " + im.JavaBase}, notes...)
		} else if len(notes) > 0 {
			st = Approximated
		}
		b.rep.add(catItem, d.id, st, fmt.Sprintf("%s on %s, %s model", kind, im.JavaBase, im.Model)+noteText(notes))
	}
	b.armour(armour)
}

func noteText(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "; " + strings.Join(notes, "; ")
}

// guessIcon finds an item_texture.json entry for an item known by identifier only.
func (b *builder) guessIcon(id string) string {
	ns, name, _ := strings.Cut(id, ":")
	for _, k := range []string{id, name, ns + "_" + name, ns + "." + name, strings.ReplaceAll(id, ":", ".")} {
		if _, ok := b.itemTex[k]; ok {
			return k
		}
	}
	for _, k := range []string{"textures/items/" + name, "textures/items/" + ns + "/" + name} {
		if _, _, ok := b.findTexture(k); ok {
			return k
		}
	}
	return ""
}

// itemName is a custom item's en_US name and its lang key: its display_name (a lang key or
// text), else the lang's item.<id>.name.
func (b *builder) itemName(id, display string) (string, string) {
	if display != "" {
		if t, ok := b.en(display); ok {
			return t, display
		}
		if t, ok := b.en(strings.TrimSuffix(display, ".name") + ".name"); ok {
			return t, strings.TrimSuffix(display, ".name") + ".name"
		}
		if !strings.HasPrefix(display, "item.") {
			return display, ""
		}
	}
	_, short, _ := strings.Cut(id, ":")
	for _, k := range []string{"item." + id + ".name", "item." + id, "item." + short + ".name"} {
		if t, ok := b.en(k); ok {
			return t, k
		}
	}
	return "", ""
}

// heldModel builds a 3D held-item model from an attachable's geometry, placed like a vanilla
// handheld sprite: the grip (the lower part of a "handle" bone, else of the whole model) on the
// sprite's grip point, the model turned so its front faces where vanilla tools point, shrunk
// when it does not fit Java's box (the display scale makes it up). It returns the model id and
// the shrink factor.
func (b *builder) heldModel(id string, a *attachable, kind string) (string, float64, bool) {
	g := b.geos[a.geometry]
	if g == nil {
		return "", 0, false
	}
	tex, ok := b.texture("item", a.texture)
	if !ok {
		return "", 0, false
	}
	// Root bones the game itself places (rightitem / item_root) keep their pivot but not their
	// rotation: Java's display transform places the item.
	ps := &pose{skip: map[string]bool{}}
	for _, bn := range g.bones {
		if n := strings.ToLower(bn.Name); n == "rightitem" || n == "item_root" || n == "leftitem" {
			ps.skip[n] = true
		}
	}
	cs := g.cubes(ps, nil)
	if len(cs) == 0 {
		return "", 0, false
	}
	var grip [3]float64
	found := false
	for _, c := range cs {
		if strings.Contains(c.bone, "handle") && isIdent(c.t.r) {
			grip = c.t.apply([3]float64{(c.lo[0] + c.hi[0]) / 2, c.lo[1] + 0.3*(c.hi[1]-c.lo[1]), (c.lo[2] + c.hi[2]) / 2})
			found = true
			break
		}
	}
	lo, hi := cubeBounds(cs)
	if !found {
		grip = [3]float64{(lo[0] + hi[0]) / 2, lo[1] + 0.15*(hi[1]-lo[1]), (lo[2] + hi[2]) / 2}
	}
	// Turned half a turn about Y (the front toward -x as vanilla sprites point), grip on the
	// handheld sprite's grip point.
	target := [3]float64{8, 8 - 5.6569, 8}
	r := rotY(180)
	var rlo, rhi [3]float64
	rlo = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	rhi = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for i := 0; i < 8; i++ {
		q := lo
		if i&1 != 0 {
			q[0] = hi[0]
		}
		if i&2 != 0 {
			q[1] = hi[1]
		}
		if i&4 != 0 {
			q[2] = hi[2]
		}
		p := r.apply([3]float64{q[0] - grip[0], q[1] - grip[1], q[2] - grip[2]})
		for a := 0; a < 3; a++ {
			rlo[a], rhi[a] = min(rlo[a], p[a]), max(rhi[a], p[a])
		}
	}
	k := fitScale(rlo, rhi, [3]float64{}, target, 0.5)
	rg := r.apply(grip)
	post := affine{r: r, t: [3]float64{target[0] - k*rg[0], target[1] - k*rg[1], target[2] - k*rg[2]}}
	els := place(cs, post, k)
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(tex.size.X), float64(tex.size.Y)
	}
	model := modelJSON(els, tex.id, tw, th)
	ds := func(s float64) []float64 { v := r4(min(4, s/k)); return []float64{v, v, v} }
	model["display"] = map[string]any{
		// Vanilla item/handheld's transforms with the sprite's 45 degree tilt taken out (the model
		// stands upright); left hands mirror the right.
		"thirdperson_righthand": map[string]any{"rotation": []float64{0, -90, 10}, "translation": []float64{0, 4, 0.5}, "scale": ds(0.85)},
		"thirdperson_lefthand":  map[string]any{"rotation": []float64{0, 90, -10}, "translation": []float64{0, 4, 0.5}, "scale": ds(0.85)},
		"firstperson_righthand": map[string]any{"rotation": []float64{0, -90, -20}, "translation": []float64{1.13, 3.2, 1.13}, "scale": ds(0.68)},
		"firstperson_lefthand":  map[string]any{"rotation": []float64{0, 90, 20}, "translation": []float64{1.13, 3.2, 1.13}, "scale": ds(0.68)},
		"head":                  map[string]any{"rotation": []float64{0, 0, 0}, "translation": []float64{0, 0, 0}, "scale": ds(1)},
		"gui":                   guiTransform(els, k),
		"ground":                map[string]any{"rotation": []float64{0, 0, 0}, "translation": []float64{0, 2, 0}, "scale": ds(0.5)},
		"fixed":                 map[string]any{"rotation": []float64{0, 180, 0}, "translation": []float64{0, 0, 0}, "scale": ds(1)},
	}
	safe := b.safeName("model/item", id+"_3d")
	b.json(b.assetPath("models", "item/"+safe+".json"), model)
	return b.ns + ":item/" + safe, k, true
}

// guiTransform fits a 3D model into an inventory slot, seen at the block angle.
func guiTransform(els []element, k float64) map[string]any {
	lo, hi := elementBounds(els)
	ext := max(hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2])
	s := 1.0
	if ext > 0 {
		s = min(4, 16/ext*0.9)
	}
	c := [3]float64{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	// The display translation is applied after the scale, in pixels/16 of a block... roughly:
	// move the model's centre onto the slot's centre.
	return map[string]any{"rotation": []float64{30, 225, 0},
		"translation": []float64{r4(-(c[0] - 8) * s * 0.7), r4(-(c[1] - 8) * s), r4(0)},
		"scale":       []float64{r4(s * 0.625), r4(s * 0.625), r4(s * 0.625)}}
}

// headModel builds a helmet's 3D model for the head display context: the head's centre
// (Bedrock (0, 28, 0)) on the model's (8, 8, 8), one Bedrock pixel per model pixel once the
// head display scale (1.6 x CustomHeadLayer's 0.625) is applied.
func (b *builder) headModel(id string, a *attachable) (string, bool) {
	g := b.geos[a.geometry]
	if g == nil {
		return "", false
	}
	tex, ok := b.texture("item", a.texture)
	if !ok {
		return "", false
	}
	cs := g.cubes(nil, nil)
	if len(cs) == 0 {
		return "", false
	}
	lo, hi := cubeBounds(cs)
	centre := [3]float64{0, 28, 0}
	k := fitScale(lo, hi, centre, [3]float64{8, 8, 8}, 0.5)
	post := affine{r: ident, t: [3]float64{8 - k*centre[0], 8 - k*centre[1], 8 - k*centre[2]}}
	els := place(cs, post, k)
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(tex.size.X), float64(tex.size.Y)
	}
	model := modelJSON(els, tex.id, tw, th)
	s := r4(min(4, 1.6/k))
	model["display"] = map[string]any{"head": map[string]any{"rotation": []float64{0, 0, 0}, "translation": []float64{0, 0, 0}, "scale": []float64{s, s, s}}}
	safe := b.safeName("model/item", id+"_head")
	b.json(b.assetPath("models", "item/"+safe+".json"), model)
	return b.ns + ":item/" + safe, true
}

// setName is an armour set's name: the item's name without its slot word.
func setName(id string) string {
	for _, w := range []string{"helmet", "chestplate", "chest", "leggings", "legs", "boots", "feet", "head", "cap", "tunic", "pants"} {
		for _, sep := range []string{"_", ":", "/", "."} {
			if c, ok := strings.CutSuffix(id, sep+w); ok {
				return c
			}
		}
		if c, ok := strings.CutPrefix(id[strings.Index(id, ":")+1:], w+"_"); ok { // helmet_ruby
			return id[:strings.Index(id, ":")+1] + c
		}
	}
	return id
}

var _ = path.Base
var _ = sort.Strings
