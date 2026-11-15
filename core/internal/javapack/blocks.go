package javapack

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Custom blocks are drawn on Java block states the Bedrock world never shows (the bridge sends
// the Java state, so it can repurpose them, as Geyser does the other way round):
//
//   - huge mushroom block side combinations javamap never produces (full cubes; the Java client
//     only changes them itself when the same mushroom block is placed next to them),
//   - powered note blocks (full cubes; Bedrock note blocks are never powered on Java; the client
//     recomputes the instrument when it places a block above or below one, so they come second),
//   - unpowered redstone wire connection combinations (no collision, cutout layer) for blocks
//     without a collision box (Bedrock trip_wire uses all of tripwire's states).
//
// The blockstate files of those blocks are replaced: the states the Bedrock block mapping uses
// keep their vanilla models (states it never sends need no look).

// Slot is one Java block state a custom block can be drawn on.
type Slot struct {
	Block string            // Java block ("minecraft:note_block")
	State map[string]string // all its properties
	Solid bool              // full collision (false: none)
}

// Key is the state's properties as a blockstate variant key ("a=1,b=2", by name).
func (s Slot) Key() string { return stateKey(s.State) }

func stateKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}

var (
	mushroomBlocks = []string{"minecraft:brown_mushroom_block", "minecraft:red_mushroom_block", "minecraft:mushroom_stem"}
	mushroomFaces  = []string{"down", "east", "north", "south", "up", "west"}
	noteInstrs     = strings.Split("harp,basedrum,snare,hat,bass,flute,bell,guitar,chime,xylophone,iron_xylophone,cow_bell,didgeridoo,bit,banjo,pling,trumpet,trumpet_exposed,trumpet_oxidized,trumpet_weathered,zombie,skeleton,creeper,dragon,wither_skeleton,piglin,custom_head", ",")
)

func bools(n int, f func(v []bool)) {
	v := make([]bool, n)
	for i := 0; i < 1<<n; i++ {
		for k := 0; k < n; k++ {
			v[k] = i&(1<<(n-1-k)) == 0 // true first
		}
		f(v)
	}
}

// Candidates lists every Java state the converter knows how to repurpose, in order of
// preference. The bridge drops the ones its block mapping uses and passes the rest as
// Input.Slots.
func Candidates() []Slot {
	var out []Slot
	for _, b := range mushroomBlocks {
		bools(6, func(v []bool) {
			st := map[string]string{}
			for i, f := range mushroomFaces {
				st[f] = fmt.Sprint(v[i])
			}
			out = append(out, Slot{Block: b, State: st, Solid: true})
		})
	}
	for _, in := range noteInstrs {
		for n := 0; n <= 24; n++ {
			out = append(out, Slot{Block: "minecraft:note_block", State: map[string]string{
				"instrument": in, "note": fmt.Sprint(n), "powered": "true"}, Solid: true})
		}
	}
	dirs := []string{"none", "side", "up"}
	for _, e := range dirs {
		for _, n := range dirs {
			for _, so := range dirs {
				for _, w := range dirs {
					out = append(out, Slot{Block: "minecraft:redstone_wire", State: map[string]string{
						"east": e, "north": n, "south": so, "west": w, "power": "0"}, Solid: false})
				}
			}
		}
	}
	return out
}

