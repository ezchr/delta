package packconv

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"delta/packconv/render"
)

// Fixture packs: tiny packs written here (no third-party content).

// testPNG is a w x h texture where every pixel has its own colour (so any UV mistake shows).
func testPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(40 + x*211/w), uint8(40 + y*211/h), uint8((x*7 + y*13) % 256), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func manifest(name, kind string) []byte {
	return []byte(fmt.Sprintf(`{"format_version":2,"header":{"name":%q,"uuid":"00000000-0000-0000-0000-%012d","version":[1,0,0]},
		"modules":[{"type":%q,"uuid":"00000000-0000-0000-0001-000000000000","version":[1,0,0]}]}`, name, len(name), kind))
}

// fixtureRP is a resource pack with one of everything.
func fixtureRP() map[string][]byte {
	return map[string][]byte{
		"manifest.json": manifest("Fixture RP", "resources"),
		// Lenient JSON on purpose: comments and trailing commas.
		"textures/item_texture.json": []byte(`{
			// item icons
			"resource_pack_name": "fixture", "texture_name": "atlas.items",
			"texture_data": {
				"fx_gem": {"textures": "textures/items/gem"},
				"fx_blade": {"textures": ["textures/items/blade"]},
				"fx_helmet": {"textures": "textures/items/helmet"},
				"fx_leggings": {"textures": "textures/items/leggings"},
			}
		}`),
		"textures/terrain_texture.json": []byte(`{"texture_data": {
			"fx_ore": {"textures": "textures/blocks/fx_ore"},
			"fx_ore_top": {"textures": "textures/blocks/fx_ore_top"},
			"fx_ore_lit": {"textures": "textures/blocks/fx_ore_lit"},
			"cobblestone": {"textures": "textures/blocks/my_cobble"}
		}}`),
		"textures/items/gem.png":         testPNG(16, 16),
		"textures/items/blade.png":       testPNG(16, 16),
		"textures/items/helmet.png":      testPNG(16, 16),
		"textures/items/leggings.png":    testPNG(16, 16),
		"textures/models/blade.png":      testPNG(32, 32),
		"textures/models/armor/fx_1.png": testPNG(64, 32),
		"textures/models/armor/fx_2.png": testPNG(64, 32),
		"textures/blocks/fx_ore.png":     testPNG(16, 16),
		"textures/blocks/fx_ore_top.png": testPNG(16, 16),
		"textures/blocks/fx_ore_lit.png": testPNG(16, 16),
		"textures/blocks/stone.png":      testPNG(16, 16), // a vanilla retexture by path
		"textures/blocks/my_cobble.png":  testPNG(16, 16), // a vanilla retexture by key
		"textures/entity/critter.png":    testPNG(64, 32),
		"attachables/blade.json": []byte(`{"format_version":"1.10.0","minecraft:attachable":{"description":{
			"identifier":"fx:blade","textures":{"default":"textures/models/blade"},"geometry":{"default":"geometry.fx_blade"},
			"render_controllers":["controller.render.default"]}}}`),
		"attachables/helmet.json": []byte(`{"format_version":"1.8.0","minecraft:attachable":{"description":{
			"identifier":"fx:helmet","textures":{"default":"textures/models/armor/fx_1"},"geometry":{"default":"geometry.humanoid.armor.helmet"}}}}`),
		"attachables/leggings.json": []byte(`{"format_version":"1.8.0","minecraft:attachable":{"description":{
			"identifier":"fx:leggings","textures":{"default":"textures/models/armor/fx_2"},"geometry":{"default":"geometry.humanoid.armor.leggings"}}}}`),
		// A blade: a handle and a rotated guard and blade, box UV and per-face UV, a mirrored cube.
		"models/entity/blade.geo.json": []byte(`{"format_version":"1.12.0","minecraft:geometry":[{"description":{
			"identifier":"geometry.fx_blade","texture_width":32,"texture_height":32},
			"bones":[{"name":"handle","pivot":[0,0,0],"cubes":[{"origin":[-1,0,-1],"size":[2,6,2],"uv":[0,0]}]},
			{"name":"guard","parent":"handle","pivot":[0,6,0],"rotation":[0,20,0],"cubes":[{"origin":[-3,6,-1],"size":[6,1,2],"uv":[8,0],"mirror":true}]},
			{"name":"blade","parent":"guard","pivot":[0,7,0],"rotation":[10,0,5],"cubes":[{"origin":[-1,7,-0.5],"size":[2,12,1],
				"uv":{"north":{"uv":[0,10],"uv_size":[2,12]},"south":{"uv":[2,10],"uv_size":[2,12]},"east":{"uv":[4,10],"uv_size":[1,12]},
				"west":{"uv":[5,10],"uv_size":[1,12]},"up":{"uv":[6,10],"uv_size":[2,1]},"down":{"uv":[8,10],"uv_size":[2,1]}}}]}]}]}`),
		// A critter: body and a turned head (legacy 1.8 format).
		"models/entity/critter.geo.json": []byte(`{"format_version":"1.8.0","geometry.fx_critter":{"texturewidth":64,"textureheight":32,
			"bones":[{"name":"body","pivot":[0,6,0],"cubes":[{"origin":[-4,2,-6],"size":[8,6,12],"uv":[0,0]}]},
			{"name":"head","parent":"body","pivot":[0,8,-6],"rotation":[15,30,0],"cubes":[{"origin":[-3,6,-12],"size":[6,6,6],"uv":[40,0]}]}]}}`),
		"entity/critter.entity.json": []byte(`{"format_version":"1.10.0","minecraft:client_entity":{"description":{
			"identifier":"fx:critter","textures":{"default":"textures/entity/critter"},"geometry":{"default":"geometry.fx_critter"},
			"animations":{"walk":"animation.fx.walk"},"render_controllers":["controller.render.default"]}}}`),
		"blocks.json":      []byte(`{"format_version":[1,1,0],"fx:ore":{"textures":{"up":"fx_ore_top","down":"fx_ore_top","side":"fx_ore"},"sound":"stone"}}`),
		"texts/en_US.lang": []byte("item.fx:gem.name=Fixture Gem\nitem.fx:blade=Fixture Blade\ntile.fx:ore.name=Fixture Ore\t## comment\nentity.fx:critter.name=Critter\n"),
		"texts/de_DE.lang": []byte("item.fx:gem.name=Testjuwel\n"),
		"sounds/sound_definitions.json": []byte(`{"format_version":"1.14.0","sound_definitions":{
			"fx.chirp":{"category":"neutral","sounds":["sounds/fx/chirp",{"name":"sounds/fx/chirp2","volume":0.5}]},
			"fx.old":{"category":"neutral","sounds":["sounds/fx/old"]}}}`),
		"sounds/fx/chirp.ogg":  []byte("OggS-fixture-1"),
		"sounds/fx/chirp2.ogg": []byte("OggS-fixture-2"),
		"sounds/fx/old.fsb":    []byte("FSB5"),
		"particles/p.json":     []byte(`{}`),
		"ui/hud_screen.json":   []byte(`{}`),
	}
}

