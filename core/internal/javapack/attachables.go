package javapack

import (
	"encoding/json"
	"math"
	"path"
	"sort"
	"strings"
)

// Attachables are how a Bedrock add-on draws an item someone holds or wears: a geometry and texture
// bound to the holder's skeleton. Java has two matching mechanisms:
//
//   - Worn armour: an equipment asset (assets/<ns>/equipment/<name>.json) whose humanoid and
//     humanoid_leggings layers are textures in the classic 64x32 armour layout, named by the
//     stack's minecraft:equippable component. Bedrock armour attachables on the standard armour
//     geometry (geometry.humanoid.armor.* / geometry.player.armor.*) use that very layout. Add-ons
//     that draw armour with their own geometry often ship the classic textures as well (a "2D"
//     subpack, or <set>_1/_2 files); those are used. Java cannot draw an armour geometry of its own.
//   - Held items with their own geometry (swords, axes, blocks...): a 3D item model built from the
//     attachable's geometry in the third person pose of its animations, with display transforms that
//     put it where Bedrock draws it in the hand. The inventory, ground, frames and the head keep the
//     flat icon (a select on the display context).

// Equip is how a custom item is worn on Java.
type Equip struct {
	Slot  string // "head", "chest", "legs" or "feet"
	Asset string // equipment asset id ("" if the pack has no texture for it: worn but not drawn)
}

type attachable struct {
	id         string
	textures   map[string]string
	geometry   map[string]string
	animations map[string]string
	animate    []string
	root       string // "" or "subpacks/<name>/": where its texture paths are looked up first
}

// readAttachables reads the packs' attachables: the main ones by the item they draw and the ones in
// subpacks (alternatives the client may pick) by item too, and the packs' animations.
func (b *builder) readAttachables() {
	b.attMain, b.attSub = map[string]*attachable{}, map[string][]*attachable{}
	exact := map[string]bool{}
	for _, s := range b.in.Packs {
		for _, n := range s.Files("") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			root := ""
			if strings.HasPrefix(n, "subpacks/") {
				parts := strings.SplitN(n, "/", 3)
				if len(parts) < 3 || !strings.HasPrefix(parts[2], "attachables/") {
					continue
				}
				root = parts[0] + "/" + parts[1] + "/"
			} else if !strings.HasPrefix(n, "attachables/") {
				continue
			}
			var f struct {
				A struct {
					D struct {
						Identifier string            `json:"identifier"`
						Item       json.RawMessage   `json:"item"`
						Textures   map[string]string `json:"textures"`
						Geometry   map[string]string `json:"geometry"`
						Animations map[string]string `json:"animations"`
						Scripts    struct {
							Animate []json.RawMessage `json:"animate"`
						} `json:"scripts"`
					} `json:"description"`
				} `json:"minecraft:attachable"`
			}
			if !s.readJSON(n, &f) || f.A.D.Identifier == "" {
				continue
			}
			d := f.A.D
			a := &attachable{id: d.Identifier, textures: d.Textures, geometry: d.Geometry, animations: d.Animations, root: root}
			for _, raw := range d.Scripts.Animate {
				var name string
				if json.Unmarshal(raw, &name) == nil {
					a.animate = append(a.animate, name)
					continue
				}
				var m map[string]any
				if json.Unmarshal(raw, &m) == nil {
					keys := make([]string, 0, len(m))
					for k := range m {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					a.animate = append(a.animate, keys...)
				}
			}
			// The items it draws: "item" (an object of item -> condition, or one id) or its own id.
			var items []string
			var one string
			var many map[string]any
			switch {
			case json.Unmarshal(d.Item, &many) == nil && len(many) > 0:
				for k := range many {
					items = append(items, k)
				}
			case json.Unmarshal(d.Item, &one) == nil && one != "":
				items = append(items, one)
			default:
				items = append(items, d.Identifier)
			}
			for _, it := range items {
				if root != "" {
					b.attSub[it] = append(b.attSub[it], a)
					continue
				}
				// The attachable named after the item wins over conditional ones (".player").
				if it == d.Identifier || !exact[it] {
					b.attMain[it] = a
					exact[it] = it == d.Identifier
				}
			}
		}
	}
	b.anims = map[string]map[string]animBone{}
	for _, s := range b.in.Packs {
		for _, n := range s.Files("animations/") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			var f struct {
				Animations map[string]struct {
					Bones map[string]animBone `json:"bones"`
				} `json:"animations"`
			}
			if !s.readJSON(n, &f) {
				continue
			}
			for id, a := range f.Animations {
				b.anims[id] = a.Bones
			}
		}
	}
}

