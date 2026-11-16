package javapack

import (
	"encoding/json"
	"strings"
)

// Custom entities that use a vanilla geometry. An add-on can give its own entity a shape that
// only the vanilla resource pack has ("geometry.villager_v2", "geometry.humanoid.custom"); the
// converter never sees that geometry, so such an entity has no model of its own. vanillaShapes
// says what it is shown as instead:
//
//   - a shape whose texture is a player skin (the humanoid shapes) is a Java mannequin wearing
//     the pack's texture as its skin (Result.StandIns: Skins and, when the render controller
//     picks the texture from an array, SkinIndex);
//   - any other shape with the vanilla texture is the Java mob of that shape (StandIns: Java);
//   - any other shape with a texture of the pack's own is a model built from the geometry in
//     the table, when the table has one (a Java mob cannot be given a texture per entity).
//
// To add a shape: one line here, plus its geometry (1.12 format JSON) if entities with their
// own texture on it should keep that texture.
type vanillaShape struct {
	java string // the Java entity type of that shape
	skin bool   // its texture is a player skin
	slim bool   // ... for the slim (3 pixel arm) player model
	geo  string // the geometry, for entities with their own texture ("": none known)
}

var vanillaShapes = map[string]vanillaShape{
	"geometry.villager_v2":         {java: "minecraft:villager"},
	"geometry.villager":            {java: "minecraft:villager", geo: villagerGeo},
	"geometry.villager.v1.8":       {java: "minecraft:villager", geo: villagerGeo},
	"geometry.humanoid":            {java: "minecraft:mannequin", skin: true},
	"geometry.humanoid.custom":     {java: "minecraft:mannequin", skin: true},
	"geometry.humanoid.customslim": {java: "minecraft:mannequin", skin: true, slim: true},
	"geometry.npc.steve":           {java: "minecraft:mannequin", skin: true},
	"geometry.npc.alex":            {java: "minecraft:mannequin", skin: true, slim: true},
	"geometry.zombie":              {java: "minecraft:zombie"},
	"geometry.zombie.v1.8":         {java: "minecraft:zombie"},
	"geometry.skeleton":            {java: "minecraft:skeleton"},
	"geometry.skeleton.v1.8":       {java: "minecraft:skeleton"},
	"geometry.pig":                 {java: "minecraft:pig"},
	"geometry.pig.v1.8":            {java: "minecraft:pig"},
	"geometry.cow":                 {java: "minecraft:cow"},
	"geometry.cow.v1.8":            {java: "minecraft:cow"},
	"geometry.iron_golem":          {java: "minecraft:iron_golem"},
}

// villagerGeo is the old villager (64x64 texture): head with nose, body with robe, folded arms,
// legs.
const villagerGeo = `{"format_version": "1.12.0", "minecraft:geometry": [{
	"description": {"identifier": "geometry.villager.v1.8", "texture_width": 64, "texture_height": 64},
	"bones": [
		{"name": "body", "pivot": [0, 24, 0], "cubes": [
			{"origin": [-4, 12, -3], "size": [8, 12, 6], "uv": [16, 20]},
			{"origin": [-4, 6, -3], "size": [8, 18, 6], "uv": [0, 38], "inflate": 0.5}]},
		{"name": "head", "parent": "body", "pivot": [0, 24, 0], "cubes": [{"origin": [-4, 24, -4], "size": [8, 10, 8], "uv": [0, 0]}]},
		{"name": "nose", "parent": "head", "pivot": [0, 26, 0], "cubes": [{"origin": [-1, 23, -6], "size": [2, 4, 2], "uv": [24, 0]}]},
		{"name": "arms", "parent": "body", "pivot": [0, 22, 0], "rotation": [-42.97, 0, 0], "cubes": [
			{"origin": [-8, 16, -2], "size": [4, 8, 4], "uv": [44, 22]},
			{"origin": [4, 16, -2], "size": [4, 8, 4], "uv": [44, 22], "mirror": true},
			{"origin": [-4, 16, -2], "size": [8, 4, 4], "uv": [40, 38]}]},
		{"name": "leg0", "parent": "body", "pivot": [-2, 12, 0], "cubes": [{"origin": [-4, 0, -2], "size": [4, 12, 4], "uv": [0, 22]}]},
		{"name": "leg1", "parent": "body", "pivot": [2, 12, 0], "mirror": true, "cubes": [{"origin": [0, 0, -2], "size": [4, 12, 4], "uv": [0, 22]}]}
	]}]}`

