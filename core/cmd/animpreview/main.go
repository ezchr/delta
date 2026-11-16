// Command animpreview checks custom mob animations offline. It converts Bedrock packs, poses one
// entity at a few animation times and draws, side by side,
//
//   - the Bedrock geometry with every bone posed by the animation (packconv's renderer, which
//     works in Bedrock model space on its own),
//   - the Java side: each part's item model from the converted zip, placed by the translation,
//     rotation and scale the bridge sends for its item display,
//
// and prints the share of pixels that differ. With -parts large enough for every moving bone to
// get a display the two must match; with the default budget the difference is what merging
// bones costs.
//
//	animpreview -pack <pack dir> [-pack ...] -out <dir> [-parts N] <entity id>[=preset[@t1,t2,...]] ...
//
// Presets: idle, walk, run, hover, fly, float, swim, sleep.
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
	"strconv"
	"strings"
	"time"

	"delta/core/internal/anim"
	"delta/core/internal/javapack"
	"delta/core/internal/molang"
	"delta/packconv/render"
)

var verbose = flag.Bool("v", false, "list the parts")

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

var presets = map[string]anim.Inputs{
	"idle":  {OnGround: true},
	"walk":  {OnGround: true, GroundSpeed: 2},
	"run":   {OnGround: true, GroundSpeed: 8},
	"hover": {},
	"fly":   {GroundSpeed: 6, VerticalSpeed: 1},
	"float": {InWater: true},
	"swim":  {InWater: true, GroundSpeed: 3},
	"sleep": {OnGround: true, Flag: func(q molang.Query) bool { return q == molang.QIsSleeping }},
}

const step = 0.05

func main() {
	var packs list
	flag.Var(&packs, "pack", "an unpacked Bedrock resource pack (repeatable, bottom of the stack first)")
	out := flag.String("out", ".", "directory for the sheets")
	parts := flag.Int("parts", 0, "displays per mob (0: the bridge's budget)")
	cell := flag.Int("size", 300, "cell size in pixels")
	zipOut := flag.String("zip", "", "also write the converted Java pack here")
	flag.Parse()
	if len(packs) == 0 || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	in := &javapack.Input{MaxParts: *parts}
	for _, p := range packs {
		s, err := javapack.ReadDir(p)
		if err != nil {
			fail(err)
		}
		in.Packs = append(in.Packs, s)
	}
	start := time.Now()
	res, err := javapack.Convert(in)
	if err != nil {
		fail(err)
	}
	st := res.Stats
	fmt.Printf("converted in %v (entities %v): %d entities, %d animated with %d part models, %d bytes of animations kept, zip %d bytes\n",
		time.Since(start).Round(time.Millisecond), st.EntitiesTook.Round(time.Millisecond), st.Entities, st.Animated, st.Parts, st.AnimBytes, len(res.Zip))
	for _, m := range st.Missing {
		fmt.Println("  not converted:", m)
	}
	if *zipOut != "" {
		if err := os.WriteFile(*zipOut, res.Zip, 0o644); err != nil {
			fail(err)
		}
	}
	z := openZip(res.Zip)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, arg := range flag.Args() {
		id, rest, _ := strings.Cut(arg, "=")
		preset, times, _ := strings.Cut(rest, "@")
		if preset == "" {
			preset = "idle"
		}
		inp, ok := presets[preset]
		if !ok {
			fail(fmt.Errorf("no preset %q", preset))
		}
		ts := []float64{0, 0.35, 0.7}
		if times != "" {
			ts = nil
			for _, s := range strings.Split(times, ",") {
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					fail(err)
				}
				ts = append(ts, f)
			}
		}
		sheet(in.Packs, z, res, id, preset, inp, ts, *cell, *out)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "animpreview:", err)
	os.Exit(1)
}

type zipFS map[string][]byte

