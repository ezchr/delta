package javapack

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"

	"delta/core/internal/anim"
)

// entities makes the item models of every custom client entity (resource pack entity/*.json):
// the geometry and texture of its "default" keys (or the first).
//
// An entity always gets one model of the whole mob, fitted into Java's model box; the bridge
// shows it on an item_display with Entity's transformation. An entity with animations
// (scripts.animate) also gets a model per moving part (anim.MakePlan decides which bones move
// together), each with the part's pivot at the model's centre, and Entity.Anim to pose them.
// Both are built in the pose the mob stands idle in rather than the geometry's bind pose (a
// T-pose in most packs), so the whole model and the bones that share a part look right.

// maxEntityAnimBones: a geometry with more bones than this is not animated.
const maxEntityAnimBones = 600

type entityDef struct {
	id                 string
	textures, geometry map[string]string
	animations         map[string]string
	renderControllers  []json.RawMessage
	scripts            struct {
		Animate      []json.RawMessage `json:"animate"`
		PreAnimation []string          `json:"pre_animation"`
		Initialize   []string          `json:"initialize"`
		Scale        json.RawMessage   `json:"scale"`
	}
}

// entityOut is one entity's models, made off the builder (workers run in parallel).
type entityOut struct {
	def   *entityDef
	geo   *geometry
	tex   texInfo
	whole map[string]any
	ent   Entity
	parts []map[string]any
	plan  []anim.Part // Model filled in when the files are written
	src   *anim.Source
	notes []string
	err   string
}

func (b *builder) entities() {
	defs := map[string]*entityDef{}
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
						Animations map[string]string `json:"animations"`
						Scripts    json.RawMessage   `json:"scripts"`
						RC         []json.RawMessage `json:"render_controllers"`
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
			def := &entityDef{id: d.Identifier, textures: d.Textures, geometry: d.Geometry, animations: d.Animations, renderControllers: d.RC}
			if len(d.Scripts) > 0 {
				_ = json.Unmarshal(d.Scripts, &def.scripts) // a script section of another shape: no animation
			}
			defs[d.Identifier] = def
		}
	}
	if len(defs) == 0 {
		return
	}
	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Geometry and texture first (the builder is not safe to use from the workers).
	var work []*entityOut
	animated := false
	for _, id := range ids {
		d := defs[id]
		geoID, texPath := pick(d.geometry), pick(d.textures)
		g := b.geos[geoID]
		if g == nil {
			// A vanilla shape: a Java entity stands in, or the shape's own geometry is used.
			vg, ok := b.vanillaShape(d, geoID, &texPath)
			if !ok {
				b.res.Stats.miss("entity %s: geometry %q not found", id, geoID)
				continue
			}
			if vg == nil {
				continue
			}
			g = vg
		}
		tex, ok := b.texture("item", texPath)
		if !ok {
			b.res.Stats.miss("entity %s: texture %q not found", id, texPath)
			continue
		}
		work = append(work, &entityOut{def: d, geo: g, tex: tex})
		animated = animated || len(d.scripts.Animate) > 0 || len(d.scripts.Scale) > 0
	}
	var lib *anim.Library
	if animated {
		lib = anim.NewLibrary()
		for _, s := range b.in.Packs {
			for _, dir := range []string{"animations/", "animation_controllers/"} {
				for _, n := range s.Files(dir) {
					if f, ok := s.File(n); ok && strings.HasSuffix(n, ".json") {
						lib.AddFile(n, stripJSON(f))
					}
				}
			}
		}
	}
	maxParts := b.in.MaxParts
	if maxParts <= 0 {
		maxParts = anim.MaxParts
	}
	workers := max(1, min(4, runtime.GOMAXPROCS(0)))
	var wg sync.WaitGroup
	next := make(chan *entityOut)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range next {
				buildEntity(o, lib, maxParts)
			}
		}()
	}
	for _, o := range work {
		next <- o
	}
	close(next)
	wg.Wait()

	for _, o := range work {
		id := o.def.id
		if o.err != "" {
			b.res.Stats.miss("entity %s: %s", id, o.err)
			continue
		}
		safe := b.safeName("entity", id)
		b.json("assets/"+Namespace+"/models/entity/"+safe+".json", o.whole)
		b.json("assets/"+Namespace+"/items/entity/"+safe+".json", map[string]any{
			"model": map[string]any{"type": "minecraft:model", "model": Namespace + ":entity/" + safe},
		})
		e := o.ent
		e.Model = Namespace + ":entity/" + safe
		e.Name = b.lang["entity."+id+".name"]
		if o.src != nil {
			for i := range o.plan {
				name := fmt.Sprintf("entity/%s/p%d", safe, i)
				b.json("assets/"+Namespace+"/models/"+name+".json", o.parts[i])
				b.json("assets/"+Namespace+"/items/"+name+".json", map[string]any{
					"model": map[string]any{"type": "minecraft:model", "model": Namespace + ":" + name},
				})
				o.plan[i].Model = Namespace + ":" + name
			}
			e.Anim = anim.NewModel(o.plan, o.src, lib)
			if e.Anim.Moves() {
				b.res.Stats.Animated++
				b.res.Stats.Parts += len(o.plan)
			}
		}
		b.res.Entities[id] = e
		b.res.Stats.Entities++
		o.whole, o.parts = nil, nil
	}
	if lib != nil {
		b.res.Stats.AnimBytes = lib.Compact()
	}
}