// StandIn is how a custom entity with a vanilla geometry is shown: as a Java entity.
type StandIn struct {
	Java string // minecraft:entity_type name
	// Skins are the textures a mannequin may wear (Java texture ids, "" for one the pack does
	// not have: the default skin). SkinIndex is the Molang that picks one for an entity
	// (queries on the entity: variant, mark_variant, actor properties); "": the first.
	Skins     []string
	SkinIndex string
	Slim      bool
}

// vanillaShape handles an entity whose geometry is not in the packs. It returns the geometry to
// build a model from (with texPath the texture for it), or records a stand-in and returns nil;
// ok is false when the shape is unknown.
func (b *builder) vanillaShape(d *entityDef, geoID string, texPath *string) (g *geometry, ok bool) {
	shape, known := vanillaShapes[strings.ToLower(geoID)]
	if !known {
		return nil, false
	}
	paths, index := b.entityTextures(d)
	own := func(p string) bool {
		_, _, ok := b.findTextureIn("", p)
		return p != "" && ok
	}
	if shape.skin {
		st := StandIn{Java: shape.java, Slim: shape.slim, SkinIndex: index}
		for _, p := range paths {
			id := ""
			if own(p) {
				if t, ok := b.texture("entity", p); ok {
					id = t.id
				}
			}
			st.Skins = append(st.Skins, id)
		}
		b.res.StandIns[d.id] = st
		b.res.Stats.StandIns++
		return nil, true
	}
	if len(paths) > 0 && own(paths[0]) && shape.geo != "" {
		geos := map[string]*geometry{}
		parseGeometries([]byte(shape.geo), geos)
		for _, g := range geos {
			*texPath = paths[0]
			return g, true
		}
	}
	b.res.StandIns[d.id] = StandIn{Java: shape.java}
	b.res.Stats.StandIns++
	return nil, true
}

// entityTextures is the textures an entity may show, as its first render controller picks them:
// the entries of the array its first texture indexes (with the index expression), the one
// texture it names, or else the entity's default texture.
func (b *builder) entityTextures(d *entityDef) (paths []string, index string) {
	fallback := []string{pick(d.textures)}
	if len(d.renderControllers) == 0 {
		return fallback, ""
	}
	name, _, ok := namedEntry(d.renderControllers[0])
	if !ok {
		return fallback, ""
	}
	var rc struct {
		Textures []string `json:"textures"`
		Arrays   struct {
			Textures map[string][]string `json:"textures"`
		} `json:"arrays"`
	}
	found := false
	for _, s := range b.in.Packs {
		for _, n := range s.Files("render_controllers/") {
			var f struct {
				RC map[string]json.RawMessage `json:"render_controllers"`
			}
			if !strings.HasSuffix(n, ".json") || !s.readJSON(n, &f) {
				continue
			}
			if raw, ok := f.RC[name]; ok && json.Unmarshal(raw, &rc) == nil {
				found = true
			}
		}
	}
	if !found || len(rc.Textures) == 0 {
		return fallback, ""
	}
	lookup := func(ref string) string {
		key, ok := cutPrefixFold(strings.TrimSpace(ref), "texture.")
		if !ok {
			return ""
		}
		for k, v := range d.textures {
			if strings.EqualFold(k, key) {
				return v
			}
		}
		return ""
	}
	first := strings.TrimSpace(rc.Textures[0])
	if i := strings.IndexByte(first, '['); i > 0 && strings.HasSuffix(first, "]") {
		for arr, entries := range rc.Arrays.Textures {
			if !strings.EqualFold(arr, strings.TrimSpace(first[:i])) || len(entries) == 0 || len(entries) > 4096 {
				continue
			}
			for _, e := range entries {
				paths = append(paths, lookup(e))
			}
			return paths, first[i+1 : len(first)-1]
		}
		return fallback, ""
	}
	if p := lookup(first); p != "" {
		return []string{p}, ""
	}
	return fallback, ""
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// namedEntry is a JSON list entry that is a name or {name: condition}.
func namedEntry(raw json.RawMessage) (name, cond string, ok bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, "", true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) != 1 {
		return "", "", false
	}
	for k, v := range m {
		name = k
		if json.Unmarshal(v, &cond) != nil {
			cond = string(v)
		}
	}
	return name, cond, true
}
