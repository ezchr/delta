package javapack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"

	"delta/core/internal/anim"
)

// AnimatedPack is a small pack with custom entities for tests:
// t:walker (a body, a head with a hat, two legs and feathers many of which move; a controller
// that walks when it moves), t:statue (no scripts) and t:sized (scripts.scale only).
func AnimatedPack(feathers int) map[string][]byte {
	var tex bytes.Buffer
	_ = png.Encode(&tex, image.NewNRGBA(image.Rect(0, 0, 64, 64)))
	bones := `{"name": "root", "pivot": [0, 0, 0]},
		{"name": "body", "parent": "root", "pivot": [0, 8, 0], "cubes": [{"origin": [-4, 8, -2], "size": [8, 8, 4], "uv": [0, 0]}]},
		{"name": "head", "parent": "body", "pivot": [0, 16, 0], "rotation": [10, 0, 0], "cubes": [{"origin": [-3, 16, -3], "size": [6, 6, 6], "uv": [0, 16]}]},
		{"name": "hat", "parent": "head", "pivot": [0, 22, 0], "cubes": [{"origin": [-4, 22, -4], "size": [8, 1, 8], "uv": [60, 60]}]},
		{"name": "leg_left", "parent": "body", "pivot": [2, 8, 0], "cubes": [{"origin": [1, 0, -1], "size": [2, 8, 2], "uv": [32, 0]}]},
		{"name": "leg_right", "parent": "body", "pivot": [-2, 8, 0], "mirror": true, "cubes": [{"origin": [-3, 0, -1], "size": [2, 8, 2], "uv": [32, 0]}]},
		{"name": "tail", "parent": "body", "pivot": [0, 9, 2], "cubes": [{"origin": [-1, 8, 2], "size": [2, 2, 60], "uv": [0, 40],
			"rotation": [0, 20, 0], "pivot": [0, 9, 2]}]}`
	anims := `"animation.t.idle": {"loop": true, "bones": {"head": {"rotation": ["math.sin(q.anim_time*90)*10", 0, 0]},
			"body": {"position": [0, "math.sin(q.anim_time*90)*4", 0]}, "tail": {"rotation": [0, "math.sin(q.anim_time*120)*25", 0]}}},
		"animation.t.walk": {"loop": true, "animation_length": 1, "bones": {
			"leg_left": {"rotation": {"0.0": [30, 0, 0], "0.5": [-30, 0, 0], "1.0": [30, 0, 0]}},
			"leg_right": {"rotation": {"0.0": [-30, 0, 0], "0.5": [30, 0, 0], "1.0": [-30, 0, 0]}}}},
		"animation.t.feathers": {"loop": true, "bones": {`
	for i := 0; i < feathers; i++ {
		bones += fmt.Sprintf(`, {"name": "feather%d", "parent": "body", "pivot": [%d, 12, 2], "cubes": [{"origin": [%d, 8, 2], "size": [1, 4, 0], "uv": [40, 0]}]}`, i, i%8-4, i%8-4)
		if i > 0 {
			anims += ","
		}
		anims += fmt.Sprintf(`"feather%d": {"rotation": ["math.sin(q.anim_time*200+%d)*%d", 0, 0]}`, i, i*20, i+1)
	}
	anims += `}}`
	geo := `{"format_version": "1.12.0", "minecraft:geometry": [{"description": {"identifier": "geometry.t.walker", "texture_width": 64, "texture_height": 64},
		"bones": [` + bones + `]}]}`
	entity := func(id, scripts string) []byte {
		return []byte(`{"format_version": "1.10.0", "minecraft:client_entity": {"description": {"identifier": "` + id + `",
			"textures": {"default": "textures/entity/walker"}, "geometry": {"default": "geometry.t.walker"},
			"animations": {"move": "controller.animation.t.move", "idle": "animation.t.idle", "walk": "animation.t.walk", "feathers": "animation.t.feathers"}` +
			scripts + `}}}`)
	}
	return map[string][]byte{
		"manifest.json":              []byte(`{"header": {"name": "Animated", "uuid": "a"}}`),
		"textures/entity/walker.png": tex.Bytes(),
		"models/entity/walker.json":  []byte(geo),
		"animations/walker.json":     []byte(`{"format_version": "1.8.0", /* comment */ "animations": {` + anims + `}}`),
		"entity/walker.entity.json":  entity("t:walker", `, "scripts": {"animate": ["move", "feathers"], "pre_animation": ["v.moving = q.ground_speed > 0.5;"], "scale": "q.property('t:size') ?? 1"}`),
		"entity/statue.entity.json":  entity("t:statue", ``),
		"entity/sized.entity.json":   entity("t:sized", `, "scripts": {"scale": 1.5}`),
		"animation_controllers/w.json": []byte(`{"format_version": "1.10.0", "animation_controllers": {"controller.animation.t.move": {"initial_state": "idle", "states": {
			"idle": {"animations": ["idle"], "transitions": [{"walk": "v.moving"}], "blend_transition": 0.2},
			"walk": {"animations": ["walk"], "transitions": [{"idle": "!v.moving"}], "blend_transition": 0.2}}}}}`),
	}
}

