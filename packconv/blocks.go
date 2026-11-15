package packconv

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
)

// Custom blocks are drawn on Java block states the Bedrock world never shows (the translator
// sends the Java state, so it can repurpose them, as Geyser does the other way round):
//
//   - huge mushroom block side combinations a vanilla block mapping never produces (full cubes;
//     the Java client only changes them itself when the same mushroom block is placed next to
//     them),
//   - powered note blocks (full cubes; Bedrock note blocks are never powered on Java; the client
//     recomputes the instrument when it places a block above or below one, so they come second),
//   - unpowered redstone wire connection combinations (no collision, cutout layer) for blocks
//     without a collision box.
//
// The blockstate files of those blocks are replaced: the states the vanilla mapping uses keep
// their vanilla models. A block whose permutations look different (other textures, geometry or
// a minecraft:transformation rotation) gets a state per look.

// Slot is one Java block state a custom block can be drawn on.
type Slot struct {
	Block string            // Java block ("minecraft:note_block")
	State map[string]string // all its properties
	Solid bool              // full collision (false: none)
}

// Key is the state's properties as a blockstate variant key ("a=1,b=2", by name).
func (s Slot) Key() string { return stateKey(s.State) }

func stateKey(m map[string]string) string {
	keys := sortedKeys(m)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}