// buildEntity makes one entity's models. A panic (a pack shaped in a way nothing here expects)
// costs only this entity.
func buildEntity(o *entityOut, lib *anim.Library, maxParts int) {
	defer func() {
		if r := recover(); r != nil {
			o.err = fmt.Sprintf("panic: %v", r)
		}
	}()
	g, d := o.geo, o.def
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(o.tex.size.X), float64(o.tex.size.Y)
	}
	var plan *anim.Plan
	var rigBones int
	if lib != nil && (len(d.scripts.Animate) > 0 || len(d.scripts.Scale) > 0) && len(g.bones) <= maxEntityAnimBones {
		src := &anim.Source{Animations: d.animations, Animate: d.scripts.Animate, Initialize: d.scripts.Initialize,
			PreAnimation: d.scripts.PreAnimation}
		if s := strings.TrimSpace(string(d.scripts.Scale)); s != "" && s != "null" {
			var str string
			if json.Unmarshal(d.scripts.Scale, &str) == nil {
				src.Scale = str
			} else {
				src.Scale = s
			}
		}
		boxes := make([]anim.Box, len(g.bones))
		for i := range g.bones {
			bn := &g.bones[i]
			src.Bones = append(src.Bones, anim.BoneDef{Name: bn.Name, Parent: bn.Parent, Pivot: bn.Pivot, Rotation: bn.Rotation})
			if bn.NeverRender {
				continue
			}
			for j := range bn.Cubes {
				lo, hi := cubeBox(bn, &bn.Cubes[j])
				bx := &boxes[i]
				if !bx.OK {
					bx.Lo, bx.Hi, bx.OK = lo, hi, true
					continue
				}
				for a := 0; a < 3; a++ {
					bx.Lo[a], bx.Hi[a] = min(bx.Lo[a], lo[a]), max(bx.Hi[a], hi[a])
				}
			}
		}
		rig := anim.Compile(src, lib, nil)
		rigBones = len(rig.Bones)
		plan = anim.MakePlan(rig, boxes, maxParts)
		o.src, o.notes = src, rig.Notes
	}

	// The whole model, in the reference pose when there is one.
	var els []element
	for i := range g.bones {
		bn := &g.bones[i]
		if bn.NeverRender {
			continue
		}
		t := affine{r: ident}
		if plan != nil && i < rigBones {
			var ok bool
			if t, ok = toAffine(plan.Ref[i]); !ok {
				continue // scaled away in the reference pose
			}
		} else {
			t = g.bindTransform(i)
		}
		els = append(els, boneElements(bn, t)...)
	}
	if len(els) == 0 {
		o.err = "geometry has no cubes"
		return
	}
	k, c := fit(els, true)
	o.whole = modelJSON(els, o.tex.id, tw, th)
	o.ent = Entity{Scale: 1 / k, Translation: [3]float64{-c[0] / 16, c[1] / 16, -c[2] / 16}}
	if plan == nil {
		return
	}
	moves := false
	defer func() {
		if !moves { // nothing an animation moves: the whole model does
			o.parts, o.plan = nil, nil
		}
	}()
	for _, pp := range plan.Parts {
		var piv [3]float64
		if pp.Bone >= 0 {
			piv = [3]float64{-g.bones[pp.Bone].Pivot[0], g.bones[pp.Bone].Pivot[1], g.bones[pp.Bone].Pivot[2]}
		}
		var pels []element
		for j, m := range pp.Members {
			t, ok := toAffine(pp.Rel[j])
			if !ok {
				continue
			}
			// The part's pivot goes to the origin (and from there to the model's centre).
			t.t = [3]float64{t.t[0] - piv[0], t.t[1] - piv[1], t.t[2] - piv[2]}
			pels = append(pels, boneElements(&g.bones[m], t)...)
		}
		far := 0.0
		for _, e := range pels {
			for a := 0; a < 3; a++ {
				far = max(far, math.Abs(e.from[a]), math.Abs(e.to[a]))
			}
		}
		pk := 1.0
		if far > 23.9 {
			pk = 23.9 / far
		}
		for i := range pels {
			e := &pels[i]
			for a := 0; a < 3; a++ {
				e.from[a], e.to[a], e.origin[a] = 8+pk*e.from[a], 8+pk*e.to[a], 8+pk*e.origin[a]
				if e.from[a] > e.to[a] {
					e.from[a], e.to[a] = e.to[a], e.from[a]
				}
			}
		}
		if len(pels) == 0 {
			continue
		}
		o.parts = append(o.parts, modelJSON(pels, o.tex.id, tw, th))
		moves = moves || pp.Bone >= 0
		o.plan = append(o.plan, anim.Part{Bone: pp.Bone, K: pk})
	}
}

// toAffine is an animation transform as the converter's affine (its scale taken as uniform).
// ok is false for a scale of (nearly) 0: nothing to draw.
func toAffine(x anim.Xform) (affine, bool) {
	s := x.Uniform()
	if s < 1e-4 || math.IsNaN(s) || math.IsInf(s, 0) {
		return affine{}, false
	}
	return affine{r: mat3(x.R), t: x.T, s: s}, true
}

// bindTransform is bone i's model transform in the geometry's bind pose.
func (g *geometry) bindTransform(i int) affine {
	byName := map[string]int{}
	for j := range g.bones {
		byName[strings.ToLower(g.bones[j].Name)] = j
	}
	t := affine{r: ident}
	var chain []int
	for j, depth := i, 0; depth < 64; depth++ {
		chain = append(chain, j)
		p, ok := byName[strings.ToLower(g.bones[j].Parent)]
		if !ok || p == j {
			break
		}
		j = p
	}
	for k := len(chain) - 1; k >= 0; k-- {
		b := &g.bones[chain[k]]
		t = t.then(rotAbout([3]float64{-b.Pivot[0], b.Pivot[1], b.Pivot[2]}, javaRot(b.Rotation)))
	}
	return t
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