func convertFiles(t *testing.T, files map[string][]byte, maxParts int) (*Result, map[string][]byte) {
	t.Helper()
	z := zipOf(t, files)
	src, err := ReadSource(bytes.NewReader(z), int64(len(z)), "a", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(&Input{Packs: []*Source{src}, MaxParts: maxParts})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(res.Zip), int64(len(res.Zip)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		out[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return res, out
}

type javaModel struct {
	Textures map[string]string `json:"textures"`
	Elements []struct {
		From, To [3]float64
		Rotation *struct {
			Origin [3]float64 `json:"origin"`
		} `json:"rotation"`
		Faces map[string]struct {
			UV      [4]float64 `json:"uv"`
			Texture string     `json:"texture"`
		} `json:"faces"`
	} `json:"elements"`
}

// checkModel checks what a Java 26 client insists on: elements inside -16..32, face UVs inside
// the texture, a texture that is in the pack.
func checkModel(t *testing.T, files map[string][]byte, id string) javaModel {
	t.Helper()
	ns, p, _ := strings.Cut(id, ":")
	if _, ok := files["assets/"+ns+"/items/"+p+".json"]; !ok {
		t.Fatalf("no item definition for %s", id)
	}
	raw, ok := files["assets/"+ns+"/models/"+p+".json"]
	if !ok {
		t.Fatalf("no model %s", id)
	}
	var m javaModel
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("model %s: %v", id, err)
	}
	if len(m.Elements) == 0 {
		t.Fatalf("model %s has no elements", id)
	}
	tns, tp, _ := strings.Cut(m.Textures["0"], ":")
	if _, ok := files["assets/"+tns+"/textures/"+tp+".png"]; !ok {
		t.Fatalf("model %s: texture %q is not in the pack", id, m.Textures["0"])
	}
	for i, e := range m.Elements {
		for a := 0; a < 3; a++ {
			if e.From[a] < -16 || e.To[a] > 32 || e.From[a] > e.To[a] {
				t.Fatalf("model %s element %d: %v..%v is outside -16..32", id, i, e.From, e.To)
			}
		}
		if len(e.Faces) == 0 {
			t.Fatalf("model %s element %d has no faces", id, i)
		}
		for name, f := range e.Faces {
			for _, v := range f.UV {
				if v < 0 || v > 16 {
					t.Fatalf("model %s element %d face %s: uv %v leaves the texture", id, i, name, f.UV)
				}
			}
		}
	}
	return m
}

func TestAnimatedEntity(t *testing.T) {
	res, files := convertFiles(t, AnimatedPack(30), 0)
	if res.Stats.Entities != 3 || res.Stats.Animated != 1 || len(res.Stats.Missing) != 0 {
		t.Fatalf("stats %+v", res.Stats)
	}
	// No scripts: the one model, as before.
	statue := res.Entities["t:statue"]
	if statue.Anim != nil || statue.Model == "" {
		t.Fatalf("statue: %+v", statue)
	}
	checkModel(t, files, statue.Model)
	// scripts.scale only: a rig for the scale, no parts.
	sized := res.Entities["t:sized"]
	if sized.Anim == nil || sized.Anim.Moves() || len(sized.Anim.Parts) != 0 {
		t.Fatalf("sized: %+v", sized.Anim)
	}
	st := sized.Anim.Rig().NewState(1)
	st.Step(0)
	if st.Scale() != 1.5 {
		t.Fatalf("sized: scripts.scale %v", st.Scale())
	}
	// The walker: parts within the budget, every model one a client takes.
	w := res.Entities["t:walker"]
	if w.Anim == nil || !w.Anim.Moves() {
		t.Fatal("walker is not animated")
	}
	if n := len(w.Anim.Parts); n != anim.MaxParts || res.Stats.Parts != n {
		t.Fatalf("walker has %d parts (stats %d), want the budget of %d", n, res.Stats.Parts, anim.MaxParts)
	}
	checkModel(t, files, w.Model)
	rig := w.Anim.Rig()
	elements := 0
	bones := map[string]anim.Part{}
	for _, p := range w.Anim.Parts {
		m := checkModel(t, files, p.Model)
		elements += len(m.Elements)
		if p.K <= 0 || p.K > 1 {
			t.Fatalf("part %s shrunk by %v", p.Model, p.K)
		}
		name := "root"
		if p.Bone >= 0 {
			name = rig.Bones[p.Bone].Name
		}
		bones[name] = p
	}
	if want := 6 + 30; elements != want {
		t.Fatalf("the parts have %d elements, the geometry %d cubes", elements, want)
	}
	for _, name := range []string{"body", "head", "tail", "leg_left", "leg_right", "feather29"} {
		if _, ok := bones[name]; !ok {
			t.Errorf("no part for %s (parts: %v)", name, bones)
		}
	}
	if _, ok := bones["hat"]; ok {
		t.Error("the hat has a part of its own")
	}
	// The tail is 60 pixels long: its model is shrunk, the display scale makes up for it.
	if k := bones["tail"].K; k >= 0.5 {
		t.Errorf("tail shrunk by %v", k)
	}
	// The head's model has the head's pivot at its centre and the bind rotation in the pose,
	// not in the model: its cube (y 16..22 around pivot y 16) is at 8..14, unrotated.
	hm := checkModel(t, files, bones["head"].Model)
	if e := hm.Elements[0]; e.Rotation != nil || e.From != [3]float64{5, 8, 5} || e.To != [3]float64{11, 14, 11} {
		t.Errorf("head element %+v", e)
	}
	st = rig.NewState(1)
	st.In = anim.Inputs{OnGround: true, GroundSpeed: 3}
	for i := 0; i < 10; i++ {
		st.Step(0.05)
	}
	if st.Playing() != "move=walk" || st.Scale() != 1 {
		t.Fatalf("walker plays %q at scale %v", st.Playing(), st.Scale())
	}
	// With room for every bone the cubes are spread over more parts.
	res2, files2 := convertFiles(t, AnimatedPack(30), 100)
	if n := len(res2.Entities["t:walker"].Anim.Parts); n != 5+30 {
		t.Fatalf("%d parts with room for all, want 35 (the hat rides the head)", n)
	}
	for _, p := range res2.Entities["t:walker"].Anim.Parts {
		checkModel(t, files2, p.Model)
	}
	// The conversion is deterministic.
	res3, _ := convertFiles(t, AnimatedPack(30), 0)
	if res3.SHA1 != res.SHA1 {
		t.Fatal("two conversions of the same pack differ")
	}
}
