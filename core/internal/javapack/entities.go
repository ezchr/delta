package javapack

import (
	"sort"
	"strings"
)

// entities makes an item model per custom client entity (resource pack entity/*.json): the
// geometry and texture of its "default" keys (or the first), fitted into Java's model box. The
// bridge shows it on an item_display with Entity's transformation.
func (b *builder) entities() {
	type def struct {
		textures, geometry map[string]string
	}
	defs := map[string]def{}
	for _, s := range b.in.Packs {
		for _, n := range s.Files("entity/") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			var f struct {
				CE struct {
					Description struct {
						Identifier string            `json:"identifier"`
						Textures   map[string]string `json:"textures"`
						Geometry   map[string]string `json:"geometry"`
					} `json:"description"`
				} `json:"minecraft:client_entity"`
			}
			if !s.readJSON(n, &f) {
				continue
			}
			d := f.CE.Description
			if d.Identifier == "" || strings.HasPrefix(d.Identifier, "minecraft:") {
				continue
			}
			defs[d.Identifier] = def{textures: d.Textures, geometry: d.Geometry}
		}
	}
	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := defs[id]
		geoID, texPath := pick(d.geometry), pick(d.textures)
		g := b.geos[geoID]
		if g == nil {
			b.res.Stats.miss("entity %s: geometry %q not found", id, geoID)
			continue
		}
		tex, ok := b.texture("item", texPath)
		if !ok {
			b.res.Stats.miss("entity %s: texture %q not found", id, texPath)
			continue
		}
		els := g.elements()
		if len(els) == 0 {
			b.res.Stats.miss("entity %s: geometry has no cubes", id)
			continue
		}
		k, c := fit(els, true)
		tw, th := g.texW, g.texH
		if tw <= 0 || th <= 0 {
			tw, th = float64(tex.size.X), float64(tex.size.Y)
		}
		safe := b.safeName("entity", id)
		b.json("assets/"+Namespace+"/models/entity/"+safe+".json", modelJSON(els, tex.id, tw, th))
		b.json("assets/"+Namespace+"/items/entity/"+safe+".json", map[string]any{
			"model": map[string]any{"type": "minecraft:model", "model": Namespace + ":entity/" + safe},
		})
		e := Entity{Model: Namespace + ":entity/" + safe, Scale: 1 / k,
			Translation: [3]float64{-c[0] / 16, c[1] / 16, -c[2] / 16}}
		e.Name = b.lang["entity."+id+".name"]
		b.res.Entities[id] = e
		b.res.Stats.Entities++
	}
}

// pick is a client entity map's "default" entry, else its first by key.
func pick(m map[string]string) string {
	if v, ok := m["default"]; ok {
		return v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return m[keys[0]]
}