// animBone is one bone's channels in an animation: a value, a list of three, or keyframes.
type animBone struct {
	Rotation json.RawMessage `json:"rotation"`
	Position json.RawMessage `json:"position"`
	Scale    json.RawMessage `json:"scale"`
}

func isStdArmorGeo(id string) bool {
	id = strings.ToLower(id)
	return strings.HasPrefix(id, "geometry.humanoid.armor") || strings.HasPrefix(id, "geometry.player.armor") ||
		strings.HasPrefix(id, "geometry.humanoid.armour")
}

// wearSlot is the armour slot a custom item is worn in ("" if none).
func wearSlot(id string, comps map[string]any) string {
	w, _ := comps["minecraft:wearable"].(map[string]any)
	if w == nil {
		return ""
	}
	if s, ok := w["slot"].(string); ok {
		switch strings.TrimPrefix(strings.ToLower(s), "slot.armor.") {
		case "head":
			return "head"
		case "chest":
			return "chest"
		case "legs":
			return "legs"
		case "feet":
			return "feet"
		}
		return ""
	}
	if n, ok := nbtInt(w["slot"]); ok {
		// Bedrock's EquipmentSlot numbering: 2 head, 3 chest, 4 legs, 5 feet.
		switch n {
		case 2:
			return "head"
		case 3:
			return "chest"
		case 4:
			return "legs"
		case 5:
			return "feet"
		}
	}
	for suf, slot := range map[string]string{"_helmet": "head", "_chestplate": "chest", "_leggings": "legs", "_boots": "feet"} {
		if strings.HasSuffix(id, suf) {
			return slot
		}
	}
	return ""
}

func nbtInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int8:
		return int64(t), true
	case uint8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return int64(t), true
	}
	return 0, false
}

// armorTex is a classic armour layer texture: its Bedrock path and the subpack it is in.
type armorTex struct{ root, path string }

// stdTexture is the texture of an attachable on the standard armour geometry.
func stdTexture(a *attachable) (armorTex, bool) {
	if a == nil || !isStdArmorGeo(pick(a.geometry)) {
		return armorTex{}, false
	}
	p := texDefault(a.textures)
	return armorTex{a.root, p}, p != ""
}

