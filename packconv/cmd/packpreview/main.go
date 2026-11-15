// Command packpreview draws before/after sheets of a conversion: for each sample, the Bedrock
// source (geometry with its texture, drawn as the game textures entity cubes) next to the Java
// model the converter made (drawn as the Java client builds model faces), from two angles, and
// optionally a hand-made Java model for comparison.
//
//	packpreview -in <bedrock packs> -java out.zip [-ref handmade.zip] -out dir sample...
//
// Samples: item:<id> (the held 3D model and the flat icon), entity:<id> (the whole model),
// part:<id>/<bone> (one bone's model), block:<id>. A sample can name the hand-made model to show
// next to it: item:<id>=<ref item_model or model id>.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"javabridge/packconv"
	"javabridge/packconv/render"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, strings.Split(v, ",")...); return nil }

const cell = 170

var mapping packconv.Mapping

func main() {
	var in list
	flag.Var(&in, "in", "Bedrock resource packs (as for packconv)")
	javaZip := flag.String("java", "", "converted Java pack")
	refZip := flag.String("ref", "", "hand-made Java pack to compare with (optional)")
	out := flag.String("out", ".", "output directory")
	name := flag.String("name", "sheet", "sheet file name prefix")
	perSheet := flag.Int("rows", 6, "samples per sheet")
	mapPath := flag.String("map", "", "the conversion's mapping.json (for item, block, entity and armour models)")
	flag.Parse()
	if *mapPath != "" {
		b, err := os.ReadFile(*mapPath)
		if err != nil {
			fail(err)
		}
		if err := json.Unmarshal(b, &mapping); err != nil {
			fail(err)
		}
	}
	var packs []*packconv.Source
	for _, p := range in {
		srcs, err := packconv.ReadPacks(p)
		if err != nil {
			fail(err)
		}
		for _, s := range srcs {
			s.SetSubpack(s.DefaultSubpack())
			packs = append(packs, s)
		}
	}
	bed := newBedrock(packs)
	jz := openZip(*javaZip)
	var rz *zipFS
	if *refZip != "" {
		rz = openZip(*refZip)
	}
	var rows []*image.NRGBA
	for _, s := range flag.Args() {
		rows = append(rows, sampleRow(s, bed, jz, rz))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for i := 0; i < len(rows); i += *perSheet {
		end := min(i+*perSheet, len(rows))
		w := 0
		h := 0
		for _, r := range rows[i:end] {
			w = max(w, r.Bounds().Dx())
			h += r.Bounds().Dy()
		}
		sheet := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.NRGBA{20, 22, 26, 255}}, image.Point{}, draw.Src)
		y := 0
		for _, r := range rows[i:end] {
			draw.Draw(sheet, r.Bounds().Add(image.Pt(0, y)), r, image.Point{}, draw.Src)
			y += r.Bounds().Dy()
		}
		p := filepath.Join(*out, fmt.Sprintf("%s_%02d.png", *name, i / *perSheet + 1))
		f, err := os.Create(p)
		if err != nil {
			fail(err)
		}
		_ = png.Encode(f, sheet)
		_ = f.Close()
		fmt.Println(p)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "packpreview:", err)
	os.Exit(1)
}

// ---- Java side ---------------------------------------------------------------------------

type zipFS struct{ files map[string][]byte }

func openZip(p string) *zipFS {
	b, err := os.ReadFile(p)
	if err != nil {
		fail(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		fail(err)
	}
	z := &zipFS{files: map[string][]byte{}}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		z.files[f.Name] = data
	}
	return z
}

func split(id string) (string, string) {
	ns, p, ok := strings.Cut(id, ":")
	if !ok {
		return "minecraft", id
	}
	return ns, p
}

func (z *zipFS) model(id string) ([]byte, bool) {
	ns, p := split(id)
	b, ok := z.files["assets/"+ns+"/models/"+p+".json"]
	return b, ok
}