// blocks gives each custom block a free slot and a model.
func (b *builder) blocks() {
	used := make([]bool, len(b.in.Slots))
	done := map[string]bool{}
	list := append([]Block(nil), b.in.Blocks...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, bl := range list {
		if done[bl.Name] {
			continue
		}
		done[bl.Name] = true
		comps, _ := bl.Props["components"].(map[string]any)
		if _, ok := b.res.ItemNames[bl.Name]; !ok {
			if n := b.blockName(bl.Name, comps); n != "" {
				b.res.ItemNames[bl.Name] = n
			}
		}
		solid := collisionSolid(comps)
		slot := -1
		for i, s := range b.in.Slots {
			if !used[i] && s.Solid == solid {
				slot = i
				break
			}
		}
		if slot < 0 {
			b.res.Stats.miss("block %s: no free Java state", bl.Name)
			continue
		}
		model, ok := b.blockModel(bl.Name, comps)
		if !ok {
			continue
		}
		used[slot] = true
		s := b.in.Slots[slot]
		if b.blockStates[s.Block] == nil {
			b.blockStates[s.Block] = map[string]string{}
		}
		b.blockStates[s.Block][s.Key()] = model
		b.res.Blocks[bl.Name] = slot
		b.res.Stats.Blocks++
		// The block's item (same identifier) is drawn as the block.
		if _, ok := b.res.ItemModels[bl.Name]; !ok {
			safe := b.safeName("item", bl.Name)
			b.json("assets/"+Namespace+"/items/"+safe+".json", map[string]any{
				"model": map[string]any{"type": "minecraft:model", "model": model},
			})
			b.res.ItemModels[bl.Name] = Namespace + ":" + safe
		}
	}
	for blk, models := range b.blockStates {
		name := strings.TrimPrefix(blk, "minecraft:")
		var def any
		switch {
		case blk == "minecraft:note_block":
			def = noteBlockState(models)
		case blk == "minecraft:redstone_wire":
			def = b.redstoneState(models)
		default:
			def = mushroomState(name, models)
		}
		b.json("assets/minecraft/blockstates/"+name+".json", def)
	}
}

// collisionSolid reports whether a custom block has a collision box (Bedrock default: a full one).
func collisionSolid(comps map[string]any) bool {
	v, ok := comps["minecraft:collision_box"]
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case map[string]any:
		if e, ok := t["enabled"]; ok && !truthy(e) {
			return false
		}
		if sz, ok := t["size"].([]any); ok && len(sz) == 3 {
			vol := 1.0
			for _, x := range sz {
				vol *= num(x)
			}
			return vol > 0
		}
		return true
	}
	return truthy(v)
}

func num(v any) float64 {
	switch t := v.(type) {
	case float32:
		return float64(t)
	case float64:
		return t
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case uint8:
		return float64(t)
	case int16:
		return float64(t)
	}
	return 0
}

// blockModel writes a custom block's model and returns its id.
func (b *builder) blockModel(name string, comps map[string]any) (string, bool) {
	mi, _ := comps["minecraft:material_instances"].(map[string]any)
	mats, _ := mi["materials"].(map[string]any)
	if mats == nil {
		mats = mi
	}
	if len(mats) == 0 {
		// No material_instances: the resource pack's blocks.json names the textures.
		if faces, ok := b.blocksTex[name]; ok {
			mats = map[string]any{}
			for f, t := range faces {
				mats[f] = map[string]any{"texture": t}
			}
		}
	}
	texFor := func(f string) (texInfo, bool) {
		m, _ := mats[f].(map[string]any)
		if m == nil {
			m, _ = mats["*"].(map[string]any)
		}
		short, _ := m["texture"].(string)
		if short == "" {
			return texInfo{}, false
		}
		p, ok := b.terrainTex[short]
		if !ok {
			p = short
		}
		return b.texture("block", p)
	}
	geoID := ""
	switch g := comps["minecraft:geometry"].(type) {
	case string:
		geoID = g
	case map[string]any:
		geoID, _ = g["identifier"].(string)
		if geoID == "" {
			geoID, _ = g["value"].(string)
		}
	}
	safe := b.safeName("block", name)
	id := Namespace + ":block/" + safe
	file := "assets/" + Namespace + "/models/block/" + safe + ".json"
	if geoID == "" || strings.HasPrefix(geoID, "minecraft:geometry.full_block") {
		textures := map[string]any{}
		have := false
		for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
			t, ok := texFor(f)
			if !ok {
				continue
			}
			textures[f] = t.id
			have = true
		}
		if !have {
			b.res.Stats.miss("block %s: no texture", name)
			return "", false
		}
		for _, f := range []string{"up", "north", "down", "south", "west", "east"} {
			if t, ok := textures[f]; ok {
				textures["particle"] = t
				break
			}
		}
		for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
			if _, ok := textures[f]; !ok {
				textures[f] = textures["particle"]
			}
		}
		b.json(file, map[string]any{"parent": "minecraft:block/cube", "textures": textures})
		return id, true
	}
	g := b.geos[geoID]
	if g == nil {
		b.res.Stats.miss("block %s: geometry %q not found", name, geoID)
		return "", false
	}
	tex, ok := texFor("*")
	if !ok {
		b.res.Stats.miss("block %s: no texture", name)
		return "", false
	}
	els := g.elements()
	fit(els, false)
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(tex.size.X), float64(tex.size.Y)
	}
	b.json(file, modelJSON(els, tex.id, tw, th))
	return id, true
}

func noteBlockState(models map[string]string) any {
	v := map[string]any{"powered=false": map[string]any{"model": "minecraft:block/note_block"}}
	for _, in := range noteInstrs {
		for n := 0; n <= 24; n++ {
			k := stateKey(map[string]string{"instrument": in, "note": fmt.Sprint(n), "powered": "true"})
			m := "minecraft:block/note_block"
			if c, ok := models[k]; ok {
				m = c
			}
			v[k] = map[string]any{"model": m}
		}
	}
	return map[string]any{"variants": v}
}

