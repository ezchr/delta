package javapack

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

// TestVanillaShapes: entities whose geometry only the vanilla pack has.
func TestVanillaShapes(t *testing.T) {
	var tex bytes.Buffer
	_ = png.Encode(&tex, image.NewNRGBA(image.Rect(0, 0, 64, 64)))
	entity := func(id, geo, textures, rc string) []byte {
		return []byte(`{"format_version": "1.10.0", "minecraft:client_entity": {"description": {"identifier": "` + id + `",
			"textures": {` + textures + `}, "geometry": {"default": "` + geo + `"}, "render_controllers": [` + rc + `]}}}`)
	}
	res, files := convertFiles(t, map[string][]byte{
		"manifest.json":               []byte(`{"header": {"name": "Shapes", "uuid": "a"}}`),
		"textures/entity/skins/a.png": tex.Bytes(),
		"textures/entity/skins/b.png": tex.Bytes(),
		"textures/entity/guild.png":   tex.Bytes(),
		"entity/nurse.entity.json":    entity("t:nurse", "geometry.villager_v2", `"default": "textures/entity/villager2/villager"`, `"controller.render.default"`),
		"entity/guild.entity.json":    entity("t:guild", "geometry.villager.v1.8", `"default": "textures/entity/guild"`, `"controller.render.default"`),
		"entity/trainer.entity.json": entity("t:trainer", "geometry.humanoid.custom",
			`"a": "textures/entity/skins/a", "b": "textures/entity/skins/b", "steve": "textures/entity/steve"`, `{"controller.render.t": "1"}`),
		"entity/slim.entity.json":    entity("t:slim", "geometry.humanoid.customSlim", `"default": "textures/entity/skins/b"`, `"controller.render.default"`),
		"entity/unknown.entity.json": entity("t:unknown", "geometry.no_such_shape", `"default": "textures/entity/guild"`, ``),
		"render_controllers/t.rc.json": []byte(`{"format_version": "1.10.0", "render_controllers": {"controller.render.t": {"arrays": {"textures": {
			"Array.skins": ["Texture.a", "Texture.b", "Texture.steve"]}}, "geometry": "Geometry.default", "textures": ["Array.skins[q.property('t:trainer')]"]}}}`),
	}, 0)
	if res.Stats.StandIns != 3 || res.Stats.Entities != 1 || len(res.Stats.Missing) != 1 {
		t.Fatalf("stats %+v", res.Stats)
	}
	if st := res.StandIns["t:nurse"]; st.Java != "minecraft:villager" || len(st.Skins) != 0 {
		t.Fatalf("nurse: %+v", st)
	}
	// A villager shape with the pack's own texture: a model from the shape's geometry.
	guild, ok := res.Entities["t:guild"]
	if _, stand := res.StandIns["t:guild"]; !ok || stand {
		t.Fatalf("guild: entity %v, stand-in %v", ok, stand)
	}
	if m := checkModel(t, files, guild.Model); len(m.Elements) != 9 {
		t.Fatalf("guild model has %d elements, want the villager's 9 cubes", len(m.Elements))
	}
	// A humanoid: a mannequin with the skins of the render controller's array and its index.
	tr := res.StandIns["t:trainer"]
	if tr.Java != "minecraft:mannequin" || tr.Slim || tr.SkinIndex != "q.property('t:trainer')" || len(tr.Skins) != 3 || tr.Skins[2] != "" {
		t.Fatalf("trainer: %+v", tr)
	}
	for _, id := range tr.Skins[:2] {
		ns, p, _ := strings.Cut(id, ":")
		if _, ok := files["assets/"+ns+"/textures/"+p+".png"]; !ok || id == "" {
			t.Fatalf("skin %q is not in the pack", id)
		}
	}
	if tr.Skins[0] == tr.Skins[1] {
		t.Fatal("two skins share a texture")
	}
	if st := res.StandIns["t:slim"]; !st.Slim || len(st.Skins) != 1 || st.Skins[0] != tr.Skins[1] || st.SkinIndex != "" {
		t.Fatalf("slim: %+v", st)
	}
}