func (z *zipFS) texture(id string) *image.NRGBA {
	ns, p := split(id)
	b, ok := z.files["assets/"+ns+"/textures/"+p+".png"]
	if !ok {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	out := toNRGBA(img)
	if _, anim := z.files["assets/"+ns+"/textures/"+p+".png.mcmeta"]; anim && out.Bounds().Dy() > out.Bounds().Dx() {
		w := out.Bounds().Dx()
		out = out.SubImage(image.Rect(0, 0, w, w)).(*image.NRGBA)
		out = toNRGBA(out)
	}
	return out
}

// itemModels resolves an item_model id (items/<path>.json) to the model ids it shows: the
// in-hand one first, the GUI one second (the same when there is one).
func (z *zipFS) itemModels(id string) (hand, gui string) {
	ns, p := split(id)
	b, ok := z.files["assets/"+ns+"/items/"+p+".json"]
	if !ok {
		return id, id // a model id itself
	}
	var f struct {
		Model json.RawMessage `json:"model"`
	}
	_ = json.Unmarshal(b, &f)
	var walk func(raw json.RawMessage, ctx string) string
	walk = func(raw json.RawMessage, ctx string) string {
		var m struct {
			Type     string          `json:"type"`
			Model    string          `json:"model"`
			Fallback json.RawMessage `json:"fallback"`
			Cases    []struct {
				When  any             `json:"when"`
				Model json.RawMessage `json:"model"`
			} `json:"cases"`
		}
		_ = json.Unmarshal(raw, &m)
		switch strings.TrimPrefix(m.Type, "minecraft:") {
		case "model":
			return m.Model
		case "select":
			for _, c := range m.Cases {
				if strings.Contains(fmt.Sprint(c.When), ctx) {
					return walk(c.Model, ctx)
				}
			}
			return walk(m.Fallback, ctx)
		}
		return ""
	}
	return walk(f.Model, "thirdperson_righthand"), walk(f.Model, "gui")
}

func (z *zipFS) render(modelID string) []*image.NRGBA {
	m, err := render.LoadJavaModel(modelID, z.model)
	if err != nil {
		return []*image.NRGBA{note("java: " + err.Error())}
	}
	qs := m.Quads(z.texture)
	return views(qs)
}

func views(qs []render.Quad) []*image.NRGBA {
	if len(qs) == 0 {
		return []*image.NRGBA{note("nothing to draw")}
	}
	lo, hi := render.Bounds(qs)
	var out []*image.NRGBA
	for _, v := range render.Views {
		out = append(out, render.Draw(qs, v, cell, lo, hi))
	}
	return out
}

func note(s string) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, cell, cell))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{60, 30, 30, 255}}, image.Point{}, draw.Src)
	for i := 0; i*45 < len(s); i++ {
		render.Label(img, 4, 4+i*8, 1, s[i*45:min(len(s), i*45+45)], color.NRGBA{255, 220, 220, 255})
	}
	return img
}

// ---- Bedrock side ------------------------------------------------------------------------

type bedrock struct {
	packs   []*packconv.Source
	geoFile map[string][]byte // geometry id -> file
	attach  map[string][2]string
	ents    map[string][2]string
	terrain map[string]string
	blocks  map[string]string
}

func newBedrock(packs []*packconv.Source) *bedrock {
	b := &bedrock{packs: packs, geoFile: map[string][]byte{}, attach: map[string][2]string{}, ents: map[string][2]string{},
		terrain: map[string]string{}, blocks: map[string]string{}}
	for _, s := range packs {
		for _, n := range s.Files("models/") {
			data, _ := s.File(n)
			data = packconv.CleanJSON(data)
			var top map[string]json.RawMessage
			if json.Unmarshal(data, &top) != nil {
				continue
			}
			if raw, ok := top["minecraft:geometry"]; ok {
				var l []struct {
					D struct {
						ID string `json:"identifier"`
					} `json:"description"`
				}
				_ = json.Unmarshal(raw, &l)
				for _, g := range l {
					b.geoFile[g.D.ID] = data
				}
			}
			for k := range top {
				id, _, _ := strings.Cut(k, ":")
				if strings.HasPrefix(id, "geometry.") {
					b.geoFile[id] = data
				}
			}
		}
		for _, dir := range []string{"attachables/", "entity/"} {
			for _, n := range s.Files(dir) {
				data, _ := s.File(n)
				var top map[string]json.RawMessage
				if json.Unmarshal(packconv.CleanJSON(data), &top) != nil {
					continue
				}
				for k, raw := range top {
					if k != "minecraft:attachable" && k != "minecraft:client_entity" {
						continue
					}
					var v struct {
						D struct {
							ID       string            `json:"identifier"`
							Textures map[string]string `json:"textures"`
							Geometry map[string]string `json:"geometry"`
						} `json:"description"`
					}
					if json.Unmarshal(raw, &v) != nil {
						continue
					}
					e := [2]string{first(v.D.Geometry), first(v.D.Textures)}
					if dir == "entity/" {
						b.ents[v.D.ID] = e
						continue
					}
					if strings.HasSuffix(v.D.ID, ".player") {
						continue
					}
					b.attach[v.D.ID] = e
				}
			}
		}
	}
	return b
}

func first(m map[string]string) string {
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

func (b *bedrock) image(p string) *image.NRGBA {
	p = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(p), ".png"), ".tga")
	for i := len(b.packs) - 1; i >= 0; i-- {
		if data, ok := b.packs[i].File(p + ".png"); ok {
			if img, err := png.Decode(bytes.NewReader(data)); err == nil {
				return toNRGBA(img)
			}
		}
	}
	return nil
}

