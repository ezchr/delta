package packconv

import (
	"fmt"
	"strings"
)

// moreRenames are whole-name renames tried with the patterns of extraCandidates.
var moreRenames = map[string]string{
	"block/quartz_ore": "nether_quartz_ore", "block/slime": "slime_block", "block/farmland_dry": "farmland",
	"block/farmland_wet": "farmland_moist", "block/rail_golden": "powered_rail", "block/rail_golden_powered": "powered_rail_on",
	"block/rail_activator_powered": "activator_rail_on", "block/rail_detector_powered": "detector_rail_on",
	"block/redstone_lamp_off": "redstone_lamp", "block/repeater_off": "repeater", "block/comparator_off": "comparator",
	"block/smoker_front_off": "smoker_front", "block/blast_furnace_front_off": "blast_furnace_front",
	"block/dirt_podzol_side": "podzol_side", "block/dirt_podzol_top": "podzol_top", "block/dirt_with_roots": "rooted_dirt",
	"block/stone_andesite": "andesite", "block/stone_andesite_smooth": "polished_andesite", "block/stone_diorite": "diorite",
	"block/stone_diorite_smooth": "polished_diorite", "block/stone_granite": "granite", "block/stone_granite_smooth": "polished_granite",
	"block/honey_bottom": "honey_block_bottom", "block/honey_side": "honey_block_side",
	"block/honey_top": "honey_block_top", "block/honeycomb": "honeycomb_block", "block/crimson_log_side": "crimson_stem",
	"block/crimson_log_top": "crimson_stem_top", "block/warped_stem_side": "warped_stem", "block/crimson_nylium_top": "crimson_nylium",
	"block/warped_nylium_top": "warped_nylium", "block/endframe_top": "end_portal_frame_top", "block/endframe_side": "end_portal_frame_side",
	"block/endframe_eye": "end_portal_frame_eye", "block/flower_houstonia": "azure_bluet", "block/flower_rose_blue": "blue_orchid",
	"block/flower_paeonia": "peony", "block/sapling_roofed_oak": "dark_oak_sapling", "block/trip_wire": "tripwire",
	"block/trip_wire_source": "tripwire_hook", "block/turtle_egg_not_cracked": "turtle_egg", "block/glowing_obsidian": "crying_obsidian",
	"block/mushroom_block_skin_brown": "brown_mushroom_block", "block/mushroom_block_skin_red": "red_mushroom_block",
	"block/mushroom_block_skin_stem": "mushroom_stem", "block/seagrass_carried": "seagrass", "block/pale_moss": "pale_moss_block",
	"block/campfire": "campfire_fire", "block/soul_campfire": "soul_campfire_fire", "block/lightning_rod_powered": "lightning_rod_on",
	"block/observer_back_lit": "observer_back_on", "block/melon_stem_connected": "attached_melon_stem",
	"block/pumpkin_stem_connected": "attached_pumpkin_stem", "block/melon_stem_disconnected": "melon_stem",
	"block/pumpkin_stem_disconnected": "pumpkin_stem", "block/cave_vines_body": "cave_vines_plant",
	"block/cave_vines_body_berries": "cave_vines_plant_lit", "block/cave_vines_head": "cave_vines", "block/cave_vines_head_berries": "cave_vines_lit",
	"block/twisting_vines_base": "twisting_vines_plant", "block/twisting_vines_bottom": "twisting_vines",
	"block/weeping_vines_base": "weeping_vines_plant", "block/weeping_vines_bottom": "weeping_vines", "block/kelp_top": "kelp",
	"block/kelp_a": "kelp_plant", "block/dried_kelp_side_a": "dried_kelp_side", "block/shulker_top_undyed": "shulker_box",
	"block/compost": "composter_compost", "block/compost_ready": "composter_ready", "block/fletcher_table_top": "fletching_table_top",
	"block/fletcher_table_side1": "fletching_table_front", "block/fletcher_table_side2": "fletching_table_side",
	"block/dispenser_front_horizontal": "dispenser_front", "block/dropper_front_horizontal": "dropper_front",
	"block/stonecutter2_top": "stonecutter_top", "block/stonecutter2_side": "stonecutter_side", "block/stonecutter2_bottom": "stonecutter_bottom",
	"block/stonecutter2_saw": "stonecutter_saw", "block/big_dripleaf_side1": "big_dripleaf_side", "block/redstone_dust_line": "redstone_dust_line0",
	"block/chain1": "chain", "block/beehive_top": "beehive_end",
	"item/fish_salmon_raw": "salmon", "item/fish_salmon_cooked": "cooked_salmon", "item/fish_clownfish_raw": "tropical_fish",
	"item/fish_pufferfish_raw": "pufferfish", "item/mutton_raw": "mutton", "item/rabbit_raw": "rabbit", "item/netherbrick": "nether_brick",
	"item/totem": "totem_of_undying", "item/melon_speckled": "glistering_melon_slice", "item/spider_eye_fermented": "fermented_spider_eye",
	"item/boat": "oak_boat", "item/boat_darkoak": "dark_oak_boat", "item/sign_darkoak": "dark_oak_sign", "item/turtle_shell_piece": "turtle_scute",
	"item/heartofthesea_closed": "heart_of_the_sea", "item/fireworks": "firework_rocket", "item/fireworks_charge": "firework_star",
	"item/chorus_fruit_popped": "popped_chorus_fruit", "item/dragons_breath": "dragon_breath", "item/potion_bottle_drinkable": "potion",
	"item/potion_bottle_splash": "splash_potion", "item/potion_bottle_lingering": "lingering_potion", "item/potion_bottle_empty": "glass_bottle",
	"item/book_portfolio": "book", "item/bucket_tropical": "tropical_fish_bucket", "item/villagebell": "bell", "item/chain": "chain",
	"item/lodestonecompass_item": "compass_16", "item/recovery_compass_item": "recovery_compass_16", "item/dye_powder_black_new": "black_dye",
	"item/dye_powder_blue_new": "blue_dye", "item/dye_powder_brown_new": "brown_dye", "item/dye_powder_white_new": "white_dye",
	"item/dye_powder_glow": "glow_ink_sac", "item/map_locked": "filled_map", "item/bundle_open": "bundle_open_front",
}

