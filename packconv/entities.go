package packconv

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Custom entities: a client entity's default geometry and texture (render controllers resolved
// statically) become item models shown on item_display entities. The whole model is one item
// model (fitted into Java's box, scaled back by the display), and each bone with cubes is its
// own item model whose (8, 8, 8) is the bone's pivot, so a translator can draw the entity bone
// by bone (and later turn bones for animations). Rest poses are baked into the models;
// animations are not converted.

type clientEntity struct {
	id                 string
	textures, geometry map[string]string
	renderControllers  []string
	animations         map[string]string
	scale              float64
}

type renderController struct {
	geometry string
	textures []string
	arrays   map[string][]string // "array.name" -> entries
}

func (b *builder) readClientEntities() map[string]*clientEntity {
	defs := map[string]*clientEntity{}
	for _, s := range b.in.Packs {
		if s.Encrypted {
			continue
		}
		for _, n := range s.Files("entity/") {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			var f struct {
				CE struct {
					Description struct {
						Identifier        string            `json:"identifier"`
						Textures          map[string]string `json:"textures"`
						Geometry          map[string]string `json:"geometry"`
						Animations        map[string]string `json:"animations"`
						RenderControllers []any             `json:"render_controllers"`
						Scripts           struct {
							Scale any `json:"scale"`
						} `json:"scripts"`
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
			ce := &clientEntity{id: d.Identifier, textures: d.Textures, geometry: d.Geometry, animations: d.Animations, scale: 1}
			for _, rc := range d.RenderControllers {
				switch t := rc.(type) {
				case string:
					ce.renderControllers = append(ce.renderControllers, t)
				case map[string]any:
					for _, k := range sortedKeys(t) {
						ce.renderControllers = append(ce.renderControllers, k)
					}
				}
			}
			switch v := d.Scripts.Scale.(type) {
			case float64:
				ce.scale = v
			case string:
				if f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, ";")), 64); err == nil {
					ce.scale = f
				}
			}
			if ce.scale <= 0 {
				ce.scale = 1
			}
			defs[d.Identifier] = ce
		}
	}
	return defs
}

func (b *builder) readRenderControllers() map[string]*renderController {
	out := map[string]*renderController{}
	for _, s := range b.in.Packs {
		if s.Encrypted {
			continue
		}
		for _, n := range s.Files("render_controllers/") {
			var f struct {
				RC map[string]struct {
					Geometry string         `json:"geometry"`
					Textures []string       `json:"textures"`
					Arrays   map[string]any `json:"arrays"`
				} `json:"render_controllers"`
			}
			if !strings.HasSuffix(n, ".json") || !s.readJSON(n, &f) {
				continue
			}
			for id, rc := range f.RC {
				r := &renderController{geometry: rc.Geometry, textures: rc.Textures, arrays: map[string][]string{}}
				for _, kind := range rc.Arrays {
					m, _ := kind.(map[string]any)
					for name, list := range m {
						l, _ := list.([]any)
						for _, e := range l {
							r.arrays[strings.ToLower(name)] = append(r.arrays[strings.ToLower(name)], str(e))
						}
					}
				}
				out[id] = r
			}
		}
	}
	return out
}

// resolve turns a render controller reference ("Geometry.default", "Array.skins[q.variant]")
// into a key of the client entity's map, statically (an array's first entry).
func (rc *renderController) resolve(ref, kind string) string {
	ref = strings.TrimSpace(ref)
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "array.") {
		name := lower
		if i := strings.IndexByte(name, '['); i >= 0 {
			name = name[:i]
		}
		if l := rc.arrays[name]; len(l) > 0 {
			return rc.resolve(l[0], kind)
		}
		return ""
	}
	if k, ok := strings.CutPrefix(lower, kind+"."); ok {
		return k
	}
	return ""
}

// entityLook is a client entity's geometry and texture after its render controller.
func (b *builder) entityLook(ce *clientEntity, rcs map[string]*renderController) (geo, tex string) {
	geo, tex = pick(ce.geometry), pick(ce.textures)
	for _, id := range ce.renderControllers {
		rc := rcs[id]
		if rc == nil {
			continue
		}
		if k := rc.resolve(rc.geometry, "geometry"); k != "" {
			if g, ok := lookupFold(ce.geometry, k); ok {
				geo = g
			}
		}
		if len(rc.textures) > 0 {
			if k := rc.resolve(rc.textures[0], "texture"); k != "" {
				if t, ok := lookupFold(ce.textures, k); ok {
					tex = t
				}
			}
		}
		break
	}
	return geo, tex
}