var (
	mushroomBlocks = []string{"minecraft:brown_mushroom_block", "minecraft:red_mushroom_block", "minecraft:mushroom_stem"}
	mushroomFaces  = []string{"down", "east", "north", "south", "up", "west"}
	// NoteInstruments are Java 26.x's note block instruments in enum order.
	NoteInstruments = strings.Split("harp,basedrum,snare,hat,bass,flute,bell,guitar,chime,xylophone,iron_xylophone,cow_bell,didgeridoo,bit,banjo,pling,trumpet,trumpet_exposed,trumpet_oxidized,trumpet_weathered,zombie,skeleton,creeper,dragon,wither_skeleton,piglin,custom_head", ",")
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
// preference. A caller drops the ones its block mapping uses and passes the rest as
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
	for _, in := range NoteInstruments {
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

// stateModel is what a repurposed state shows: a model, turned.
type stateModel struct {
	model string
	x, y  int
}

func (s stateModel) apply() map[string]any {
	m := map[string]any{"model": s.model}
	if s.x != 0 {
		m["x"] = s.x
	}
	if s.y != 0 {
		m["y"] = s.y
	}
	return m
}

// blockDef is a custom block with everything known about it.
type blockDef struct {
	name   string
	comps  map[string]any
	perms  []blockPerm
	states []blockState
	source string
}

type blockPerm struct {
	cond  string
	comps map[string]any
}

type blockState struct {
	name   string
	values []any
}

// gatherBlocks merges the runtime blocks, the behaviour packs' blocks and the custom blocks
// blocks.json alone defines.
func (b *builder) gatherBlocks() []*blockDef {
	defs := map[string]*blockDef{}
	for _, s := range b.in.BehaviorPacks {
		if s.Encrypted {
			continue
		}
		for _, n := range s.Files("blocks/") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			var f struct {
				Block struct {
					Description struct {
						Identifier string                     `json:"identifier"`
						States     map[string]json.RawMessage `json:"states"`
						Properties map[string]json.RawMessage `json:"properties"`
						Traits     map[string]json.RawMessage `json:"traits"`
					} `json:"description"`
					Components   map[string]any `json:"components"`
					Permutations []struct {
						Condition  string         `json:"condition"`
						Components map[string]any `json:"components"`
					} `json:"permutations"`
				} `json:"minecraft:block"`
			}
			if !s.readJSON(n, &f) || f.Block.Description.Identifier == "" {
				continue
			}
			d := &blockDef{name: f.Block.Description.Identifier, comps: f.Block.Components, source: "behavior_pack"}
			if d.comps == nil {
				d.comps = map[string]any{}
			}
			for _, p := range f.Block.Permutations {
				d.perms = append(d.perms, blockPerm{cond: p.Condition, comps: p.Components})
			}
			states := f.Block.Description.States
			if states == nil {
				states = f.Block.Description.Properties
			}
			for _, k := range sortedKeys(states) {
				d.states = append(d.states, blockState{name: k, values: stateValues(states[k])})
			}
			d.states = append(d.states, traitStates(f.Block.Description.Traits)...)
			defs[d.name] = d
		}
	}
	for _, bl := range b.in.Blocks {
		if bl.Name == "" || strings.HasPrefix(bl.Name, "minecraft:") {
			continue
		}
		d := &blockDef{name: bl.Name, comps: map[string]any{}, source: "runtime"}
		if old := defs[bl.Name]; old != nil {
			d.states = old.states
		}
		if c, ok := bl.Props["components"].(map[string]any); ok {
			d.comps = c
		}
		if ps, ok := bl.Props["permutations"].([]any); ok {
			for _, p := range ps {
				pm, _ := p.(map[string]any)
				cond, _ := pm["condition"].(string)
				c, _ := pm["components"].(map[string]any)
				d.perms = append(d.perms, blockPerm{cond: cond, comps: c})
			}
		}
		if props, ok := bl.Props["properties"].([]any); ok && len(props) > 0 {
			d.states = nil
			for _, p := range props {
				pm, _ := p.(map[string]any)
				name, _ := pm["name"].(string)
				vals, _ := pm["enum"].([]any)
				if name != "" && len(vals) > 0 {
					d.states = append(d.states, blockState{name: name, values: vals})
				}
			}
		}
		defs[bl.Name] = d
	}
	for id := range b.blocksJSON {
		if strings.HasPrefix(id, "minecraft:") || defs[id] != nil {
			continue
		}
		defs[id] = &blockDef{name: id, comps: map[string]any{}, source: "resource_pack"}
	}
	out := make([]*blockDef, 0, len(defs))
	for _, id := range sortedKeys(defs) {
		out = append(out, defs[id])
	}
	return out
}

// stateValues reads a block state definition: a list of values or {"values": {"min", "max"}}.
func stateValues(raw json.RawMessage) []any {
	var list []any
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var rng struct {
		Values struct {
			Min, Max int
		} `json:"values"`
	}
	if json.Unmarshal(raw, &rng) == nil && rng.Values.Max >= rng.Values.Min && rng.Values.Max-rng.Values.Min < 64 {
		for v := rng.Values.Min; v <= rng.Values.Max; v++ {
			list = append(list, float64(v))
		}
	}
	return list
}

// traitStates are the block states a block's traits add.
func traitStates(traits map[string]json.RawMessage) []blockState {
	var out []blockState
	for _, k := range sortedKeys(traits) {
		var t struct {
			Enabled []string `json:"enabled_states"`
		}
		_ = json.Unmarshal(traits[k], &t)
		for _, st := range t.Enabled {
			var vals []any
			switch st {
			case "minecraft:cardinal_direction":
				vals = []any{"north", "south", "east", "west"}
			case "minecraft:facing_direction", "minecraft:block_face":
				vals = []any{"down", "up", "north", "south", "east", "west"}
			case "minecraft:vertical_half":
				vals = []any{"bottom", "top"}
			case "minecraft:corner_and_cardinal_direction":
				continue
			default:
				continue
			}
			out = append(out, blockState{name: st, values: vals})
		}
	}
	return out
}

// combos lists every combination of a block's states (the first values first: the default), at
// most limit of them.
func combos(states []blockState, limit int) []map[string]any {
	out := []map[string]any{{}}
	for _, st := range states {
		if len(st.values) == 0 {
			continue
		}
		var next []map[string]any
		for _, c := range out {
			for _, v := range st.values {
				m := make(map[string]any, len(c)+1)
				for k, x := range c {
					m[k] = x
				}
				m[st.name] = v
				next = append(next, m)
				if len(next) >= limit {
					break
				}
			}
			if len(next) >= limit {
				break
			}
		}
		out = next
	}
	return out
}

// blocks gives each custom block a free slot (one per look) and a model.
func (b *builder) blocks() {
	used := make([]bool, len(b.in.Slots))
	take := func(solid bool) int {
		for i, s := range b.in.Slots {
			if !used[i] && s.Solid == solid {
				used[i] = true
				return i
			}
		}
		return -1
	}
	for _, d := range b.gatherBlocks() {
		bm := &BlockMapping{Slot: -1}
		bm.Name, bm.NameKey = b.blockName(d.name, d.comps)
		if bm.Name != "" {
			b.res.ItemNames[d.name] = bm.Name
		}
		bm.Solid = collisionSolid(d.comps)
		var notes []string
		// The looks: the default state and every permutation combination that differs.
		cs := combos(d.states, 256)
		if len(d.states) > 0 && len(cs) >= 256 {
			notes = append(notes, "more than 256 state combinations: the rest look like the default")
		}
		lookSlot := map[string]int{}
		var looks []string
		condErr := map[string]bool{}
		for ci, st := range cs {
			comps := map[string]any{}
			for k, v := range d.comps {
				comps[k] = v
			}
			for _, p := range d.perms {
				ok, err := evalCondition(p.cond, st)
				if err != nil {
					if !condErr[p.cond] {
						condErr[p.cond] = true
						notes = append(notes, fmt.Sprintf("permutation condition %q: %v", p.cond, err))
					}
					continue
				}
				if ok {
					for k, v := range p.comps {
						comps[k] = v
					}
				}
			}
			sm, approx, ok := b.blockLook(d.name, comps)
			if !ok {
				if ci == 0 {
					break
				}
				continue
			}
			if approx != "" && ci == 0 {
				bm.Approximate = approx
			}
			key := fmt.Sprintf("%s|%d|%d", sm.model, sm.x, sm.y)
			slot, seen := lookSlot[key]
			if !seen {
				slot = take(bm.Solid)
				lookSlot[key] = slot
				looks = append(looks, key)
				if slot >= 0 {
					s := b.in.Slots[slot]
					if b.blockStates[s.Block] == nil {
						b.blockStates[s.Block] = map[string]stateModel{}
					}
					b.blockStates[s.Block][s.Key()] = sm
				}
			}
			if ci == 0 {
				bm.Slot = slot
				if slot >= 0 {
					bm.JavaState = b.in.Slots[slot].StateString()
					b.res.Blocks[d.name] = slot
					// The block's item (same identifier) is drawn as the block.
					safe := b.safeName("item", d.name)
					b.json(b.assetPath("items", safe+".json"), map[string]any{"model": map[string]any{"type": "minecraft:model", "model": sm.model}})
					bm.ItemModel = b.itemModelID(safe)
					b.res.ItemModels[d.name] = bm.ItemModel
				}
			} else if slot != bm.Slot && slot >= 0 {
				bm.Variants = append(bm.Variants, BlockVariant{States: st, JavaState: b.in.Slots[slot].StateString(), Slot: slot})
			}
		}
		if len(looks) == 0 {
			b.res.Stats.miss("block %s: no model", d.name)
			b.rep.add(catBlock, d.name, Skipped, "no texture or geometry found"+noteText(notes))
			bm.Fallback = "minecraft:stone"
			b.m.Blocks[d.name] = bm
			continue
		}
		if bm.Slot < 0 {
			bm.Fallback = b.fallbackBlock(d.name, bm.Solid)
			b.res.Stats.miss("block %s: no free Java state", d.name)
			b.rep.add(catBlock, d.name, Skipped, fmt.Sprintf("no free Java state left (solid=%v): shown as %s", bm.Solid, bm.Fallback))
			b.m.Blocks[d.name] = bm
			continue
		}
		if len(looks) > 1 {
			free := 0
			for _, k := range looks {
				if lookSlot[k] >= 0 {
					free++
				}
			}
			if free < len(looks) {
				notes = append(notes, fmt.Sprintf("%d of %d looks got no state (pool exhausted)", len(looks)-free, len(looks)))
			}
		}
		b.res.Stats.Blocks++
		b.m.Blocks[d.name] = bm
		st := Converted
		if bm.Approximate != "" {
			notes = append([]string{bm.Approximate}, notes...)
		}
		if len(notes) > 0 {
			st = Approximated
		}
		b.rep.add(catBlock, d.name, st, fmt.Sprintf("%s, %d look(s)", bm.JavaState, len(looks))+noteText(notes))
	}
	for _, blk := range sortedKeys(b.blockStates) {
		models := b.blockStates[blk]
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

// fallbackBlock is the vanilla block shown for a custom block no state is left for: for solid
// blocks the concrete closest in colour to its texture.
func (b *builder) fallbackBlock(name string, solid bool) string {
	if !solid {
		return "minecraft:air"
	}
	return "minecraft:stone"
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

// materialTextures resolves a block's material instances: face/instance name -> Bedrock texture
// path. blocks.json textures are used when the components have none.
func (b *builder) materialTextures(name string, comps map[string]any) map[string]string {
	out := map[string]string{}
	mi, _ := comps["minecraft:material_instances"].(map[string]any)
	mats, _ := mi["materials"].(map[string]any)
	if mats == nil {
		mats = mi
	}
	keys := sortedKeys(mats)
	for _, f := range keys {
		m, _ := mats[f].(map[string]any)
		short, _ := m["texture"].(string)
		if short == "" {
			continue
		}
		if e, ok := b.terrainTex[short]; ok {
			out[f] = e.path
		} else {
			out[f] = short
		}
	}
	// Instances that point at another one ("north": "side").
	for _, f := range keys {
		if alias, ok := mats[f].(string); ok {
			if p, ok := out[alias]; ok {
				out[f] = p
			}
		}
	}
	if len(out) == 0 {
		if e, ok := b.blocksJSON[name]; ok {
			for f, short := range e.textures {
				p := short
				if t, ok := b.terrainTex[short]; ok {
					p = t.path
				}
				switch f {
				case "side":
					for _, s := range []string{"north", "south", "east", "west"} {
						if _, ok := e.textures[s]; !ok {
							out[s] = p
						}
					}
					out["*"] = p
				default:
					out[f] = p
				}
			}
		}
	}
	return out
}

// blockLook writes the model of a block with the given (merged) components and returns it with
// the blockstate rotation it needs.
func (b *builder) blockLook(name string, comps map[string]any) (stateModel, string, bool) {
	texPaths := b.materialTextures(name, comps)
	texFor := func(mat string) (texInfo, bool) {
		p, ok := texPaths[mat]
		if !ok {
			p, ok = texPaths["*"]
		}
		if !ok {
			// Any instance, the first by name.
			for _, k := range sortedKeys(texPaths) {
				p, ok = texPaths[k], true
				break
			}
		}
		if !ok {
			return texInfo{}, false
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
	// minecraft:transformation: quarter turns become the blockstate rotation; the rest is baked.
	var rot, scale, trans [3]float64
	scale = [3]float64{1, 1, 1}
	if t, ok := comps["minecraft:transformation"].(map[string]any); ok {
		rot, scale, trans = vec3(t["rotation"], 0), vec3(t["scale"], 1), vec3(t["translation"], 0)
	}
	sm := stateModel{}
	quarter := func(v float64) bool { return math.Abs(v/90-math.Round(v/90)) < 1e-6 }
	if quarter(rot[0]) && quarter(rot[1]) && rot[2] == 0 {
		sm.x = (int(math.Round(rot[0]))%360 + 360) % 360
		sm.y = (int(math.Round(rot[1]))%360 + 360) % 360
		rot = [3]float64{}
	}
	approx := ""
	sig := fmt.Sprintf("%s|%v|%v|%v|%v", geoID, texPaths, rot, scale, trans)
	modelName := name
	if s := b.lookNames[sig]; s != "" {
		sm.model = s
		return sm, b.lookApprox[sig], true
	}
	safe := b.safeName("block", modelName)
	id := b.ns + ":block/" + safe
	file := b.assetPath("models", "block/"+safe+".json")
	full := geoID == "" || strings.HasPrefix(geoID, "minecraft:geometry.full_block")
	if full && rot == ([3]float64{}) && scale == [3]float64{1, 1, 1} && trans == ([3]float64{}) {
		textures := map[string]any{}
		for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
			if t, ok := texFor(f); ok {
				textures[f] = t.id
			}
		}
		if len(textures) == 0 {
			delete(b.usedSafe, "block|"+safe)
			return sm, "", false
		}
		for _, f := range []string{"up", "north", "down", "south", "west", "east"} {
			if t, ok := textures[f]; ok {
				textures["particle"] = t
				break
			}
		}
		allSame := true
		for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
			if textures[f] != textures["down"] {
				allSame = false
			}
		}
		if allSame {
			b.json(file, map[string]any{"parent": "minecraft:block/cube_all", "textures": map[string]any{"all": textures["down"]}})
		} else {
			b.json(file, map[string]any{"parent": "minecraft:block/cube", "textures": textures})
		}
	} else {
		var els []element
		tw, th := 16.0, 16.0
		if full {
			cube := geoCube{Origin: [3]float64{-8, 0, -8}, Size: [3]float64{16, 16, 16}, UV: json.RawMessage(
				`{"north":{"uv":[0,0],"uv_size":[16,16],"material_instance":"north"},"south":{"uv":[0,0],"uv_size":[16,16],"material_instance":"south"},` +
					`"east":{"uv":[0,0],"uv_size":[16,16],"material_instance":"east"},"west":{"uv":[0,0],"uv_size":[16,16],"material_instance":"west"},` +
					`"up":{"uv":[16,16],"uv_size":[-16,-16],"material_instance":"up"},"down":{"uv":[16,16],"uv_size":[-16,-16],"material_instance":"down"}}`)}
			g := &geometry{texW: 16, texH: 16, bones: []geoBone{{Name: "block", Cubes: []geoCube{cube}}}}
			els = place(g.cubes(nil, nil), blockPost(rot, scale, trans), 1)
		} else {
			g := b.geos[geoID]
			if g == nil {
				delete(b.usedSafe, "block|"+safe)
				return sm, "", false
			}
			ps := boneVisibility(comps)
			cs := g.cubes(ps, nil)
			els = place(cs, blockPost(rot, scale, trans), 1)
			tw, th = g.texW, g.texH
			if tw <= 0 || th <= 0 {
				if t, ok := texFor("*"); ok {
					tw, th = float64(t.size.X), float64(t.size.Y)
				}
			}
			approx = "custom geometry on a full-cube state (neighbouring faces are culled as for a full block)"
			if g.polyMesh {
				approx = "poly_mesh bones are not converted; " + approx
			}
		}
		if !inJavaBox(els) {
			approx = "geometry clipped to Java's -16..32 box; " + approx
			for i := range els {
				for a := 0; a < 3; a++ {
					els[i].from[a] = max(-16, min(32, els[i].from[a]))
					els[i].to[a] = max(-16, min(32, els[i].to[a]))
				}
			}
		}
		model := modelJSONTex(els, func(mat string) string {
			if mat == "" {
				mat = "*"
			}
			t, ok := texFor(mat)
			if !ok {
				return ""
			}
			return t.id
		}, tw, th)
		model["parent"] = "minecraft:block/block"
		if len(model["textures"].(map[string]any)) == 0 {
			delete(b.usedSafe, "block|"+safe)
			return sm, "", false
		}
		b.json(file, model)
	}
	b.lookNames[sig] = id
	b.lookApprox[sig] = approx
	sm.model = id
	return sm, approx, true
}

// blockPost places Bedrock block geometry (x/z centred on 0) in a Java block model (0..16),
// with a transformation's scale, remaining rotation and translation (blocks) about the centre.
func blockPost(rot, scale, trans [3]float64) affine {
	c := [3]float64{8, 8, 8}
	r := javaRot(rot)
	s := mat3{{scale[0], 0, 0}, {0, scale[1], 0}, {0, 0, scale[2]}}
	m := r.mul(s)
	// p -> m (p + (8,0,8) - c) + c + t
	shift := [3]float64{8 - c[0], 0 - c[1], 8 - c[2]}
	ms := m.apply(shift)
	t := conv([3]float64{trans[0] * 16, trans[1] * 16, trans[2] * 16})
	return affine{r: m, t: [3]float64{ms[0] + c[0] + t[0], ms[1] + c[1] + t[1], ms[2] + c[2] + t[2]}}
}

// boneVisibility hides bones a block's geometry component turns off.
func boneVisibility(comps map[string]any) *pose {
	g, _ := comps["minecraft:geometry"].(map[string]any)
	bv, _ := g["bone_visibility"].(map[string]any)
	if len(bv) == 0 {
		return nil
	}
	ps := &pose{skip: map[string]bool{}, hidden: map[string]bool{}}
	for k, v := range bv {
		if bvv, ok := v.(bool); ok && !bvv {
			ps.hidden[strings.ToLower(k)] = true
		}
		if s, ok := v.(string); ok && (s == "false" || s == "0" || s == "0.0") {
			ps.hidden[strings.ToLower(k)] = true
		}
	}
	return ps
}

func vec3(v any, def float64) [3]float64 {
	out := [3]float64{def, def, def}
	if l, ok := v.([]any); ok && len(l) == 3 {
		for i := range out {
			out[i] = num(l[i])
		}
	}
	return out
}

func noteBlockState(models map[string]stateModel) any {
	v := map[string]any{"powered=false": map[string]any{"model": "minecraft:block/note_block"}}
	for _, in := range NoteInstruments {
		for n := 0; n <= 24; n++ {
			k := stateKey(map[string]string{"instrument": in, "note": fmt.Sprint(n), "powered": "true"})
			m := map[string]any{"model": "minecraft:block/note_block"}
			if c, ok := models[k]; ok {
				m = c.apply()
			}
			v[k] = m
		}
	}
	return map[string]any{"variants": v}
}

// redstoneVanilla is vanilla 26.3's redstone_wire blockstate (multipart).
const redstoneVanilla = `{"multipart":[{"apply":{"model":"minecraft:block/redstone_dust_dot"},"when":{"OR":[{"east":"none","north":"none","south":"none","west":"none"},{"east":"side|up","north":"side|up"},{"east":"side|up","south":"side|up"},{"south":"side|up","west":"side|up"},{"north":"side|up","west":"side|up"}]}},{"apply":{"model":"minecraft:block/redstone_dust_side0"},"when":{"north":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side_alt0"},"when":{"south":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side_alt1","y":270},"when":{"east":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_side1","y":270},"when":{"west":"side|up"}},{"apply":{"model":"minecraft:block/redstone_dust_up"},"when":{"north":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":90},"when":{"east":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":180},"when":{"south":"up"}},{"apply":{"model":"minecraft:block/redstone_dust_up","y":270},"when":{"west":"up"}}]}`

// redstoneState is vanilla's redstone wire with its parts limited to powered wire and the
// unpowered states the vanilla mapping uses (the ones not offered as slots), plus a part per
// custom block state.
func (b *builder) redstoneState(models map[string]stateModel) any {
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
	for _, k := range sortedKeys(models) {
		parts = append(parts, map[string]any{"when": parseKey(k), "apply": models[k].apply()})
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
func mushroomState(name string, models map[string]stateModel) any {
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
	for _, k := range sortedKeys(models) {
		parts = append(parts, map[string]any{"when": parseKey(k), "apply": models[k].apply()})
	}
	return map[string]any{"multipart": parts}
}

// blockName is a custom block's en_US name and lang key: its display_name (a lang key or text),
// else tile.<name>.name.
func (b *builder) blockName(name string, comps map[string]any) (string, string) {
	v := str(comps["minecraft:display_name"])
	if v != "" {
		if t, ok := b.en(v); ok {
			return t, v
		}
		if !strings.Contains(v, ".") || strings.Contains(v, " ") {
			return v, ""
		}
	}
	for _, k := range []string{"tile." + name + ".name", "block." + name + ".name"} {
		if t, ok := b.en(k); ok {
			return t, k
		}
	}
	return "", ""
}

var _ = image.Point{}
var _ = sort.Strings