// texDefault is an attachable's texture: "default", else the first that is not the glint.
func texDefault(m map[string]string) string {
	if v, ok := m["default"]; ok {
		return v
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if !strings.Contains(v, "enchanted_item_glint") && k != "enchanted" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return m[keys[0]]
}

// armor gives every wearable custom item its Java equipment: slot and layer texture.
func (b *builder) armor() {
	slots := map[string]string{}
	var ids []string
	for _, it := range b.in.Items {
		comps, _ := it.Data["components"].(map[string]any)
		if s := wearSlot(it.ID, comps); s != "" {
			slots[it.ID] = s
			ids = append(ids, it.ID)
		}
	}
	sort.Strings(ids)
	// Sets: items whose main attachables share a texture.
	set := map[string][]string{}
	for _, id := range ids {
		if a := b.attMain[id]; a != nil {
			k := texDefault(a.textures)
			set[k] = append(set[k], id)
		}
	}
	own := func(id string) (armorTex, bool) {
		if t, ok := stdTexture(b.attMain[id]); ok {
			return t, true
		}
		for _, a := range b.attSub[id] {
			if t, ok := stdTexture(a); ok {
				return t, true
			}
		}
		return armorTex{}, false
	}
	assets := map[string]map[string]string{} // asset name -> layer -> texture id
	for _, id := range ids {
		slot := slots[id]
		layer := "humanoid"
		if slot == "legs" {
			layer = "humanoid_leggings"
		}
		t, ok := own(id)
		approx := false
		var synth []byte
		if !ok && b.attMain[id] != nil {
			// A piece drawn with its own geometry (a 3D helmet): the set's classic texture for the
			// same layer, from a sibling piece.
			for _, sib := range set[texDefault(b.attMain[id].textures)] {
				if (slots[sib] == "legs") != (slot == "legs") {
					continue
				}
				if t, ok = own(sib); ok {
					break
				}
			}
			if !ok {
				t, ok = b.namedLayer(texDefault(b.attMain[id].textures), slot == "legs")
			}
			if !ok {
				synth, ok = b.synthLayer(b.attMain[id], slot)
			}
			if !ok {
				// Its own texture if it is in the 64x32 armour layout anyway.
				p := texDefault(b.attMain[id].textures)
				if data, tga, found := b.findTextureIn("", p); found {
					if _, size, err := toPNG(data, tga); err == nil && size.X == 2*size.Y {
						t, ok, approx = armorTex{"", p}, true, true
					}
				}
			}
		}
		e := Equip{Slot: slot}
		if ok && synth != nil {
			name := b.safeName("equip", strings.ReplaceAll(id, ":", "_")+"_layer")
			b.files["assets/"+Namespace+"/textures/entity/equipment/"+layer+"/"+name+".png"] = synth
			assets[name] = map[string]string{layer: Namespace + ":" + name}
			e.Asset = Namespace + ":" + name
			b.res.Stats.Equipment++
			b.res.Stats.miss("armour %s: own geometry, layer made from its main cubes", id)
		} else if ok {
			if name, tok := b.equipTexture(layer, t); tok {
				if assets[name] == nil {
					assets[name] = map[string]string{}
				}
				assets[name][layer] = Namespace + ":" + name
				e.Asset = Namespace + ":" + name
				b.res.Stats.Equipment++
				if approx {
					b.res.Stats.miss("armour %s: own geometry, drawn with its texture on the standard armour", id)
				}
			}
		} else {
			b.res.Stats.miss("armour %s: no armour layer texture (own geometry only)", id)
		}
		b.res.ItemEquip[id] = e
	}
	names := make([]string, 0, len(assets))
	for n := range assets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		layers := map[string]any{}
		for layer, tex := range assets[n] {
			layers[layer] = []any{map[string]any{"texture": tex}}
		}
		b.json("assets/"+Namespace+"/equipment/"+n+".json", map[string]any{"layers": layers})
	}
}

// namedLayer finds a set's classic layer texture by the usual names next to its own texture:
// <set>_1 / <set>_2 or <set>_layer_1 / _layer_2, in the pack or a subpack.
func (b *builder) namedLayer(p string, legs bool) (armorTex, bool) {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(p), ".png"), ".tga")
	for _, suf := range []string{"_armor", "_armour", "_layer_1", "_layer_2", "_1", "_2"} {
		base = strings.TrimSuffix(base, suf)
	}
	n := "1"
	if legs {
		n = "2"
	}
	roots := []string{""}
	seen := map[string]bool{}
	var subs []string
	for _, s := range b.in.Packs {
		for _, f := range s.Files("subpacks/") {
			parts := strings.SplitN(f, "/", 3)
			if len(parts) == 3 && !seen[parts[1]] {
				seen[parts[1]] = true
				subs = append(subs, "subpacks/"+parts[1]+"/")
			}
		}
	}
	sort.Strings(subs)
	roots = append(roots, subs...)
	for _, cand := range []string{base + "_" + n, base + "_layer_" + n} {
		for _, r := range roots {
			if _, _, ok := b.findTextureExact(r + cand); ok {
				return armorTex{r, cand}, true
			}
		}
	}
	return armorTex{}, false
}

// equipTexture copies an armour layer texture to textures/entity/equipment/<layer>/<name>.png and
// returns its name.
func (b *builder) equipTexture(layer string, t armorTex) (string, bool) {
	src := t.root + "|" + strings.ToLower(t.path)
	if n, ok := b.equipDone[layer+"|"+src]; ok {
		return n, true
	}
	data, tga, ok := b.findTextureIn(t.root, t.path)
	if !ok {
		return "", false
	}
	png, _, err := toPNG(data, tga)
	if err != nil {
		return "", false
	}
	name := b.equipName[src]
	if name == "" {
		base := strings.ToLower(path.Base(t.path))
		base = strings.TrimSuffix(strings.TrimSuffix(base, ".png"), ".tga")
		name = b.safeName("equip", base)
		b.equipName[src] = name
	}
	b.files["assets/"+Namespace+"/textures/entity/equipment/"+layer+"/"+name+".png"] = png
	b.equipDone[layer+"|"+src] = name
	return name, true
}