func fixtureBP() map[string][]byte {
	return map[string][]byte{
		"manifest.json": manifest("Fixture BP", "data"),
		"items/gem.json": []byte(`{"format_version":"1.20.50","minecraft:item":{"description":{"identifier":"fx:gem"},
			"components":{"minecraft:icon":"fx_gem","minecraft:max_stack_size":16}}}`),
		"items/blade.json": []byte(`{"format_version":"1.20.50","minecraft:item":{"description":{"identifier":"fx:blade"},
			"components":{"minecraft:icon":{"textures":{"default":"fx_blade"}},"minecraft:max_stack_size":1,"minecraft:hand_equipped":true,
			"minecraft:durability":{"max_durability":250},"minecraft:tags":{"tags":["minecraft:is_sword"]},
			"minecraft:display_name":{"value":"item.fx:blade"}}}}`),
		"items/helmet.json": []byte(`{"format_version":"1.20.50","minecraft:item":{"description":{"identifier":"fx:helmet"},
			"components":{"minecraft:icon":"fx_helmet","minecraft:wearable":{"slot":"slot.armor.head","protection":3},"minecraft:durability":{"max_durability":300}}}}`),
		"items/leggings.json": []byte(`{"format_version":"1.20.50","minecraft:item":{"description":{"identifier":"fx:leggings"},
			"components":{"minecraft:icon":"fx_leggings","minecraft:wearable":{"slot":"slot.armor.legs","protection":5}}}}`),
		"items/snack.json": []byte(`{"format_version":"1.20.50","minecraft:item":{"description":{"identifier":"fx:snack"},
			"components":{"minecraft:icon":"apple","minecraft:food":{"nutrition":4,"saturation_modifier":0.6},"minecraft:use_modifiers":{"use_duration":1.2}}}}`),
		// A block with a facing state (turned by permutations) and a lit state (another texture).
		"blocks/ore.json": []byte(`{"format_version":"1.20.60","minecraft:block":{"description":{"identifier":"fx:ore",
			"states":{"fx:lit":[false,true]},
			"traits":{"minecraft:placement_direction":{"enabled_states":["minecraft:cardinal_direction"]}}},
			"components":{"minecraft:material_instances":{"*":{"texture":"fx_ore"},"up":{"texture":"fx_ore_top"},"down":{"texture":"fx_ore_top"}}},
			"permutations":[
				{"condition":"q.block_state('minecraft:cardinal_direction') == 'east'","components":{"minecraft:transformation":{"rotation":[0,90,0]}}},
				{"condition":"q.block_state('fx:lit') && q.block_state('minecraft:cardinal_direction') == 'north'","components":{"minecraft:material_instances":{"*":{"texture":"fx_ore_lit"}}}}
			]}}`),
	}
}