func (b *bedrock) render(geo, tex string, o render.Options) []*image.NRGBA {
	data, ok := b.geoFile[geo]
	if !ok {
		return []*image.NRGBA{note("bedrock: no geometry " + geo)}
	}
	g, ok := render.ParseBedrockGeometry(data, geo)
	if !ok {
		return []*image.NRGBA{note("bedrock: bad geometry " + geo)}
	}
	img := b.image(tex)
	if img == nil {
		return []*image.NRGBA{note("bedrock: no texture " + tex)}
	}
	return views(g.Quads(img, o))
}

func toNRGBA(img image.Image) *image.NRGBA {
	r := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

// ---- samples -------------------------------------------------------------------------------

func sampleRow(s string, bed *bedrock, jz, rz *zipFS) *image.NRGBA {
	spec, ref, _ := strings.Cut(s, "=")
	kind, id, _ := strings.Cut(spec, ":")
	var left, right, third []*image.NRGBA
	label := spec
	switch kind {
	case "item", "head":
		im := mapping.Items[id]
		if im == nil {
			left = []*image.NRGBA{note("not in the mapping: " + id)}
			break
		}
		hand, gui := jz.itemModels(im.ItemModel)
		if kind == "head" {
			hand = jz.headModel(im.ItemModel)
		}
		a, ok := bed.attach[id]
		if ok && !strings.HasPrefix(a[0], "geometry.humanoid") && !strings.HasPrefix(a[0], "geometry.player") {
			o := render.Options{Skip: map[string]bool{"rightitem": true, "item_root": true, "leftitem": true}, Post: render.RotY(180)}
			if kind == "head" {
				o = render.Options{}
			}
			left = bed.render(a[0], a[1], o)
			right = jz.render(hand)
			label += " (3d: " + a[0] + ")"
		} else {
			right = jz.render(gui)
			left = []*image.NRGBA{note("flat icon")}
		}
		if ref != "" && rz != nil {
			rh, rg := rz.itemModels(ref)
			if kind == "head" {
				rh = rz.headModel(ref)
			}
			if ok {
				third = rz.render(rh)
			} else {
				third = rz.render(rg)
			}
		}
	case "armor":
		im := mapping.Items[id]
		if im == nil || im.Equipment == nil || im.Equipment.Asset == "" {
			left = []*image.NRGBA{note("no equipment asset for " + id)}
			break
		}
		var bq []render.Quad
		for _, pid := range sortedIDs() {
			pm := mapping.Items[pid]
			if pm.Equipment == nil || pm.Equipment.Asset != im.Equipment.Asset {
				continue
			}
			a, ok := bed.attach[pid]
			if !ok {
				continue
			}
			tex := bed.image(a[1])
			if tex == nil {
				continue
			}
			if strings.HasPrefix(a[0], "geometry.humanoid") || strings.HasPrefix(a[0], "geometry.player") {
				bq = append(bq, armourBoxes(pm.Equipment.Slot, tex)...)
			} else if data, ok := bed.geoFile[a[0]]; ok {
				if g, ok := render.ParseBedrockGeometry(data, a[0]); ok {
					bq = append(bq, g.Quads(tex, render.Options{})...)
				}
			}
			label += " " + pm.Equipment.Slot
		}
		left = views(bq)
		right = views(jz.armourQuads(im.Equipment.Asset))
		if ref != "" && rz != nil {
			third = views(rz.armourQuads(ref))
		}
	case "entity", "part":
		eid, bone, _ := strings.Cut(id, "/")
		e, ok := bed.ents[eid]
		em := mapping.Entities[eid]
		if !ok || em == nil {
			left = []*image.NRGBA{note("no client entity " + eid)}
			break
		}
		o := render.Options{}
		mid := em.Model
		if bone != "" {
			o.Bones = map[string]bool{strings.ToLower(bone): true}
			for _, p := range em.Parts {
				if strings.EqualFold(p.Bone, bone) {
					mid = p.Model
				}
			}
		}
		left = bed.render(e[0], e[1], o)
		right = jz.render(mid)
		if ref != "" && rz != nil {
			rh, _ := rz.itemModels(ref)
			third = rz.render(rh)
		}
	case "block":
		bm := mapping.Blocks[id]
		if bm == nil {
			left = []*image.NRGBA{note("not in the mapping: " + id)}
			break
		}
		hand, _ := jz.itemModels(bm.ItemModel)
		right = jz.render(hand)
		label += " " + bm.JavaState
		if ref != "" && rz != nil {
			third = rz.render(ref)
		}
	}
	if len(left) == len(render.Views) && len(right) == len(render.Views) {
		worst := 0.0
		for i := range left {
			worst = max(worst, render.Diff(left[i], right[i]))
		}
		label += fmt.Sprintf(" diff %.1f%%", worst*100)
		fmt.Printf("%-60s bedrock vs converted: %.2f%% of pixels differ (worst view)\n", spec, worst*100)
	}
	cols := append(append(append([]*image.NRGBA{}, left...), right...), third...)
	row := image.NewNRGBA(image.Rect(0, 0, max(len(cols), 1)*(cell+4), cell+16))
	draw.Draw(row, row.Bounds(), &image.Uniform{color.NRGBA{20, 22, 26, 255}}, image.Point{}, draw.Src)
	render.Label(row, 4, 4, 2, label, color.NRGBA{230, 230, 230, 255})
	x := 0
	for i, c := range cols {
		draw.Draw(row, c.Bounds().Add(image.Pt(x, 16)), c, image.Point{}, draw.Src)
		tag := "bedrock"
		if i >= len(left) {
			tag = "java (converted)"
		}
		if i >= len(left)+len(right) {
			tag = "java (hand-made)"
		}
		render.Label(row, x+4, 16+cell-10, 1, tag, color.NRGBA{255, 255, 120, 255})
		x += cell + 4
	}
	return row
}

func sortedIDs() []string {
	ids := make([]string, 0, len(mapping.Items))
	for id := range mapping.Items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// headModel is the model an item definition shows in the head display context.
func (z *zipFS) headModel(id string) string {
	ns, p := split(id)
	b, ok := z.files["assets/"+ns+"/items/"+p+".json"]
	if !ok {
		return id
	}
	var f struct {
		Model struct {
			Model string `json:"model"`
			Cases []struct {
				When  any `json:"when"`
				Model struct {
					Model string `json:"model"`
				} `json:"model"`
			} `json:"cases"`
		} `json:"model"`
	}
	_ = json.Unmarshal(b, &f)
	for _, c := range f.Model.Cases {
		if strings.Contains(fmt.Sprint(c.When), "head") {
			return c.Model.Model
		}
	}
	return f.Model.Model
}

// armourBoxes are the game's armour boxes for a slot, textured with a 64x32 layer.
func armourBoxes(slot string, tex *image.NRGBA) []render.Quad {
	type box struct {
		o, s   [3]float64
		uv     string
		inf    float64
		mirror bool
	}
	var boxes []box
	switch slot {
	case "head":
		boxes = []box{{[3]float64{-4, 24, -4}, [3]float64{8, 8, 8}, "[0,0]", 1, false}}
	case "chest":
		boxes = []box{{[3]float64{-4, 12, -2}, [3]float64{8, 12, 4}, "[16,16]", 1, false},
			{[3]float64{-8, 12, -2}, [3]float64{4, 12, 4}, "[40,16]", 1, false}, {[3]float64{4, 12, -2}, [3]float64{4, 12, 4}, "[40,16]", 1, true}}
	case "legs":
		boxes = []box{{[3]float64{-4, 12, -2}, [3]float64{8, 12, 4}, "[16,16]", 0.5, false},
			{[3]float64{-4, 0, -2}, [3]float64{4, 12, 4}, "[0,16]", 0.5, false}, {[3]float64{0, 0, -2}, [3]float64{4, 12, 4}, "[0,16]", 0.5, true}}
	case "feet":
		boxes = []box{{[3]float64{-4, 0, -2}, [3]float64{4, 12, 4}, "[0,16]", 1, false}, {[3]float64{0, 0, -2}, [3]float64{4, 12, 4}, "[0,16]", 1, true}}
	}
	g := &render.BedrockGeometry{TexW: 64, TexH: 32}
	for i, b := range boxes {
		inf, mir := b.inf, b.mirror
		g.Bones = append(g.Bones, render.BedrockBone{Name: fmt.Sprint("b", i), Cubes: []render.BedrockCube{{Origin: b.o, Size: b.s,
			UV: json.RawMessage(b.uv), Inflate: &inf, Mirror: &mir}}})
	}
	return g.Quads(tex, render.Options{})
}

// armourQuads draws an equipment asset's layers on the game's armour boxes.
func (z *zipFS) armourQuads(asset string) []render.Quad {
	ns, p := split(asset)
	b, ok := z.files["assets/"+ns+"/equipment/"+p+".json"]
	if !ok {
		return nil
	}
	var f struct {
		Layers map[string][]struct {
			Texture string `json:"texture"`
		} `json:"layers"`
	}
	_ = json.Unmarshal(b, &f)
	var out []render.Quad
	for layer, list := range f.Layers {
		if len(list) == 0 {
			continue
		}
		tns, tp := split(list[0].Texture)
		tex := z.texture(tns + ":entity/equipment/" + layer + "/" + tp)
		if tex == nil {
			continue
		}
		if layer == "humanoid" {
			for _, s := range []string{"head", "chest", "feet"} {
				out = append(out, armourBoxes(s, tex)...)
			}
		} else if layer == "humanoid_leggings" {
			out = append(out, armourBoxes("legs", tex)...)
		}
	}
	return out
}
