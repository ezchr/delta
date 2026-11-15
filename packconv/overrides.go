package packconv

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"
)

// Vanilla texture overrides: a Bedrock pack's textures/blocks/<name> or textures/items/<name>
// replaces the Java texture of the same thing. Most names are the same on both editions; the
// rest follow a few patterns (planks_oak -> oak_planks, wool_colored_red -> red_wool, silver ->
// light_gray...). A name is only used when Java 26.3 has that texture (javatextures_gen.go).

var javaTextureSet = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(javaTextureNames) {
		m[n] = true
	}
	return m
})

// renames are Bedrock texture names whose Java name follows no pattern.
var renames = map[string]string{
	"block/grass_side_carried": "grass_block_side", "block/grass_top": "grass_block_top",
	"block/grass_side": "grass_block_side_overlay", "block/stonebrick": "stone_bricks",
	"block/stonebrick_mossy": "mossy_stone_bricks", "block/stonebrick_cracked": "cracked_stone_bricks",
	"block/stonebrick_carved": "chiseled_stone_bricks", "block/brick": "bricks",
	"block/hardened_clay": "terracotta", "block/cobblestone_mossy": "mossy_cobblestone",
	"block/stone_slab_top": "smooth_stone", "block/stone_slab_side": "smooth_stone_slab_side",
	"block/quartz_block_lines": "quartz_pillar", "block/quartz_block_lines_top": "quartz_pillar_top",
	"block/quartz_block_chiseled": "chiseled_quartz_block", "block/quartz_block_chiseled_top": "chiseled_quartz_block_top",
	"block/sandstone_normal": "sandstone", "block/sandstone_carved": "chiseled_sandstone",
	"block/sandstone_smooth": "cut_sandstone", "block/red_sandstone_normal": "red_sandstone",
	"block/red_sandstone_carved": "chiseled_red_sandstone", "block/red_sandstone_smooth": "cut_red_sandstone",
	"block/nether_brick": "nether_bricks", "block/red_nether_brick": "red_nether_bricks",
	"block/end_bricks": "end_stone_bricks", "block/prismarine_rough": "prismarine",
	"block/prismarine_dark": "dark_prismarine", "block/glass_pane_top": "glass_pane_top",
	"block/sponge_wet": "wet_sponge", "block/web": "cobweb", "block/mob_spawner": "spawner",
	"block/portal": "nether_portal", "block/reeds": "sugar_cane", "block/waterlily": "lily_pad",
	"block/deadbush": "dead_bush", "block/tallgrass": "short_grass", "block/flower_rose": "poppy",
	"block/flower_dandelion": "dandelion", "block/noteblock": "note_block", "block/melon_side": "melon_side",
	"block/pumpkin_face_off": "carved_pumpkin", "block/pumpkin_face_on": "jack_o_lantern",
	"block/trapdoor": "oak_trapdoor", "block/iron_trapdoor": "iron_trapdoor", "block/door_wood_lower": "oak_door_bottom",
	"block/door_wood_upper": "oak_door_top", "block/door_iron_lower": "iron_door_bottom", "block/door_iron_upper": "iron_door_top",
	"block/redstone_dust_cross": "redstone_dust_dot", "block/torch_on": "torch", "block/redstone_torch_on": "redstone_torch",
	"block/redstone_torch_off": "redstone_torch_off", "block/rail_normal": "rail", "block/rail_normal_turned": "rail_corner",
	"block/furnace_front_off": "furnace_front", "block/furnace_front_on": "furnace_front_on",
	"block/crafting_table_front": "crafting_table_front", "block/tnt_side": "tnt_side",
	"item/apple_golden": "golden_apple", "item/bow_standby": "bow", "item/bucket_empty": "bucket",
	"item/fishing_rod_uncast": "fishing_rod", "item/book_normal": "book", "item/book_writable": "writable_book",
	"item/book_written": "written_book", "item/book_enchanted": "enchanted_book", "item/map_empty": "map",
	"item/map_filled": "filled_map", "item/reeds": "sugar_cane", "item/seeds_wheat": "wheat_seeds",
	"item/seeds_pumpkin": "pumpkin_seeds", "item/seeds_melon": "melon_seeds", "item/melon": "melon_slice",
	"item/slimeball": "slime_ball", "item/fireball": "fire_charge", "item/carrot_golden": "golden_carrot",
	"item/fish_raw": "cod", "item/fish_cooked": "cooked_cod", "item/beef_raw": "beef", "item/beef_cooked": "cooked_beef",
	"item/porkchop_raw": "porkchop", "item/porkchop_cooked": "cooked_porkchop", "item/chicken_raw": "chicken",
	"item/chicken_cooked": "cooked_chicken", "item/potato_baked": "baked_potato", "item/door_wood": "oak_door",
	"item/door_iron": "iron_door", "item/minecart_normal": "minecart", "item/sign": "oak_sign",
	"item/redstone_dust": "redstone", "item/dye_powder_black": "black_dye", "item/dye_powder_white": "white_dye",
	"item/clock_item": "clock_00", "item/compass_item": "compass_16", "item/bed_red": "red_bed",
}