func lookupFold(m map[string]string, k string) (string, bool) {
	for mk, v := range m {
		if strings.EqualFold(mk, k) {
			return v, true
		}
	}
	return "", false
}

// entities makes the models of every custom client entity.
func (b *builder) entities() {
	defs := b.readClientEntities()
	rcs := b.readRenderControllers()
	for _, id := range sortedKeys(defs) {
		ce := defs[id]
		geoID, texPath := b.entityLook(ce, rcs)
		g := b.geos[geoID]
		if g == nil {
			b.res.Stats.miss("entity %s: geometry %q not found", id, geoID)
			b.rep.add(catEntity, id, Skipped, fmt.Sprintf("geometry %q not found", geoID))
			continue
		}
		tex, ok := b.texture("item", texPath)
		if !ok {
			b.res.Stats.miss("entity %s: texture %q not found", id, texPath)
			b.rep.add(catEntity, id, Skipped, fmt.Sprintf("texture %q not found", texPath))
			continue
		}
		tw, th := g.texW, g.texH
		if tw <= 0 || th <= 0 {
			tw, th = float64(tex.size.X), float64(tex.size.Y)
		}
		all := g.cubes(nil, nil)
		if len(all) == 0 {
			b.res.Stats.miss("entity %s: geometry has no cubes", id)
			b.rep.add(catEntity, id, Skipped, "geometry has no cubes")
			continue
		}
		s := ce.scale
		// The whole model, centred and fitted (one item display).
		els := place(all, identAffine, s)
		k, c := fit(els, true)
		safe := b.safeName("entity", id)
		b.json(b.assetPath("models", "entity/"+safe+".json"), modelJSON(els, tex.id, tw, th))
		b.json(b.assetPath("items", "entity/"+safe+".json"), map[string]any{
			"model": map[string]any{"type": "minecraft:model", "model": b.ns + ":entity/" + safe},
		})
		e := Entity{Model: b.ns + ":entity/" + safe, Scale: 1 / k,
			Translation: [3]float64{-c[0] / 16, c[1] / 16, -c[2] / 16}}
		name, key := b.entityName(id)
		e.Name = name

		// One part per bone with cubes.
		bt := g.boneTransforms(nil)
		var notes []string
		for _, bn := range g.bones {
			key := strings.ToLower(bn.Name)
			e.Bones = append(e.Bones, EntityBone{Name: bn.Name, Parent: bn.Parent, Pivot: bn.Pivot, Rotation: bn.Rotation, Cubes: len(bn.Cubes)})
			cs := g.cubes(nil, map[string]bool{key: true})
			if len(cs) == 0 {
				continue
			}
			piv := bt[key].apply(conv(bn.Pivot))
			piv = [3]float64{piv[0] * s, piv[1] * s, piv[2] * s}
			for i := range cs {
				cs[i].t = affine{r: cs[i].t.r, t: cs[i].t.t}
			}
			lo, hi := cubeBounds(cs)
			for a := 0; a < 3; a++ {
				lo[a], hi[a] = lo[a]*s, hi[a]*s
			}
			pk := fitScale(lo, hi, piv, [3]float64{8, 8, 8}, 0)
			post := affine{r: ident, t: [3]float64{8 - pk*piv[0], 8 - pk*piv[1], 8 - pk*piv[2]}}
			pels := place(cs, post, pk*s)
			model := modelJSON(pels, tex.id, tw, th)
			ds := r4(1 / pk)
			if ds > 4 {
				notes = append(notes, fmt.Sprintf("bone %s needs a display scale of %.2f (Java's limit is 4): drawn smaller", bn.Name, ds))
				ds = 4
			}
			model["display"] = map[string]any{"fixed": map[string]any{"rotation": []float64{0, 0, 0}, "translation": []float64{0, 0, 0}, "scale": []float64{ds, ds, ds}}}
			psafe := b.safeName("entitypart", id+"/"+bn.Name)
			b.json(b.assetPath("models", "entity/"+psafe+".json"), model)
			b.json(b.assetPath("items", "entity/"+psafe+".json"), map[string]any{
				"model": map[string]any{"type": "minecraft:model", "model": b.ns + ":entity/" + psafe},
			})
			e.Parts = append(e.Parts, EntityPart{Bone: bn.Name, Parent: bn.Parent, Model: b.ns + ":entity/" + psafe,
				Offset: [3]float64{r4(-piv[0] / 16), r4(piv[1] / 16), r4(-piv[2] / 16)},
				Pivot:  bn.Pivot, Rotation: bn.Rotation, Scale: r4(pk)})
		}
		lo, hi := cubeBounds(all)
		e.Width = r4(max(hi[0]-lo[0], hi[2]-lo[2]) * s / 16)
		e.Height = r4(max(0, hi[1]) * s / 16)
		e.StandIn = standIn(e.Width, e.Height)
		b.res.Entities[id] = e
		b.res.Stats.Entities++
		em := &EntityMapping{Model: e.Model, Scale: r4(e.Scale), Translation: [3]float64{r4(e.Translation[0]), r4(e.Translation[1]), r4(e.Translation[2])},
			ModelYaw: 180, Bones: e.Bones, Name: name, NameKey: key, Width: e.Width, Height: e.Height, StandIn: e.StandIn}
		for _, p := range e.Parts {
			em.Parts = append(em.Parts, PartMapping{Bone: p.Bone, Parent: p.Parent, Model: p.Model, Offset: p.Offset,
				Pivot: p.Pivot, Rotation: p.Rotation, Scale: p.Scale, Display: "fixed"})
		}
		for _, k := range sortedKeys(ce.animations) {
			em.Unsupported = append(em.Unsupported, "animation "+k+" ("+ce.animations[k]+")")
		}
		if g.polyMesh {
			em.Unsupported = append(em.Unsupported, "poly_mesh bones")
		}
		b.m.Entities[id] = em
		if len(ce.animations) > 0 {
			notes = append(notes, fmt.Sprintf("%d animations not converted (static pose)", len(ce.animations)))
		}
		if k < 1 {
			notes = append(notes, fmt.Sprintf("whole model shrunk x%.3f to fit Java's box (display scales it back)", k))
		}
		st := Converted
		if len(notes) > 0 {
			st = Approximated
		}
		b.rep.add(catEntity, id, st, fmt.Sprintf("%d parts, %d bones, %.2fx%.2f blocks, stand-in %s", len(e.Parts), len(e.Bones), e.Width, e.Height, e.StandIn)+noteText(notes))
	}
}