// findTextureIn finds a texture by Bedrock path in a subpack (root) first, then in the packs.
func (b *builder) findTextureIn(root, p string) ([]byte, bool, bool) {
	if root != "" {
		if d, tga, ok := b.findTextureExact(root + strings.TrimPrefix(p, "/")); ok {
			return d, tga, ok
		}
	}
	return b.findTexture(p)
}

// findTextureExact is findTexture for a full path in a pack (one that may be in a subpack).
func (b *builder) findTextureExact(p string) ([]byte, bool, bool) {
	p = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(p), ".png"), ".tga")
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
		if f, ok := s.File(p + ".png"); ok {
			return f, false, true
		}
		if f, ok := s.File(p + ".tga"); ok {
			return f, true, true
		}
	}
	return nil, false, false
}

// Java's hand: ItemInHandLayer turns the arm's pose by X -90 then Y 180 and moves (1, 2, -10)
// pixels (right hand) before the item's display transform. In the arm's space (Java entity model:
// y down, front -z, the shoulder pivot at 0) a point the display transform puts at d is at
// handR*(handV + d).
var (
	handR = rotX(-90).mul(rotY(180))
	handV = [3]float64{1, 2, -10}
	// Bedrock's rightItem bone (geometry.humanoid) is at (-6, 15, 1), the right arm's pivot at
	// (-5, 22, 0): the held attachable's origin is (-1, -7, 1) from the shoulder, which is
	// (-1, 7, 1) in Java's y-down model space.
	bedrockGrip = [3]float64{-1, 7, 1}
	// Bedrock points in Java model space (x mirrored, y up, see geometry.go) to Java's entity
	// model space (y down, x the other way): a turn of 180 degrees about z.
	jmToEntity = rotZ(180)
	// Java's handheld (item/handheld) transforms, which a held model's first person pose is
	// derived from: the item is shown where a vanilla sword's parts would be, relative to its
	// third person pose.
	vanillaTP = display{rot: rotXYZ(0, -90, 55), t: [3]float64{0, 4, 0.5}, s: 0.85}
	vanillaFP = display{rot: rotXYZ(0, -90, 25), t: [3]float64{1.13, 3.2, 1.13}, s: 0.68}
)

// display is an item display transform: d(m) = t + s*rot*(m - 8), pixels.
type display struct {
	rot mat3
	t   [3]float64
	s   float64
}

// rotXYZ is Java's display rotation (Quaternionf.rotationXYZ: Rx*Ry*Rz), degrees.
func rotXYZ(x, y, z float64) mat3 { return rotX(x).mul(rotY(y)).mul(rotZ(z)) }

// eulerXYZ splits m = Rx(x) Ry(y) Rz(z) into its angles in degrees.
func eulerXYZ(m mat3) (x, y, z float64) {
	sy := max(-1, min(1, m[0][2]))
	y = math.Asin(sy)
	if math.Abs(sy) < 0.99999 {
		x = math.Atan2(-m[1][2], m[2][2])
		z = math.Atan2(-m[0][1], m[0][0])
	} else {
		x = math.Atan2(m[2][1], m[1][1])
		z = 0
	}
	const d = 180 / math.Pi
	return x * d, y * d, z * d
}

func (a mat3) transpose() mat3 {
	var t mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			t[i][j] = a[j][i]
		}
	}
	return t
}

func (d display) json() map[string]any {
	x, y, z := eulerXYZ(d.rot)
	clamp := func(v float64) float64 { return r4(max(-80, min(80, v))) }
	s := r4(max(-4, min(4, d.s)))
	return map[string]any{
		"rotation":    []float64{r4(x), r4(y), r4(z)},
		"translation": []float64{clamp(d.t[0]), clamp(d.t[1]), clamp(d.t[2])},
		"scale":       []float64{s, s, s},
	}
}

