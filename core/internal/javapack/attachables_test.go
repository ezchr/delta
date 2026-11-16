package javapack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"testing"
)

func TestMolang(t *testing.T) {
	for _, c := range []struct {
		expr string
		fp   bool
		want float64
		ok   bool
	}{
		{"c.is_first_person ? -125 : 90", false, 90, true},
		{"c.is_first_person ? -125 : 90", true, -125, true},
		{"query.is_first_person ? 0.5 : 1", false, 1, true},
		{"!variable.is_first_person ? (2 + 3) * 2 : 0", false, 10, true},
		{"-6", false, -6, true},
		{"math.sin(90) * 2", false, 2, true},
		{"1 < 2 && 3 >= 3", false, 1, true},
		{"query.anim_time * 30", false, 0, false},
	} {
		got, ok := evalMolang(c.expr, c.fp)
		if math.Abs(got-c.want) > 1e-9 || ok != c.ok {
			t.Errorf("%q (fp %v) = %v %v, want %v %v", c.expr, c.fp, got, ok, c.want, c.ok)
		}
	}
}

func near(a, b [3]float64) bool {
	return math.Abs(a[0]-b[0]) < 1e-6 && math.Abs(a[1]-b[1]) < 1e-6 && math.Abs(a[2]-b[2]) < 1e-6
}

func (d display) at(m [3]float64) [3]float64 {
	v := d.rot.apply([3]float64{m[0] - 8, m[1] - 8, m[2] - 8})
	return [3]float64{d.t[0] + d.s*v[0], d.t[1] + d.s*v[1], d.t[2] + d.s*v[2]}
}