func openZip(b []byte) zipFS {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		fail(err)
	}
	z := zipFS{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		z[f.Name] = data
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

func (z zipFS) model(id string) ([]byte, bool) {
	ns, p := split(id)
	b, ok := z["assets/"+ns+"/models/"+p+".json"]
	return b, ok
}

func (z zipFS) texture(id string) *image.NRGBA {
	ns, p := split(id)
	b, ok := z["assets/"+ns+"/textures/"+p+".png"]
	if !ok {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return toNRGBA(img)
}

func toNRGBA(img image.Image) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// bedrockEntity finds an entity's geometry and texture in the packs, as the converter picks
// them.
func bedrockEntity(packs []*javapack.Source, id string) (*render.BedrockGeometry, *image.NRGBA) {
	var geoID, texPath string
	for _, s := range packs {
		for _, n := range s.Files("entity/") {
			b, _ := s.File(n)
			var f struct {
				CE struct {
					D struct {
						ID       string            `json:"identifier"`
						Textures map[string]string `json:"textures"`
						Geometry map[string]string `json:"geometry"`
					} `json:"description"`
				} `json:"minecraft:client_entity"`
			}
			if json.Unmarshal(b, &f) != nil || f.CE.D.ID != id {
				continue
			}
			geoID, texPath = pick(f.CE.D.Geometry), pick(f.CE.D.Textures)
		}
	}
	if geoID == "" {
		fail(fmt.Errorf("no client entity %s", id))
	}
	var geo *render.BedrockGeometry
	var tex *image.NRGBA
	for _, s := range packs {
		for _, n := range s.Files("models/") {
			b, _ := s.File(n)
			if !bytes.Contains(b, []byte(geoID)) {
				continue
			}
			if g, ok := render.ParseBedrockGeometry(b, geoID); ok {
				geo = g
			}
		}
		if b, ok := s.File(texPath + ".png"); ok {
			if img, err := png.Decode(bytes.NewReader(b)); err == nil {
				tex = toNRGBA(img)
			}
		}
	}
	if geo == nil || tex == nil {
		fail(fmt.Errorf("%s: geometry %s or texture %s not found", id, geoID, texPath))
	}
	return geo, tex
}

func pick(m map[string]string) string {
	if v, ok := m["default"]; ok {
		return v
	}
	best := ""
	for k := range m {
		if best == "" || k < best {
			best = k
		}
	}
	return m[best]
}

// place puts an item model's quads where an item display shows them: the model's point m at
// t + l (s * Ry180 (m - 8) / 16) in blocks, given back in Java model space pixels (the frame
// the Bedrock renderer draws in: Ry180 and 16 undone).
func place(qs []render.Quad, t [3]float32, l anim.Mat, s [3]float32) []render.Quad {
	lm := render.Mat(l)
	out := make([]render.Quad, len(qs))
	for i, q := range qs {
		for k, m := range q.P {
			v := render.Vec{-(m[0] - 8) / 16 * float64(s[0]), (m[1] - 8) / 16 * float64(s[1]), -(m[2] - 8) / 16 * float64(s[2])}
			v = lm.Apply(v).Add(render.Vec{float64(t[0]), float64(t[1]), float64(t[2])})
			q.P[k] = render.Vec{-16 * v[0], 16 * v[1], -16 * v[2]}
		}
		if q.N != (render.Vec{}) {
			n := lm.Apply(render.Vec{-q.N[0], q.N[1], -q.N[2]})
			q.N = render.Vec{-n[0], n[1], -n[2]}
		}
		out[i] = q
	}
	return out
}

func sheet(packs []*javapack.Source, z zipFS, res *javapack.Result, id, preset string, in anim.Inputs, times []float64, cell int, dir string) {
	e, ok := res.Entities[id]
	if !ok {
		fail(fmt.Errorf("entity %s was not converted", id))
	}
	geo, tex := bedrockEntity(packs, id)
	if e.Anim == nil {
		fail(fmt.Errorf("entity %s has no scripts", id))
	}
	full := e.Anim.FullRig()
	rig := e.Anim.Rig()
	fmt.Printf("%s: %d bones, %d parts, static model scale %.3f\n", id, len(full.Bones), len(e.Anim.Parts), e.Scale)
	for _, n := range rig.Notes {
		fmt.Println("  note:", n)
	}
	type partQuads struct {
		p  anim.Part
		qs []render.Quad
	}
	var pq []partQuads
	for _, p := range e.Anim.Parts {
		m, err := render.LoadJavaModel(p.Model, z.model)
		if err != nil {
			fail(err)
		}
		name := "root"
		if p.Bone >= 0 {
			name = full.Bones[p.Bone].Name
		}
		if *verbose {
			fmt.Printf("  part %-28s k %.3f elements %d\n", name, p.K, len(m.Elements))
		}
		pq = append(pq, partQuads{p, m.Quads(z.texture)})
	}
	whole, err := render.LoadJavaModel(e.Model, z.model)
	if err != nil {
		fail(err)
	}
	sc := float32(e.Scale)
	wq := place(whole.Quads(z.texture), [3]float32{float32(e.Translation[0]), float32(e.Translation[1]), float32(e.Translation[2])},
		anim.Ident, [3]float32{sc, sc, sc})

	sf, sr := full.NewState(1), rig.NewState(1)
	sf.In, sr.In = in, in
	advance := func(dt float64) {
		sf.Step(dt)
		sr.Step(dt)
	}
	for i := 0; i < 4; i++ {
		advance(0.55)
	}
	now := 0.0
	type row struct {
		t      float64
		bq, jq []render.Quad
	}
	var rows []row
	var all []render.Quad
	for _, t := range times {
		for now+step/2 < t {
			advance(step)
			now += step
		}
		pose := map[string]render.BonePose{}
		for i, b := range full.Bones {
			r, p, s := sf.Local(i)
			pose[strings.ToLower(b.Name)] = render.BonePose{Rot: r, Pos: p, Scale: s}
		}
		bq := geo.Quads(tex, render.Options{Pose: pose})
		var jq []render.Quad
		for _, p := range pq {
			pp := sr.Pose(p.p.Bone, p.p.K)
			q := [4]float64{float64(pp.Q[0]), float64(pp.Q[1]), float64(pp.Q[2]), float64(pp.Q[3])}
			jq = append(jq, place(p.qs, pp.T, anim.QuatMat(q), pp.S)...)
		}
		rows = append(rows, row{t, bq, jq})
		fmt.Printf("  t=%.2f playing %s\n", t, sr.Playing())
		all = append(all, bq...)
	}
	fmt.Printf("  %s: playing %s, scripts.scale %.3f\n", preset, sr.Playing(), sr.Scale())
	lo, hi := render.Bounds(all)
	views := render.Views[:2]
	cols := 2*len(views) + 1
	img := image.NewNRGBA(image.Rect(0, 0, cols*cell, len(rows)*cell))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{20, 22, 26, 255}}, image.Point{}, draw.Src)
	white := color.NRGBA{255, 255, 255, 255}
	for ri, r := range rows {
		for vi, v := range views {
			b := render.Draw(r.bq, v, cell, lo, hi)
			j := render.Draw(r.jq, v, cell, lo, hi)
			fmt.Printf("  t=%.2f view %d: %.2f%% of the pixels differ\n", r.t, vi, 100*render.Diff(b, j))
			render.Label(b, 4, 4, 2, fmt.Sprintf("bedrock %s t=%.2f", preset, r.t), white)
			render.Label(j, 4, 4, 2, fmt.Sprintf("java %d parts", len(pq)), white)
			draw.Draw(img, image.Rect((2*vi)*cell, ri*cell, (2*vi+1)*cell, (ri+1)*cell), b, image.Point{}, draw.Src)
			draw.Draw(img, image.Rect((2*vi+1)*cell, ri*cell, (2*vi+2)*cell, (ri+1)*cell), j, image.Point{}, draw.Src)
		}
		if ri == 0 {
			w := render.Draw(wq, views[0], cell, lo, hi)
			render.Label(w, 4, 4, 2, "java static model", white)
			draw.Draw(img, image.Rect((cols-1)*cell, 0, cols*cell, cell), w, image.Point{}, draw.Src)
		}
	}
	name := strings.NewReplacer(":", "_", "/", "_").Replace(id) + "_" + preset + ".png"
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
	fmt.Println("  wrote", filepath.Join(dir, name))
}