// redstoneVanilla is vanilla 26.3's redstone_wire blockstate (multipart).
const redstoneVanilla = `{"multipart":[{"apply":{"model":"minecraft:block/redstone_dust_dot"},"when":{"OR":[{"east":"none","north":"none","south":"none","west":"none"},{"east":"side|up","north":"side|up"},{"east":"side|up","south":"side|up"},{"south":"side|up","west":"side|up"},{"north":"side|up","west":"side|up"}]}},{"apply":{"model":"minecraft:block/redstone_dust_side0"},"when":{"north":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side_alt0"},"when":{"south":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side_alt1","y":270},"when":{"east":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side1","y":270},"when":{"west":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_up"},"when":{"north":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":90},"when":{"east":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":180},"when":{"south":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":270},"when":{"west":"up"}}]}`

// redstoneState is vanilla's redstone wire with its parts limited to powered wire and the
// unpowered states the block mapping uses (the ones not offered as slots), plus a part per custom
// block state.
func (b *builder) redstoneState(models map[string]string) any {
	free := map[string]bool{}
	for _, s := range b.in.Slots {
		if s.Block == "minecraft:redstone_wire" {
			free[s.Key()] = true
		}
	}
	keep := []any{map[string]any{"power": "1|2|3|4|5|6|7|8|9|10|11|12|13|14|15"}}
	for _, c := range Candidates() {
		if c.Block == "minecraft:redstone_wire" && !free[c.Key()] {
			keep = append(keep, c.State)
		}
	}
	var def struct {
		Multipart []map[string]any `json:"multipart"`
	}
	_ = json.Unmarshal([]byte(redstoneVanilla), &def)
	var parts []any
	for _, p := range def.Multipart {
		p["when"] = map[string]any{"AND": []any{p["when"], map[string]any{"OR": keep}}}
		parts = append(parts, p)
	}
	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, map[string]any{"when": parseKey(k), "apply": map[string]any{"model": models[k]}})
	}
	return map[string]any{"multipart": parts}
}

func parseKey(k string) map[string]string {
	st := map[string]string{}
	for _, kv := range strings.Split(k, ",") {
		p, v, _ := strings.Cut(kv, "=")
		st[p] = v
	}
	return st
}

// mushroomState is a huge mushroom block's multipart with vanilla's face parts limited to the
// states not drawn as custom blocks, and one part per custom block state.
func mushroomState(name string, models map[string]string) any {
	type face struct {
		prop string
		x, y int
	}
	faces := []face{{"north", 0, 0}, {"east", 0, 90}, {"south", 0, 180}, {"west", 0, 270}, {"up", 270, 0}, {"down", 90, 0}}
	var vanilla []map[string]string
	bools(6, func(v []bool) {
		st := map[string]string{}
		for i, f := range mushroomFaces {
			st[f] = fmt.Sprint(v[i])
		}
		if _, custom := models[stateKey(st)]; !custom {
			vanilla = append(vanilla, st)
		}
	})
	var parts []any
	for _, val := range []string{"true", "false"} {
		for _, f := range faces {
			var or []any
			for _, st := range vanilla {
				if st[f.prop] == val {
					or = append(or, st)
				}
			}
			if len(or) == 0 {
				continue
			}
			apply := map[string]any{"model": "minecraft:block/" + name}
			if val == "false" {
				apply["model"] = "minecraft:block/mushroom_block_inside"
			} else if f.prop != "north" {
				apply["uvlock"] = true
			}
			if f.x != 0 {
				apply["x"] = f.x
			}
			if f.y != 0 {
				apply["y"] = f.y
			}
			parts = append(parts, map[string]any{"when": map[string]any{"OR": or}, "apply": apply})
		}
	}
	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, map[string]any{"when": parseKey(k), "apply": map[string]any{"model": models[k]}})
	}
	return map[string]any{"multipart": parts}
}

// blockName is a custom block's name: its display_name (a lang key or text), else tile.<name>.name.
func (b *builder) blockName(name string, comps map[string]any) string {
	v := ""
	switch dn := comps["minecraft:display_name"].(type) {
	case string:
		v = dn
	case map[string]any:
		v, _ = dn["value"].(string)
	}
	if v != "" {
		if t, ok := b.lang[v]; ok {
			return t
		}
		if !strings.Contains(v, ".") || strings.Contains(v, " ") {
			return v
		}
	}
	if t, ok := b.lang["tile."+name+".name"]; ok {
		return t
	}
	return ""
}