// TestHeldPlacement checks that a held model's third person display puts each cube where Bedrock
// draws it: Java's hand (handR, handV) after the display transform lands on the Bedrock grip plus
// the posed geometry, and that the first person transform of vanilla's own pose is vanilla's.
func TestHeldPlacement(t *testing.T) {
	g := map[string]*geometry{}
	parseGeometries([]byte(`{"format_version":"1.12.0","minecraft:geometry":[{"description":{"identifier":"geometry.s","texture_width":16,"texture_height":16},
		"bones":[{"name":"rightitem","pivot":[0,0,0]},{"name":"root","parent":"rightitem","pivot":[0,0,0],"rotation":[0,-90,0]},
		{"name":"blade","parent":"root","pivot":[0,0,0],"cubes":[{"origin":[-1,-2,0],"size":[2,30,1],"uv":[0,0]}]}]}]}`), g)
	geo := g["geometry.s"]
	anim := map[string]boneAnim{"rightitem": {rot: [3]float64{90, 0, 0}, pos: [3]float64{0.3, -1, 1}}}
	m, ok := heldModel(geo, anim, texInfo{id: "x:y", size: image.Pt(16, 16)})
	if !ok {
		t.Fatal("no model")
	}
	// Rebuild what heldModel did and follow the cube's centre through both pipelines.
	pre := heldPre()
	ref := geo.elementsWith(affine{r: ident}, anim) // posed, Java model space
	q := [3]float64{(ref[0].from[0] + ref[0].to[0]) / 2, (ref[0].from[1] + ref[0].to[1]) / 2, (ref[0].from[2] + ref[0].to[2]) / 2}
	want := jmToEntity.apply(q)
	want = [3]float64{want[0] + bedrockGrip[0], want[1] + bedrockGrip[1], want[2] + bedrockGrip[2]}

	els := geo.elementsWith(pre, anim)
	best := ident
	if els[0].rotated {
		best = rotZ(els[0].rz).mul(rotY(els[0].ry)).mul(rotX(els[0].rx))
	}
	els = geo.elementsWith(affine{r: best.transpose()}.then(pre), anim)
	if els[0].rotated {
		t.Fatal("the display rotation should leave the blade unturned")
	}
	k, c := fit(els, true)
	tp, fp := heldDisplays(best, c, k, pre.t, heldFPScale) // shrunk about the grip here
	mc := [3]float64{(els[0].from[0] + els[0].to[0]) / 2, (els[0].from[1] + els[0].to[1]) / 2, (els[0].from[2] + els[0].to[2]) / 2}
	d := tp.at(mc)
	got := handR.apply([3]float64{handV[0] + d[0], handV[1] + d[1], handV[2] + d[2]})
	if !near(got, want) {
		t.Fatalf("cube centre in the arm at %v, Bedrock draws it at %v", got, want)
	}
	// First person: the grip stays where vanilla's pose puts the third person grip, the model
	// shrunk about it.
	gtp := pre.t // the grip in the item frame
	gm := [3]float64{8 + (best.transpose().apply(gtp)[0]-c[0])*k, 8 + (best.transpose().apply(gtp)[1]-c[1])*k, 8 + (best.transpose().apply(gtp)[2]-c[2])*k}
	gv := vanillaTP.rot.transpose().apply([3]float64{gtp[0] - vanillaTP.t[0], gtp[1] - vanillaTP.t[1], gtp[2] - vanillaTP.t[2]})
	gv = [3]float64{8 + gv[0]/vanillaTP.s, 8 + gv[1]/vanillaTP.s, 8 + gv[2]/vanillaTP.s}
	if !near(fp.at(gm), vanillaFP.at(gv)) {
		t.Fatalf("first person grip %v, want %v", fp.at(gm), vanillaFP.at(gv))
	}
	if math.Abs(fp.s-vanillaFP.s/vanillaTP.s*heldFPScale/k) > 1e-9 {
		t.Fatalf("first person scale %v", fp.s)
	}
	// The blade points forward (Java entity -z) from the hand in third person, as on Bedrock.
	if want[2] > bedrockGrip[2]-5 {
		t.Fatalf("blade not in front of the hand: %v", want)
	}
	disp := m["display"].(map[string]any)
	if disp["thirdperson_righthand"] == nil || disp["firstperson_righthand"] == nil {
		t.Fatalf("display %v", disp)
	}

	// Vanilla's own third person pose maps to vanilla's first person pose.
	van := vanillaTP.rot
	cc := van.transpose().apply(vanillaTP.t)
	tp2, fp2 := heldDisplays(van, cc, 1/vanillaTP.s, [3]float64{}, 1)
	if !near(tp2.t, vanillaTP.t) || math.Abs(tp2.s-vanillaTP.s) > 1e-9 {
		t.Fatalf("tp %+v", tp2)
	}
	if !near(fp2.t, vanillaFP.t) || math.Abs(fp2.s-vanillaFP.s) > 1e-9 || !isIdent(fp2.rot.transpose().mul(vanillaFP.rot)) {
		t.Fatalf("fp %+v, want %+v", fp2, vanillaFP)
	}
	for _, r := range []mat3{vanillaFP.rot, rotXYZ(30, 40, 50), rotXYZ(-10, 89, 5)} {
		x, y, z := eulerXYZ(r)
		if !isIdent(rotXYZ(x, y, z).transpose().mul(r)) {
			t.Fatalf("euler %v %v %v does not give back the rotation", x, y, z)
		}
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestArmorAndHeld converts an add-on whose armour is drawn with its own geometry (with the
// classic layer textures in a subpack) and a sword with its own held geometry.
func TestArmorAndHeld(t *testing.T) {
	att := func(id, geo, tex, extra string) []byte {
		return []byte(`{"format_version":"1.10.0","minecraft:attachable":{"description":{"identifier":"` + id + `",
			"textures":{"default":"` + tex + `","enchanted":"textures/misc/enchanted_item_glint"},
			"geometry":{"default":"` + geo + `"}` + extra + `}}}`)
	}
	files := map[string][]byte{
		"manifest.json": []byte(`{"header":{"name":"T"}}`),
		"textures/item_texture.json": []byte(`{"texture_data":{"sword":{"textures":"textures/items/sword"},
			"helm":{"textures":"textures/items/helm"}}}`),
		"textures/items/sword.png":                    pngOf(t, 16, 16),
		"textures/items/helm.png":                     pngOf(t, 16, 16),
		"textures/models/armor/x_armor.png":           pngOf(t, 128, 128),
		"textures/models/weapons/sword.png":           pngOf(t, 32, 32),
		"subpacks/flat/textures/models/armor/x_1.png": pngOf(t, 64, 32),
		"subpacks/flat/textures/models/armor/x_2.png": pngOf(t, 64, 32),
		"attachables/x_helmet.json":                   att("t:x_helmet", "geometry.x_helmet", "textures/models/armor/x_armor", ""),
		"attachables/x_chestplate.json":               att("t:x_chestplate", "geometry.x_chest", "textures/models/armor/x_armor", ""),
		"attachables/x_leggings.json":                 att("t:x_leggings", "geometry.x_legs", "textures/models/armor/x_armor", ""),
		"subpacks/flat/attachables/x_helmet.json":     att("t:x_helmet", "geometry.flat_helmet", "textures/models/helmet/x", ""),
		"subpacks/flat/attachables/x_chestplate.json": att("t:x_chestplate", "geometry.humanoid.armor.chestplate", "textures/models/armor/x_1", ""),
		"subpacks/flat/attachables/x_leggings.json":   att("t:x_leggings", "geometry.humanoid.armor.leggings", "textures/models/armor/x_2", ""),
		"attachables/sword.json": att("t:sword", "geometry.t_sword", "textures/models/weapons/sword",
			`,"animations":{"hold":"animation.t.hold"},"scripts":{"animate":["hold"]}`),
		"animations/t.json": []byte(`{"format_version":"1.10.0","animations":{"animation.t.hold":{"loop":true,"bones":{
			"rightitem":{"rotation":["c.is_first_person ? -125 : 90","c.is_first_person ? 120 : 0",0],"scale":"c.is_first_person ? 0.5 : 1"}}}}}`),
		"models/entity/sword.geo.json": []byte(`{"format_version":"1.12.0","minecraft:geometry":[{"description":{"identifier":"geometry.t_sword","texture_width":32,"texture_height":32},
			"bones":[{"name":"rightitem","pivot":[0,0,0]},{"name":"blade","parent":"rightitem","pivot":[0,0,0],"cubes":[{"origin":[-1,-2,0],"size":[2,20,1],"uv":[0,0]},
			{"origin":[-2,4,-0.5],"size":[4,1,2],"uv":[0,0],"pivot":[0,4,0],"rotation":[0,0,22.5]}]}]}]}`),
	}
	z := zipOf(t, files)
	s, err := ReadSource(bytes.NewReader(z), int64(len(z)), "u", "1", "")
	if err != nil {
		t.Fatal(err)
	}
	wear := func(slot string) map[string]any {
		return map[string]any{"components": map[string]any{"minecraft:wearable": map[string]any{"slot": slot, "protection": int32(3)},
			"minecraft:icon": map[string]any{"texture": "helm"}}}
	}
	res, err := Convert(&Input{Packs: []*Source{s}, Items: []Item{
		{ID: "t:x_helmet", Data: wear("slot.armor.head")},
		{ID: "t:x_chestplate", Data: wear("slot.armor.chest")},
		{ID: "t:x_leggings", Data: wear("slot.armor.legs")},
		{ID: "t:sword", Data: map[string]any{"components": map[string]any{"minecraft:icon": map[string]any{"texture": "sword"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, c, l := res.ItemEquip["t:x_helmet"], res.ItemEquip["t:x_chestplate"], res.ItemEquip["t:x_leggings"]
	if h.Slot != "head" || c.Slot != "chest" || l.Slot != "legs" {
		t.Fatalf("slots %+v %+v %+v", h, c, l)
	}
	if c.Asset == "" || h.Asset != c.Asset || l.Asset == "" || l.Asset == c.Asset {
		t.Fatalf("assets: helmet %q chest %q legs %q", h.Asset, c.Asset, l.Asset)
	}
	if _, ok := res.ItemEquip["t:sword"]; ok {
		t.Fatal("sword is not armour")
	}
	zr, err := zip.NewReader(bytes.NewReader(res.Zip), int64(len(res.Zip)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()
		got[f.Name] = buf.Bytes()
	}
	name := func(asset string) string { return asset[len(Namespace)+1:] }
	for _, p := range []string{
		"assets/delta/equipment/" + name(c.Asset) + ".json",
		"assets/delta/equipment/" + name(l.Asset) + ".json",
		"assets/delta/textures/entity/equipment/humanoid/" + name(c.Asset) + ".png",
		"assets/delta/textures/entity/equipment/humanoid_leggings/" + name(l.Asset) + ".png",
		"assets/delta/models/item/t/sword_held.json",
		"assets/delta/items/t/sword.json",
	} {
		if got[p] == nil {
			t.Errorf("missing %s", p)
		}
	}
	var eq map[string]map[string][]map[string]string
	_ = json.Unmarshal(got["assets/delta/equipment/"+name(l.Asset)+".json"], &eq)
	if eq["layers"]["humanoid_leggings"][0]["texture"] != l.Asset {
		t.Fatalf("leggings asset %s", got["assets/delta/equipment/"+name(l.Asset)+".json"])
	}
	var def map[string]map[string]any
	_ = json.Unmarshal(got["assets/delta/items/t/sword.json"], &def)
	if def["model"]["type"] != "minecraft:select" || def["model"]["property"] != "minecraft:display_context" {
		t.Fatalf("sword item definition %s", got["assets/delta/items/t/sword.json"])
	}
	var held struct {
		Elements []struct {
			From, To []float64
		} `json:"elements"`
		Display map[string]struct{ Rotation, Translation, Scale []float64 } `json:"display"`
	}
	_ = json.Unmarshal(got["assets/delta/models/item/t/sword_held.json"], &held)
	if len(held.Elements) != 2 || len(held.Display["thirdperson_righthand"].Scale) != 3 {
		t.Fatalf("held model %s", got["assets/delta/models/item/t/sword_held.json"])
	}
	for _, e := range held.Elements {
		for i := 0; i < 3; i++ {
			if e.From[i] < -16 || e.To[i] > 32 {
				t.Fatalf("element outside the model box: %v %v", e.From, e.To)
			}
		}
	}
	if res.ItemModels["t:sword"] == "" || res.Stats.Held != 1 || res.Stats.Equipment != 3 {
		t.Fatalf("models %v stats %+v", res.ItemModels, res.Stats)
	}
}