// colours are Bedrock colour names that differ.
var colours = map[string]string{"silver": "light_gray", "lightblue": "light_blue"}

// javaTextureFor returns the Java texture ("block/oak_planks") a Bedrock texture
// (textures/<kind dir>/<rel>) overrides.
func javaTextureFor(kind, rel string) (string, bool) {
	set := javaTextureSet()
	name := path.Base(rel)
	try := func(n string) (string, bool) {
		if set[kind+"/"+n] {
			return kind + "/" + n, true
		}
		return "", false
	}
	if j, ok := renames[kind+"/"+name]; ok {
		if r, ok := try(j); ok {
			return r, true
		}
	}
	if r, ok := try(name); ok {
		return r, true
	}
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if c, ok := colours[p]; ok {
			parts[i] = c
		}
	}
	n := strings.Join(parts, "_")
	if r, ok := try(n); ok {
		return r, true
	}
	// Prefixed families: X_colored_<colour> and friends.
	for _, f := range []struct{ prefix, suffix string }{
		{"wool_colored_", "_wool"}, {"hardened_clay_stained_", "_terracotta"}, {"glass_", "_stained_glass"},
		{"glazed_terracotta_", "_glazed_terracotta"}, {"concrete_powder_", "_concrete_powder"}, {"concrete_", "_concrete"},
		{"shulker_top_", "_shulker_box"}, {"candle_", "_candle"}, {"dye_powder_", "_dye"},
	} {
		if c, ok := strings.CutPrefix(n, f.prefix); ok {
			if r, ok := try(c + f.suffix); ok {
				return r, true
			}
		}
	}
	for _, c := range extraCandidates(kind, n) {
		if r, ok := try(c); ok {
			return r, true
		}
	}
	// "planks_oak" -> "oak_planks", "log_oak_top" -> "oak_log_top", "bucket_water" ->
	// "water_bucket": the first word moved after the second.
	if len(parts) >= 2 {
		sw := append([]string{parts[1], parts[0]}, parts[2:]...)
		if r, ok := try(strings.Join(sw, "_")); ok {
			return r, true
		}
		// "sword_diamond" style: the first word last.
		rot := append(append([]string{}, parts[1:]...), parts[0])
		if r, ok := try(strings.Join(rot, "_")); ok {
			return r, true
		}
	}
	return "", false
}

// Vanilla Bedrock texture names (vanilla_gen.go).
var (
	vanillaTextures = sync.OnceValue(func() map[string]bool {
		m := map[string]bool{}
		for _, n := range strings.Fields(bedrockVanillaTextures) {
			m[n] = true
		}
		return m
	})
	vanillaTerrainKeys = sync.OnceValue(func() map[string]string { return keyTable(bedrockTerrainKeys) })
	vanillaItemKeys    = sync.OnceValue(func() map[string]string { return keyTable(bedrockItemKeys) })
)