func fixtureInput(t *testing.T) *Input {
	rp := NewSource(fixtureRP())
	bp := NewSource(fixtureBP())
	if rp.Type != "resources" || !bp.IsBehaviour() {
		t.Fatalf("pack types %q %q", rp.Type, bp.Type)
	}
	return &Input{Packs: []*Source{rp}, BehaviorPacks: []*Source{bp}, Slots: Candidates()}
}

func convertFixture(t *testing.T) (*Result, map[string][]byte) {
	t.Helper()
	res, err := Convert(fixtureInput(t))
	if err != nil {
		t.Fatal(err)
	}
	return res, unzip(t, res.Zip)
}

func unzip(t *testing.T, z []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func jsonOf(t *testing.T, files map[string][]byte, name string) map[string]any {
	t.Helper()
	b, ok := files[name]
	if !ok {
		t.Fatalf("%s missing from the pack", name)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func TestFixtureValidJSONAndDeterministic(t *testing.T) {
	res, files := convertFixture(t)
	for n, b := range files {
		if strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".mcmeta") {
			if !json.Valid(b) {
				t.Errorf("%s: invalid JSON", n)
			}
		}
	}
	res2, _ := convertFixture(t)
	if res.SHA1 != res2.SHA1 || !bytes.Equal(res.Zip, res2.Zip) {
		t.Fatalf("not deterministic: %s vs %s", res.SHA1, res2.SHA1)
	}
	if !bytes.Equal(res.Mapping.MarshalIndent(), res2.Mapping.MarshalIndent()) {
		t.Fatal("mapping not deterministic")
	}
	mc := jsonOf(t, files, "pack.mcmeta")["pack"].(map[string]any)
	if mc["min_format"].(float64) != float64(DefaultMinFormat) {
		t.Fatalf("pack.mcmeta %v", mc)
	}
}

func TestFixtureFlatItem(t *testing.T) {
	res, files := convertFixture(t)
	im := res.Mapping.Items["fx:gem"]
	if im == nil || im.JavaBase != "minecraft:paper" || im.Model != "flat" || im.MaxStack != 16 || im.Name != "Fixture Gem" || im.Source != "behavior_pack" {
		t.Fatalf("gem mapping %+v", im)
	}
	def := jsonOf(t, files, "assets/delta/items/fx/gem.json")
	model := def["model"].(map[string]any)["model"].(string)
	m := jsonOf(t, files, "assets/delta/models/"+strings.TrimPrefix(model, "delta:")+".json")
	if m["parent"] != "minecraft:item/generated" {
		t.Fatalf("gem model %v", m)
	}
	layer := m["textures"].(map[string]any)["layer0"].(string)
	if !bytes.Equal(files["assets/delta/textures/"+strings.TrimPrefix(layer, "delta:")+".png"], fixtureRP()["textures/items/gem.png"]) {
		t.Fatal("gem texture not copied as is")
	}
	if res.ItemModels["fx:gem"] != im.ItemModel || res.ItemNames["fx:gem"] != "Fixture Gem" {
		t.Fatal("compat tables")
	}
	// The vanilla icon falls back to the client's own texture.
	sn := res.Mapping.Items["fx:snack"]
	if sn.JavaBase != "minecraft:bread" || sn.Food == nil || sn.Food.Nutrition != 4 || sn.Food.UseSeconds != 1.2 {
		t.Fatalf("snack %+v %+v", sn, sn.Food)
	}
	sm := jsonOf(t, files, "assets/delta/models/item/fx/snack.json")
	if sm["textures"].(map[string]any)["layer0"] != "minecraft:item/apple" {
		t.Fatalf("snack icon %v", sm)
	}
	// Languages.
	de := jsonOf(t, files, "assets/delta/lang/de_de.json")
	if de["item.fx:gem.name"] != "Testjuwel" {
		t.Fatalf("de_de %v", de)
	}
	if jsonOf(t, files, "assets/delta/lang/en_us.json")["tile.fx:ore.name"] != "Fixture Ore" {
		t.Fatal("lang tab comment not cut")
	}
}

// renderDiff draws the Bedrock geometry and a converted Java model from every sheet view and
// returns the worst share of differing pixels.
func renderDiff(t *testing.T, files map[string][]byte, geoFile, geoID string, tex []byte, modelID string, o render.Options) float64 {
	t.Helper()
	g, ok := render.ParseBedrockGeometry(CleanJSON(fixtureRP()[geoFile]), geoID)
	if !ok {
		t.Fatalf("geometry %s", geoID)
	}
	ti, _ := png.Decode(bytes.NewReader(tex))
	timg := toNRGBA(ti)
	bq := g.Quads(timg, o)
	load := func(id string) ([]byte, bool) {
		ns, p, _ := strings.Cut(id, ":")
		b, ok := files["assets/"+ns+"/models/"+p+".json"]
		return b, ok
	}
	jm, err := render.LoadJavaModel(modelID, load)
	if err != nil {
		t.Fatal(err)
	}
	jq := jm.Quads(func(id string) *image.NRGBA {
		ns, p, _ := strings.Cut(id, ":")
		b, ok := files["assets/"+ns+"/textures/"+p+".png"]
		if !ok {
			return nil
		}
		img, _ := png.Decode(bytes.NewReader(b))
		return toNRGBA(img)
	})
	if len(jq) == 0 || len(bq) == 0 {
		t.Fatalf("nothing drawn: %d bedrock, %d java quads", len(bq), len(jq))
	}
	blo, bhi := render.Bounds(bq)
	jlo, jhi := render.Bounds(jq)
	worst := 0.0
	for _, v := range render.Views {
		worst = max(worst, render.Diff(render.Draw(bq, v, 128, blo, bhi), render.Draw(jq, v, 128, jlo, jhi)))
	}
	return worst
}

func TestFixtureAttachableItem(t *testing.T) {
	res, files := convertFixture(t)
	im := res.Mapping.Items["fx:blade"]
	if im == nil || im.Kind != "sword" || im.JavaBase != "minecraft:diamond_sword" || im.Model != "3d" || im.MaxDamage != 250 || im.MaxStack != 1 {
		t.Fatalf("blade %+v", im)
	}
	if im.Name != "Fixture Blade" || im.NameKey != "item.fx:blade" {
		t.Fatalf("blade name %q %q", im.Name, im.NameKey)
	}
	def := jsonOf(t, files, "assets/delta/items/fx/blade.json")["model"].(map[string]any)
	if def["type"] != "minecraft:select" || def["property"] != "minecraft:display_context" {
		t.Fatalf("blade definition %v", def)
	}
	held := def["fallback"].(map[string]any)["model"].(string)
	m := jsonOf(t, files, "assets/delta/models/"+strings.TrimPrefix(held, "delta:")+".json")
	if _, ok := m["display"].(map[string]any)["firstperson_righthand"]; !ok {
		t.Fatal("no display transforms")
	}
	// The Java model looks like the Bedrock geometry (turned half a turn, as held models are).
	d := renderDiff(t, files, "models/entity/blade.geo.json", "geometry.fx_blade", fixtureRP()["textures/models/blade.png"], held,
		render.Options{Post: render.RotY(180)})
	if d > 0.002 {
		t.Fatalf("blade: %.2f%% of pixels differ from the Bedrock geometry", d*100)
	}
}

func TestFixtureArmour(t *testing.T) {
	res, files := convertFixture(t)
	h, l := res.Mapping.Items["fx:helmet"], res.Mapping.Items["fx:leggings"]
	if h.Equipment == nil || h.Equipment.Slot != "head" || h.JavaBase != "minecraft:diamond_helmet" || l.Equipment == nil || l.Equipment.Slot != "legs" {
		t.Fatalf("armour %+v %+v", h, l)
	}
	if h.Equipment.Asset == "" || h.Equipment.Asset != l.Equipment.Asset {
		t.Fatalf("assets %q %q", h.Equipment.Asset, l.Equipment.Asset)
	}
	name := strings.TrimPrefix(h.Equipment.Asset, "delta:")
	eq := jsonOf(t, files, "assets/delta/equipment/"+name+".json")["layers"].(map[string]any)
	if eq["humanoid"] == nil || eq["humanoid_leggings"] == nil {
		t.Fatalf("layers %v", eq)
	}
	hum, _ := png.Decode(bytes.NewReader(files["assets/delta/textures/entity/equipment/humanoid/"+name+".png"]))
	src, _ := png.Decode(bytes.NewReader(fixtureRP()["textures/models/armor/fx_1.png"]))
	// The helmet region is copied, the chestplate region (no chestplate) stays empty.
	if !sameColour(hum.At(3, 5), src.At(3, 5)) {
		t.Fatal("helmet region not copied")
	}
	if _, _, _, a := hum.At(20, 20).RGBA(); a != 0 {
		t.Fatal("chestplate region filled without a chestplate")
	}
	legs := files["assets/delta/textures/entity/equipment/humanoid_leggings/"+name+".png"]
	li, _ := png.Decode(bytes.NewReader(legs))
	ls, _ := png.Decode(bytes.NewReader(fixtureRP()["textures/models/armor/fx_2.png"]))
	if !sameColour(li.At(10, 20), ls.At(10, 20)) {
		t.Fatal("leggings layer not copied")
	}
}

func TestFixtureBlock(t *testing.T) {
	res, files := convertFixture(t)
	bm := res.Mapping.Blocks["fx:ore"]
	if bm == nil || bm.Slot < 0 || !bm.Solid || bm.Name != "Fixture Ore" {
		t.Fatalf("ore %+v", bm)
	}
	// Looks: default (north/unlit), east (turned), lit+north (other texture).
	if len(bm.Variants) == 0 {
		t.Fatal("no variants")
	}
	var turned, lit bool
	for _, v := range bm.Variants {
		if v.States["minecraft:cardinal_direction"] == "east" && v.States["fx:lit"] == false {
			turned = true
		}
		if v.States["minecraft:cardinal_direction"] == "north" && v.States["fx:lit"] == true {
			lit = true
		}
		if v.States["minecraft:cardinal_direction"] == "south" {
			t.Fatalf("south looks like the default but has a variant: %+v", v)
		}
	}
	if !turned || !lit {
		t.Fatalf("variants %+v", bm.Variants)
	}
	slot := Candidates()[bm.Slot]
	if bm.JavaState != slot.StateString() || res.Blocks["fx:ore"] != bm.Slot {
		t.Fatal("slot and state disagree")
	}
	bs := jsonOf(t, files, "assets/minecraft/blockstates/"+strings.TrimPrefix(slot.Block, "minecraft:")+".json")
	raw, _ := json.Marshal(bs)
	if !strings.Contains(string(raw), `"y":90`) {
		t.Fatalf("no turned state in %s", raw)
	}
	model := jsonOf(t, files, "assets/delta/models/block/fx/ore.json")
	if model["parent"] != "minecraft:block/cube" {
		t.Fatalf("ore model %v", model)
	}
	if bm.ItemModel == "" || res.ItemModels["fx:ore"] != bm.ItemModel {
		t.Fatal("block item")
	}
}

func TestFixtureEntity(t *testing.T) {
	res, files := convertFixture(t)
	e := res.Entities["fx:critter"]
	if e.Model == "" || len(e.Parts) != 2 || len(e.Bones) != 2 || e.Name != "Critter" {
		t.Fatalf("critter %+v", e)
	}
	em := res.Mapping.Entities["fx:critter"]
	if em.ModelYaw != 180 || len(em.Unsupported) != 1 || em.Parts[0].Display != "fixed" {
		t.Fatalf("critter mapping %+v", em)
	}
	// The head's pivot (0, 8, -6) in Bedrock pixels is (0, 0.5, 0.375) blocks for an entity facing
	// south.
	var head EntityPart
	for _, p := range e.Parts {
		if p.Bone == "head" {
			head = p
		}
	}
	if head.Offset != [3]float64{0, 0.5, 0.375} || head.Parent != "body" {
		t.Fatalf("head part %+v", head)
	}
	// Whole model and each part look like the Bedrock geometry.
	tex := fixtureRP()["textures/entity/critter.png"]
	if d := renderDiff(t, files, "models/entity/critter.geo.json", "geometry.fx_critter", tex, e.Model, render.Options{}); d > 0.002 {
		t.Fatalf("critter whole: %.2f%% differ", d*100)
	}
	for _, p := range e.Parts {
		d := renderDiff(t, files, "models/entity/critter.geo.json", "geometry.fx_critter", tex, p.Model,
			render.Options{Bones: map[string]bool{strings.ToLower(p.Bone): true}})
		if d > 0.002 {
			t.Fatalf("critter %s: %.2f%% differ", p.Bone, d*100)
		}
		// The part's model has the pivot at (8, 8, 8): the head cube's corner nearest the pivot.
	}
}

func TestFixtureOverridesSoundsReport(t *testing.T) {
	res, files := convertFixture(t)
	if !bytes.Equal(files["assets/minecraft/textures/block/stone.png"], fixtureRP()["textures/blocks/stone.png"]) {
		t.Fatal("stone retexture missing")
	}
	if !bytes.Equal(files["assets/minecraft/textures/block/cobblestone.png"], fixtureRP()["textures/blocks/my_cobble.png"]) {
		t.Fatal("cobblestone key remap missing")
	}
	if _, ok := files["assets/minecraft/textures/block/fx_ore.png"]; ok {
		t.Fatal("custom block texture treated as an override")
	}
	sm := res.Mapping.Sounds["fx.chirp"]
	if sm == nil || sm.JavaEvent != "delta:fx.chirp" || sm.Files != 2 {
		t.Fatalf("sound %+v", sm)
	}
	if _, ok := res.Mapping.Sounds["fx.old"]; ok {
		t.Fatal(".fsb sound converted")
	}
	snd := jsonOf(t, files, "assets/delta/sounds.json")
	if snd["fx.chirp"] == nil {
		t.Fatalf("sounds.json %v", snd)
	}
	md := res.Report.Markdown()
	for _, want := range []string{"JSON UI screens", "particle effects", "fx.old", "animations not converted"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// TestRuntimeItems converts with items as the translator sees them: a network-form component
// map and an identifier alone.
func TestRuntimeItems(t *testing.T) {
	in := fixtureInput(t)
	in.BehaviorPacks = nil
	in.Items = []Item{
		{ID: "fx:gem", Data: map[string]any{"components": map[string]any{
			"item_properties": map[string]any{"minecraft:icon": map[string]any{"textures": map[string]any{"default": "fx_gem"}},
				"max_stack_size": int32(8), "hand_equipped": uint8(0)},
		}}},
		{ID: "fx:blade", Data: map[string]any{"components": map[string]any{
			"item_properties":      map[string]any{"minecraft:icon": map[string]any{"textures": map[string]any{"default": "fx_blade"}}, "hand_equipped": uint8(1), "enchantable_slot": "sword"},
			"minecraft:durability": map[string]any{"max_durability": int32(99)},
		}}},
		{ID: "fx:leggings"}, // identifier only: icon guessed, slot from the armour attachable
	}
	res, err := Convert(in)
	if err != nil {
		t.Fatal(err)
	}
	g, b, l := res.Mapping.Items["fx:gem"], res.Mapping.Items["fx:blade"], res.Mapping.Items["fx:leggings"]
	if g.MaxStack != 8 || g.Source != "runtime" || g.Model != "flat" {
		t.Fatalf("gem %+v", g)
	}
	if b.Kind != "sword" || b.MaxDamage != 99 || b.Model != "3d" {
		t.Fatalf("blade %+v", b)
	}
	if l.Kind != "leggings" || l.Equipment == nil || l.Equipment.Asset == "" || l.Model != "flat" {
		t.Fatalf("leggings %+v", l)
	}
}

func TestSubpackAndZip(t *testing.T) {
	files := fixtureRP()
	files["subpacks/hd/textures/items/gem.png"] = testPNG(32, 32)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create("Fixture/" + n)
		_, _ = w.Write(b)
	}
	_ = zw.Close()
	s, err := ReadSource(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Fixture RP" || s.Version != "1.0.0" {
		t.Fatalf("manifest %q %q", s.Name, s.Version)
	}
	if !s.SetSubpack("hd") {
		t.Fatal("subpack not found")
	}
	b, _ := s.File("textures/items/gem.png")
	cfg, _ := png.DecodeConfig(bytes.NewReader(b))
	if cfg.Width != 32 {
		t.Fatal("subpack file does not override")
	}
	n := 0
	for _, f := range s.Files("textures/items/") {
		if f == "textures/items/gem.png" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("gem listed %d times", n)
	}
}

func TestLenientJSON(t *testing.T) {
	var v map[string]any
	in := "\xef\xbb\xbf{ // c\n \"a\": [1, 2, /* x */ ], \"b\": \"//not a comment\", /* y */ }"
	if err := json.Unmarshal(stripJSON([]byte(in)), &v); err != nil {
		t.Fatal(err)
	}
	if v["b"] != "//not a comment" {
		t.Fatalf("%v", v)
	}
}

func TestMolang(t *testing.T) {
	st := map[string]any{"a:lit": true, "minecraft:cardinal_direction": "east", "a:n": float64(3)}
	for expr, want := range map[string]bool{
		"q.block_state('a:lit')":      true,
		"!query.block_state('a:lit')": false,
		"q.block_state('minecraft:cardinal_direction') == 'east' && q.block_state('a:n') >= 3": true,
		"q.block_state('a:n') == 2 || q.block_state('a:n') == 4":                               false,
		"(q.block_state('a:n') + 1) * 2 == 8 ? 1 : 0":                                          true,
		"query.block_property('a:lit') == true":                                                true,
	} {
		got, err := evalCondition(expr, st)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", expr, got, err, want)
		}
	}
	if _, err := evalCondition("q.is_sneaking", st); err == nil {
		t.Error("unsupported query accepted")
	}
}

func TestEncryptedDetected(t *testing.T) {
	hdr := make([]byte, 300)
	hdr[4], hdr[5], hdr[6], hdr[7] = 0xFC, 0xB9, 0xCF, 0x9B
	s := NewSource(map[string][]byte{"manifest.json": manifest("Enc", "resources"), "contents.json": hdr})
	if !s.Encrypted {
		t.Fatal("not detected")
	}
	res, err := Convert(&Input{Packs: []*Source{s}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Count(catPack, Skipped) != 1 {
		t.Fatal("not reported")
	}
}

func sameColour(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}