// entityName is a custom entity's en_US name and lang key.
func (b *builder) entityName(id string) (string, string) {
	for _, k := range []string{"entity." + id + ".name", "item.spawn_egg.entity." + id + ".name"} {
		if t, ok := b.en(k); ok {
			return t, k
		}
	}
	return "", ""
}

// standIn is a vanilla Java entity of about the given size (blocks), for its hitbox.
func standIn(w, h float64) string {
	type ent struct {
		name string
		w, h float64
	}
	list := []ent{{"minecraft:rabbit", 0.4, 0.5}, {"minecraft:chicken", 0.4, 0.7}, {"minecraft:fox", 0.6, 0.7},
		{"minecraft:wolf", 0.6, 0.85}, {"minecraft:pig", 0.9, 0.9}, {"minecraft:sheep", 0.9, 1.3},
		{"minecraft:cow", 0.9, 1.4}, {"minecraft:zombie", 0.6, 1.95}, {"minecraft:polar_bear", 1.4, 1.4},
		{"minecraft:horse", 1.4, 1.6}, {"minecraft:iron_golem", 1.4, 2.7}, {"minecraft:ravager", 1.95, 2.2},
		{"minecraft:ghast", 4, 4}}
	best, bd := list[0].name, math.Inf(1)
	lw, lh := math.Log(max(w, 0.1)), math.Log(max(h, 0.1))
	for _, e := range list {
		d := math.Pow(math.Log(e.w)-lw, 2) + math.Pow(math.Log(e.h)-lh, 2)
		if d < bd {
			best, bd = e.name, d
		}
	}
	return best
}