// pose is the attachable's third person pose: its animations evaluated for a player not in first
// person, by bone. ok is false if part of it could not be evaluated.
func (b *builder) pose(a *attachable) (map[string]boneAnim, bool) {
	out := map[string]boneAnim{}
	ok := true
	for _, short := range a.animate {
		id := a.animations[short]
		if id == "" {
			continue
		}
		if strings.HasPrefix(id, "controller.") {
			ok = false
			continue
		}
		bones, found := b.anims[id]
		if !found {
			ok = false
			continue
		}
		for bone, ch := range bones {
			key := strings.ToLower(bone)
			cur := out[key]
			if r, good := vec3(ch.Rotation, 0); r != nil {
				for i := 0; i < 3; i++ {
					cur.rot[i] += r[i]
				}
				ok = ok && good
			}
			if p, good := vec3(ch.Position, 0); p != nil {
				for i := 0; i < 3; i++ {
					cur.pos[i] += p[i]
				}
				ok = ok && good
			}
			if sc, good := vec3(ch.Scale, 1); sc != nil {
				if cur.scale == 0 {
					cur.scale = 1
				}
				cur.scale *= (sc[0] + sc[1] + sc[2]) / 3
				ok = ok && good && sc[0] == sc[1] && sc[1] == sc[2]
			}
			out[key] = cur
		}
	}
	return out, ok
}

// vec3 reads an animation channel for the third person: a value or three (numbers or Molang), or
// keyframes (the first). nil if absent.
func vec3(raw json.RawMessage, def float64) ([]float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	one := func(r json.RawMessage) (float64, bool) {
		var f float64
		if json.Unmarshal(r, &f) == nil {
			return f, true
		}
		var s string
		if json.Unmarshal(r, &s) == nil {
			if v, ok := evalMolang(s, false); ok {
				return v, true
			}
			return def, false
		}
		return def, false
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		switch len(list) {
		case 1:
			v, ok := one(list[0])
			return []float64{v, v, v}, ok
		case 3:
		default:
			return nil, false
		}
		out := make([]float64, 3)
		ok := true
		for i := range list {
			var good bool
			out[i], good = one(list[i])
			ok = ok && good
		}
		return out, ok
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) == nil && len(keys) > 0 {
		// Keyframes: the earliest one (a pose held, not the motion).
		first, ft := "", math.Inf(1)
		for k := range keys {
			var t float64
			if err := json.Unmarshal([]byte(k), &t); err == nil && t < ft {
				first, ft = k, t
			}
		}
		if first == "" {
			return nil, false
		}
		kf := keys[first]
		var obj map[string]json.RawMessage
		if json.Unmarshal(kf, &obj) == nil {
			if v, ok := obj["post"]; ok {
				kf = v
			} else if v, ok := obj["pre"]; ok {
				kf = v
			}
		}
		v, _ := vec3(kf, def)
		return v, len(keys) == 1
	}
	v, ok := one(raw)
	return []float64{v, v, v}, ok
}

// held gives custom items whose attachable has a geometry of its own a 3D model in the hand.
func (b *builder) held() {
	for _, it := range b.in.Items {
		comps, _ := it.Data["components"].(map[string]any)
		if wearSlot(it.ID, comps) != "" {
			continue
		}
		a := b.attMain[it.ID]
		if a == nil {
			continue
		}
		g := b.geos[pick(a.geometry)]
		if g == nil {
			continue // a vanilla geometry (item sprite, armour): the flat icon is right
		}
		texPath := texDefault(a.textures)
		tex, ok := b.textureAt("item", a.root, texPath)
		if !ok {
			b.res.Stats.miss("held %s: texture %q not found", it.ID, texPath)
			continue
		}
		anim, exact := b.pose(a)
		if !exact {
			b.res.Stats.miss("held %s: animation partly evaluated (pose approximated)", it.ID)
		}
		model, ok := heldModel(g, anim, tex)
		if !ok {
			b.res.Stats.miss("held %s: geometry has no cubes", it.ID)
			continue
		}
		safe := b.itemSafe[it.ID]
		if safe == "" {
			safe = b.safeName("item", it.ID)
			b.itemSafe[it.ID] = safe
		}
		b.json("assets/"+Namespace+"/models/item/"+safe+"_held.json", model)
		held := map[string]any{"type": "minecraft:model", "model": Namespace + ":item/" + safe + "_held"}
		def := held
		if b.itemFlat[it.ID] {
			def = map[string]any{
				"type": "minecraft:select", "property": "minecraft:display_context",
				"cases": []any{map[string]any{
					"when":  []string{"gui", "ground", "fixed", "on_shelf", "head"},
					"model": map[string]any{"type": "minecraft:model", "model": Namespace + ":item/" + safe},
				}},
				"fallback": held,
			}
		} else {
			b.res.ItemModels[it.ID] = Namespace + ":" + safe
			b.res.Stats.Items++
		}
		b.json("assets/"+Namespace+"/items/"+safe+".json", map[string]any{"model": def})
		b.res.Stats.Held++
	}
}