// doublePlants are Bedrock double_plant_<name> textures' Java names.
var doublePlants = map[string]string{"grass": "tall_grass", "fern": "large_fern", "paeonia": "peony", "rose": "rose_bush",
	"syringa": "lilac", "sunflower": "sunflower"}

// coralColours are Bedrock coral colours' Java coral types.
var coralColours = map[string]string{"blue": "tube", "pink": "brain", "purple": "bubble", "red": "fire", "yellow": "horn"}

// extraCandidates are Java names for Bedrock texture names that follow less regular patterns.
func extraCandidates(kind, n string) []string {
	var out []string
	if j, ok := moreRenames[kind+"/"+n]; ok {
		out = append(out, j)
	}
	add := func(s string) { out = append(out, s) }
	if i := strings.LastIndex(n, "_stage_"); i >= 0 { // "_stage_3" -> "_stage3"
		add(n[:i] + "_stage" + n[i+7:])
	}
	for _, p := range []struct{ from, to string }{{"big_oak", "dark_oak"}, {"roofed_oak", "dark_oak"}, {"darkoak", "dark_oak"}} {
		if strings.Contains(n, p.from) {
			n2 := strings.ReplaceAll(n, p.from, p.to)
			add(n2)
			parts := strings.Split(n2, "_")
			if len(parts) >= 3 && parts[1]+"_"+parts[2] == "dark_oak" {
				add(strings.Join(append(append([]string{}, parts[1:3]...), append([]string{parts[0]}, parts[3:]...)...), "_"))
			}
		}
	}
	if c, ok := strings.CutPrefix(n, "flower_"); ok {
		add(c)
		if t, ok := strings.CutPrefix(c, "tulip_"); ok {
			add(t + "_tulip")
		}
	}
	if c, ok := strings.CutPrefix(n, "door_"); ok { // door_acacia_lower -> acacia_door_bottom
		parts := strings.Split(c, "_")
		if len(parts) >= 2 {
			half := map[string]string{"lower": "bottom", "upper": "top"}[parts[len(parts)-1]]
			add(strings.Join(parts[:len(parts)-1], "_") + "_door_" + half)
		}
	}
	if c, ok := strings.CutSuffix(n, "_door_lower"); ok {
		add(c + "_door_bottom")
	}
	if c, ok := strings.CutPrefix(n, "leaves_"); ok { // leaves_jungle_opaque -> jungle_leaves
		c = strings.TrimSuffix(strings.TrimSuffix(c, "_opaque"), "_carried")
		add(c + "_leaves")
	}
	if c, ok := strings.CutSuffix(n, "_opaque"); ok {
		add(c)
	}
	if c, ok := strings.CutPrefix(n, "glass_pane_top_"); ok {
		if col, ok := colours[c]; ok {
			c = col
		}
		add(c + "_stained_glass_pane_top")
	}
	if c, ok := strings.CutPrefix(n, "double_plant_"); ok {
		parts := strings.Split(c, "_")
		if j, ok := doublePlants[parts[0]]; ok && len(parts) == 2 {
			add(j + "_" + parts[1])
		}
	}
	if strings.HasPrefix(n, "coral_") {
		parts := strings.Split(n, "_")
		dead := parts[len(parts)-1] == "dead"
		if dead {
			parts = parts[:len(parts)-1]
		}
		col := coralColours[parts[len(parts)-1]]
		pre := ""
		if dead {
			pre = "dead_"
		}
		switch {
		case col == "":
		case len(parts) == 2:
			add(pre + col + "_coral_block")
		case parts[1] == "plant":
			add(pre + col + "_coral")
		case parts[1] == "fan":
			add(pre + col + "_coral_fan")
		}
	}
	for _, suf := range []string{"_log_side", "_stem_side", "_side_log"} {
		if c, ok := strings.CutSuffix(n, suf); ok {
			add(c + "_log")
			add(c + "_stem")
		}
	}
	if c, ok := strings.CutPrefix(n, "grass_path_"); ok {
		add("dirt_path_" + c)
	}
	if kind == "item" {
		if c, ok := strings.CutPrefix(n, "gold_"); ok {
			add("golden_" + c)
		}
		if c, ok := strings.CutPrefix(n, "wood_"); ok {
			add("wooden_" + c)
		}
		if c, ok := strings.CutPrefix(n, "spawn_egg_"); ok {
			add(c + "_spawn_egg")
		}
		if c, ok := strings.CutPrefix(n, "egg_"); ok {
			add(c + "_spawn_egg")
		}
		if c, ok := strings.CutPrefix(n, "record_"); ok {
			add("music_disc_" + c)
		}
		if c, ok := strings.CutPrefix(n, "bed_"); ok {
			if col, ok := colours[c]; ok {
				c = col
			}
			add(c + "_bed")
		}
		if c, ok := strings.CutSuffix(n, "_open"); ok && strings.HasPrefix(c, "bundle_") {
			add(strings.TrimPrefix(c, "bundle_") + "_bundle_open_front")
		}
		if c, ok := strings.CutPrefix(n, "light_block_"); ok {
			add("light_" + fmt.Sprintf("%02s", c))
		}
	}
	return out
}