func keyTable(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Fields(s) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// overrideSource is a texture that replaces a vanilla one.
type overrideSource struct {
	pack *Source
	file string // path in the pack, with extension
	why  string
}

// overrides copies textures that replace vanilla ones to their Java names: files at a vanilla
// Bedrock texture path (textures/blocks, textures/items), and vanilla terrain/item texture keys a
// pack points at its own file. The last pack wins. Vanilla textures with no Java counterpart are
// reported.
func (b *builder) overrides() {
	found := map[string]overrideSource{} // java texture -> source
	var order []string
	var unmapped []string
	add := func(java string, src overrideSource) {
		if _, ok := found[java]; !ok {
			found[java] = src
			order = append(order, java)
		}
	}
	vt := vanillaTextures()
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
		if s.Encrypted {
			continue
		}
		// Vanilla keys pointed at the pack's own files.
		for _, t := range []struct {
			file string
			keys map[string]string
		}{{"textures/terrain_texture.json", vanillaTerrainKeys()}, {"textures/item_texture.json", vanillaItemKeys()}} {
			var f struct {
				TextureData map[string]struct {
					Textures json.RawMessage `json:"textures"`
				} `json:"texture_data"`
			}
			if !s.readJSON(t.file, &f) {
				continue
			}
			for _, k := range sortedKeys(f.TextureData) {
				vp, ok := t.keys[k]
				if !ok {
					continue
				}
				paths := texturePaths(f.TextureData[k].Textures)
				if len(paths) == 0 || texKey(paths[0]) == vp {
					continue
				}
				file := texKey(paths[0])
				ext := ".png"
				if _, ok := s.File(file + ext); !ok {
					ext = ".tga"
					if _, ok := s.File(file + ext); !ok {
						continue
					}
				}
				if java, ok := vanillaJava(vp); ok {
					add(java, overrideSource{s, file + ext, "vanilla key " + k})
				}
			}
		}
		for _, dir := range []string{"textures/blocks/", "textures/items/"} {
			for _, n := range s.Files(dir) {
				ext := path.Ext(n)
				if ext != ".png" && ext != ".tga" {
					continue
				}
				p := strings.TrimSuffix(n, ext)
				vanilla := vt[p]
				if !vanilla && (strings.Contains(strings.TrimPrefix(p, dir), "/") || b.customTexture(p)) {
					continue // the pack's own texture
				}
				java, ok := vanillaJava(p)
				if !ok {
					if vanilla {
						unmapped = append(unmapped, p)
					}
					continue
				}
				why := "same path as vanilla"
				if !vanilla {
					why = "vanilla name (path not in the vanilla list)"
				}
				add(java, overrideSource{s, n, why})
			}
		}
	}
	sortStrings(order)
	for _, java := range order {
		src := found[java]
		data, _ := src.pack.File(src.file)
		png, size, err := toPNG(data, path.Ext(src.file) == ".tga")
		if err != nil {
			b.rep.add(catOverride, src.file, Skipped, "unreadable image: "+err.Error())
			continue
		}
		dst := "assets/minecraft/textures/" + java + ".png"
		b.files[dst] = png
		if fb, ok := b.flipbooks[texKey(src.file)]; ok && size.Y > size.X && size.X > 0 && size.Y%size.X == 0 {
			anim := map[string]any{"frametime": max(1, fb.ticks)}
			if len(fb.frames) > 0 {
				anim["frames"] = fb.frames
			}
			b.json(dst+".mcmeta", map[string]any{"animation": anim})
		}
		b.m.Overrides = append(b.m.Overrides, java)
		b.res.Stats.Overrides++
		b.rep.add(catOverride, src.file, Converted, "-> minecraft:"+java+" ("+src.why+")")
	}
	if len(unmapped) > 0 {
		sortStrings(unmapped)
		shown := unmapped
		if len(shown) > 60 {
			shown = shown[:60]
		}
		b.rep.add(catOverride, fmt.Sprintf("%d vanilla textures", len(unmapped)), Skipped,
			"no Java texture known for them (most are entity/GUI textures in Java, or Bedrock-only): "+strings.Join(shown, ", "))
	}
}

// vanillaJava is the Java texture of a vanilla Bedrock texture path (no extension).
func vanillaJava(p string) (string, bool) {
	if rel, ok := strings.CutPrefix(p, "textures/blocks/"); ok {
		return javaTextureFor("block", rel)
	}
	if rel, ok := strings.CutPrefix(p, "textures/items/"); ok {
		return javaTextureFor("item", rel)
	}
	return "", false
}

// customTexture reports whether a texture path is used by the packs' own content (a custom
// item_texture/terrain_texture key), so it is not a vanilla override.
func (b *builder) customTexture(p string) bool {
	if b.customTex == nil {
		b.customTex = map[string]bool{}
		vk, vi := vanillaTerrainKeys(), vanillaItemKeys()
		for k, e := range b.itemTex {
			if _, ok := vi[k]; !ok {
				b.customTex[texKey(e.path)] = true
			}
		}
		for k, e := range b.terrainTex {
			if _, ok := vk[k]; !ok {
				b.customTex[texKey(e.path)] = true
			}
		}
	}
	return b.customTex[texKey(p)]
}