// heldPre is the move from a held attachable's posed geometry (Java model space) to the item
// frame of Java's hand (before the display transform): the arm space point is
// bedrockGrip + jmToEntity*q, the item frame handR^T*(that) - handV.
func heldPre() affine {
	hT := handR.transpose()
	g0 := hT.apply(bedrockGrip)
	return affine{r: hT.mul(jmToEntity), t: [3]float64{g0[0] - handV[0], g0[1] - handV[1], g0[2] - handV[2]}}
}

// heldModel is a held attachable's Java item model: the geometry posed, turned into Java's hand
// space, fitted into the model box, with the display transforms that undo the fit.
func heldModel(g *geometry, anim map[string]boneAnim, tex texInfo) (map[string]any, bool) {
	pre := heldPre()
	els := g.elementsWith(pre, anim)
	if len(els) == 0 {
		return nil, false
	}
	// The display rotation takes the turn most cubes share, so the model's elements stay unturned.
	best, bestN := ident, countUnturned(els)
	for _, c := range turns(els) {
		if n := countUnturned(g.elementsWith(affine{r: c.transpose()}.then(pre), anim)); n > bestN {
			best, bestN = c, n
		}
	}
	els = g.elementsWith(affine{r: best.transpose()}.then(pre), anim)
	k, c := fit(els, true)
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(tex.size.X), float64(tex.size.Y)
	}
	m := modelJSON(els, tex.id, tw, th)
	tp, fp := heldDisplays(best, c, k, pre.t, heldFPScale)
	m["display"] = map[string]any{
		"thirdperson_righthand": tp.json(),
		"firstperson_righthand": fp.json(),
	}
	return m, true
}

// heldFPScale shrinks a held model in first person about the grip: Bedrock models are drawn at
// the player's scale (a katana is longer than a sword sprite), which fills the first person view
// at vanilla's sword scale; Bedrock's own first person poses use about half.
const heldFPScale = 0.6

// heldDisplays are the third and first person transforms of a held model turned by best (its
// elements are best^T of the item frame) and fitted by fit's k and centre c. grip is the hand's
// point in the item frame; the first person pose is shrunk by fpScale about it.
func heldDisplays(best mat3, c [3]float64, k float64, grip [3]float64, fpScale float64) (tp, fp display) {
	// Third person: d(m) = best*(c + (m-8)/k).
	bc := best.apply(c)
	tp = display{rot: best, t: bc, s: 1 / k}
	// First person: vanillaFP(vanillaTP^-1(shrink(tp(m)))), shrink about the grip.
	sc := [3]float64{grip[0] + fpScale*(bc[0]-grip[0]), grip[1] + fpScale*(bc[1]-grip[1]), grip[2] + fpScale*(bc[2]-grip[2])}
	rel := vanillaFP.rot.mul(vanillaTP.rot.transpose())
	ratio := vanillaFP.s / vanillaTP.s
	rd := rel.apply([3]float64{sc[0] - vanillaTP.t[0], sc[1] - vanillaTP.t[1], sc[2] - vanillaTP.t[2]})
	fp = display{rot: rel.mul(best), s: ratio * fpScale / k,
		t: [3]float64{vanillaFP.t[0] + ratio*rd[0], vanillaFP.t[1] + ratio*rd[1], vanillaFP.t[2] + ratio*rd[2]}}
	return tp, fp
}

func countUnturned(els []element) int {
	n := 0
	for _, e := range els {
		if !e.rotated {
			n++
		}
	}
	return n
}

// turns are the distinct rotations of turned elements (at most 16).
func turns(els []element) []mat3 {
	var out []mat3
	for _, e := range els {
		if !e.rotated {
			continue
		}
		r := rotZ(e.rz).mul(rotY(e.ry)).mul(rotX(e.rx))
		dup := false
		for _, o := range out {
			if isIdent(o.transpose().mul(r)) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
		if len(out) >= 16 {
			break
		}
	}
	return out
}
