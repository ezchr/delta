package bridge

import (
	"strings"
	"unicode"
)

// Bedrock actor identifiers -> Java 26.3 entity types (ported from the original bridge entity_map.go,
// which took the Bedrock ids from Geyser's entity_identifiers.dat and checked the Java data
// layouts against the 26.3 decompile; 26.2 has the same layouts for everything sent here).

// entityKind says which Java entity data classes a type has, so only indices that exist are
// sent (a wrong index or serializer makes the Java client drop the connection).
type entityKind uint16

const (
	entLiving     entityKind = 1 << iota // LivingEntity: 8 hand flags, 9 health, ... 14 sleeping pos
	entMob                               // Mob: 15 flags
	entAgeable                           // AgeableMob: 16 baby (BOOLEAN)
	entZombieBaby                        // Zombie: 16 baby (BOOLEAN), not ageable
	entPiglinBaby                        // Piglin: 17 baby
	entPlayer                            // Avatar/Player: 16 skin parts
	entBoat                              // Java turns boats 90 degrees from Bedrock
	entProjectile                        // client-simulated: velocity matters
	entArrow                             // AbstractArrow: 8 flags (0x01 critical)
	entDragon                            // the client makes 8 part entities with the next ids
	entText                              // a custom actor shown as its name tag (entities_text.go)
)

// entityType is how one Bedrock actor type is shown to Java players.
type entityType struct {
	java string // minecraft:entity_type name; "" = not shown
	// yOff is how far above the Java position Bedrock puts the entity (Java y = Bedrock y - yOff).
	yOff float64
	kind entityKind
}

const (
	mob      = entLiving | entMob
	ageable  = mob | entAgeable
	zombieLk = mob | entZombieBaby
)

// entityTypes covers every id in Geyser's bedrock/entity_identifiers.dat.
var entityTypes = map[string]entityType{
	// Passive and neutral mobs.
	"minecraft:allay":            {"minecraft:allay", 0, mob},
	"minecraft:armadillo":        {"minecraft:armadillo", 0, ageable},
	"minecraft:axolotl":          {"minecraft:axolotl", 0, ageable},
	"minecraft:bat":              {"minecraft:bat", 0, mob},
	"minecraft:bee":              {"minecraft:bee", 0, ageable},
	"minecraft:camel":            {"minecraft:camel", 0, ageable},
	"minecraft:camel_husk":       {"minecraft:camel_husk", 0, ageable},
	"minecraft:cat":              {"minecraft:cat", 0, ageable},
	"minecraft:chicken":          {"minecraft:chicken", 0, ageable},
	"minecraft:cod":              {"minecraft:cod", 0, mob},
	"minecraft:copper_golem":     {"minecraft:copper_golem", 0, mob},
	"minecraft:cow":              {"minecraft:cow", 0, ageable},
	"minecraft:dolphin":          {"minecraft:dolphin", 0, ageable},
	"minecraft:donkey":           {"minecraft:donkey", 0, ageable},
	"minecraft:fox":              {"minecraft:fox", 0, ageable},
	"minecraft:frog":             {"minecraft:frog", 0, ageable},
	"minecraft:glow_squid":       {"minecraft:glow_squid", 0, ageable},
	"minecraft:goat":             {"minecraft:goat", 0, ageable},
	"minecraft:happy_ghast":      {"minecraft:happy_ghast", 0, ageable},
	"minecraft:horse":            {"minecraft:horse", 0, ageable},
	"minecraft:llama":            {"minecraft:llama", 0, ageable},
	"minecraft:mooshroom":        {"minecraft:mooshroom", 0, ageable},
	"minecraft:mule":             {"minecraft:mule", 0, ageable},
	"minecraft:nautilus":         {"minecraft:nautilus", 0, mob},
	"minecraft:ocelot":           {"minecraft:ocelot", 0, ageable},
	"minecraft:panda":            {"minecraft:panda", 0, ageable},
	"minecraft:parrot":           {"minecraft:parrot", 0, ageable},
	"minecraft:pig":              {"minecraft:pig", 0, ageable},
	"minecraft:polar_bear":       {"minecraft:polar_bear", 0, ageable},
	"minecraft:pufferfish":       {"minecraft:pufferfish", 0, mob},
	"minecraft:rabbit":           {"minecraft:rabbit", 0, ageable},
	"minecraft:salmon":           {"minecraft:salmon", 0, mob},
	"minecraft:sheep":            {"minecraft:sheep", 0, ageable},
	"minecraft:sniffer":          {"minecraft:sniffer", 0, ageable},
	"minecraft:squid":            {"minecraft:squid", 0, ageable},
	"minecraft:strider":          {"minecraft:strider", 0, ageable},
	"minecraft:tadpole":          {"minecraft:tadpole", 0, mob},
	"minecraft:trader_llama":     {"minecraft:trader_llama", 0, ageable},
	"minecraft:tropicalfish":     {"minecraft:tropical_fish", 0, mob},
	"minecraft:turtle":           {"minecraft:turtle", 0, ageable},
	"minecraft:villager":         {"minecraft:villager", 0, ageable},
	"minecraft:villager_v2":      {"minecraft:villager", 0, ageable},
	"minecraft:wandering_trader": {"minecraft:wandering_trader", 0, ageable},
	"minecraft:wolf":             {"minecraft:wolf", 0, ageable},
	"minecraft:zombie_horse":     {"minecraft:zombie_horse", 0, ageable},
	"minecraft:skeleton_horse":   {"minecraft:skeleton_horse", 0, ageable},
	"minecraft:zombie_nautilus":  {"minecraft:zombie_nautilus", 0, mob},
	"minecraft:snow_golem":       {"minecraft:snow_golem", 0, mob},
	"minecraft:iron_golem":       {"minecraft:iron_golem", 0, mob},
	"minecraft:npc":              {"minecraft:villager", 0, ageable},

	// Hostile mobs.
	"minecraft:blaze":                {"minecraft:blaze", 0, mob},
	"minecraft:bogged":               {"minecraft:bogged", 0, mob},
	"minecraft:breeze":               {"minecraft:breeze", 0, mob},
	"minecraft:cave_spider":          {"minecraft:cave_spider", 0, mob},
	"minecraft:creaking":             {"minecraft:creaking", 0, mob},
	"minecraft:creeper":              {"minecraft:creeper", 0, mob},
	"minecraft:drowned":              {"minecraft:drowned", 0, zombieLk},
	"minecraft:elder_guardian":       {"minecraft:elder_guardian", 0, mob},
	"minecraft:elder_guardian_ghost": {"", 0, 0},
	"minecraft:ender_dragon":         {"minecraft:ender_dragon", 0, mob | entDragon},
	"minecraft:enderman":             {"minecraft:enderman", 0, mob},
	"minecraft:endermite":            {"minecraft:endermite", 0, mob},
	"minecraft:evocation_illager":    {"minecraft:evoker", 0, mob},
	"minecraft:evocation_fang":       {"minecraft:evoker_fangs", 0, 0},
	"minecraft:ghast":                {"minecraft:ghast", 0, mob},
	"minecraft:guardian":             {"minecraft:guardian", 0, mob},
	"minecraft:hoglin":               {"minecraft:hoglin", 0, ageable},
	"minecraft:husk":                 {"minecraft:husk", 0, zombieLk},
	"minecraft:magma_cube":           {"minecraft:magma_cube", 0, ageable},
	"minecraft:parched":              {"minecraft:parched", 0, mob},
	"minecraft:phantom":              {"minecraft:phantom", 0, mob},
	"minecraft:piglin":               {"minecraft:piglin", 0, mob | entPiglinBaby},
	"minecraft:piglin_brute":         {"minecraft:piglin_brute", 0, mob},
	"minecraft:pillager":             {"minecraft:pillager", 0, mob},
	"minecraft:ravager":              {"minecraft:ravager", 0, mob},
	"minecraft:shulker":              {"minecraft:shulker", 0, mob},
	"minecraft:silverfish":           {"minecraft:silverfish", 0, mob},
	"minecraft:skeleton":             {"minecraft:skeleton", 0, mob},
	"minecraft:slime":                {"minecraft:slime", 0, ageable},
	"minecraft:spider":               {"minecraft:spider", 0, mob},
	"minecraft:stray":                {"minecraft:stray", 0, mob},
	"minecraft:sulfur_cube":          {"minecraft:sulfur_cube", 0, ageable},
	"minecraft:vex":                  {"minecraft:vex", 0, mob},
	"minecraft:vindicator":           {"minecraft:vindicator", 0, mob},
	"minecraft:warden":               {"minecraft:warden", 0, mob},
	"minecraft:witch":                {"minecraft:witch", 0, mob},
	"minecraft:wither":               {"minecraft:wither", 0, mob},
	"minecraft:wither_skeleton":      {"minecraft:wither_skeleton", 0, mob},
	"minecraft:zoglin":               {"minecraft:zoglin", 0, mob | entZombieBaby},
	"minecraft:zombie":               {"minecraft:zombie", 0, zombieLk},
	"minecraft:zombie_pigman":        {"minecraft:zombified_piglin", 0, zombieLk},
	"minecraft:zombie_villager":      {"minecraft:zombie_villager", 0, zombieLk},
	"minecraft:zombie_villager_v2":   {"minecraft:zombie_villager", 0, zombieLk},

	// Players come in AddPlayer; the rest are not shown.
	"minecraft:player":        {"minecraft:player", eyeHeight, entLiving | entPlayer},
	"minecraft:agent":         {"", 0, 0},
	"minecraft:tripod_camera": {"", 0, 0},
	"minecraft:balloon":       {"", 0, 0},
	"minecraft:ice_bomb":      {"", 0, 0},

	"minecraft:armor_stand": {"minecraft:armor_stand", 0, entLiving},

	// Items, XP, blocks, decorations.
	"minecraft:item":                 {"minecraft:item", 0.125, entProjectile},
	"minecraft:xp_orb":               {"minecraft:experience_orb", 0, entProjectile},
	"minecraft:falling_block":        {"minecraft:falling_block", 0.49, entProjectile},
	"minecraft:tnt":                  {"minecraft:tnt", 0.49, entProjectile},
	"minecraft:painting":             {"minecraft:painting", 0, 0},
	"minecraft:leash_knot":           {"minecraft:leash_knot", 0, 0},
	"minecraft:ender_crystal":        {"minecraft:end_crystal", 0, 0},
	"minecraft:lightning_bolt":       {"minecraft:lightning_bolt", 0, 0},
	"minecraft:area_effect_cloud":    {"minecraft:area_effect_cloud", 0, 0},
	"minecraft:ominous_item_spawner": {"minecraft:ominous_item_spawner", 0, 0},

	// Boats and minecarts.
	"minecraft:boat":                   {"minecraft:oak_boat", 0.375, entBoat},
	"minecraft:chest_boat":             {"minecraft:oak_chest_boat", 0.375, entBoat},
	"minecraft:minecart":               {"minecraft:minecart", 0.35, 0},
	"minecraft:chest_minecart":         {"minecraft:chest_minecart", 0.35, 0},
	"minecraft:hopper_minecart":        {"minecraft:hopper_minecart", 0.35, 0},
	"minecraft:tnt_minecart":           {"minecraft:tnt_minecart", 0.35, 0},
	"minecraft:command_block_minecart": {"minecraft:command_block_minecart", 0.35, 0},

	// Projectiles.
	"minecraft:arrow":                         {"minecraft:arrow", 0, entProjectile | entArrow},
	"minecraft:thrown_trident":                {"minecraft:trident", 0, entProjectile | entArrow},
	"minecraft:snowball":                      {"minecraft:snowball", 0, entProjectile},
	"minecraft:egg":                           {"minecraft:egg", 0, entProjectile},
	"minecraft:ender_pearl":                   {"minecraft:ender_pearl", 0, entProjectile},
	"minecraft:xp_bottle":                     {"minecraft:experience_bottle", 0, entProjectile},
	"minecraft:splash_potion":                 {"minecraft:splash_potion", 0, entProjectile},
	"minecraft:lingering_potion":              {"minecraft:lingering_potion", 0, entProjectile},
	"minecraft:eye_of_ender_signal":           {"minecraft:eye_of_ender", 0, entProjectile},
	"minecraft:fireworks_rocket":              {"minecraft:firework_rocket", 0, entProjectile},
	"minecraft:fishing_hook":                  {"minecraft:fishing_bobber", 0, entProjectile},
	"minecraft:fireball":                      {"minecraft:fireball", 0, entProjectile},
	"minecraft:small_fireball":                {"minecraft:small_fireball", 0, entProjectile},
	"minecraft:dragon_fireball":               {"minecraft:dragon_fireball", 0, entProjectile},
	"minecraft:wither_skull":                  {"minecraft:wither_skull", 0, entProjectile},
	"minecraft:wither_skull_dangerous":        {"minecraft:wither_skull", 0, entProjectile},
	"minecraft:shulker_bullet":                {"minecraft:shulker_bullet", 0, entProjectile},
	"minecraft:llama_spit":                    {"minecraft:llama_spit", 0, entProjectile},
	"minecraft:wind_charge_projectile":        {"minecraft:wind_charge", 0, entProjectile},
	"minecraft:breeze_wind_charge_projectile": {"minecraft:breeze_wind_charge", 0, entProjectile},
}

// boatTypes and chestBoatTypes map the Bedrock boat variant to the Java type.
var (
	boatTypes = [...]string{
		"minecraft:oak_boat", "minecraft:spruce_boat", "minecraft:birch_boat", "minecraft:jungle_boat",
		"minecraft:acacia_boat", "minecraft:dark_oak_boat", "minecraft:mangrove_boat",
		"minecraft:bamboo_raft", "minecraft:cherry_boat", "minecraft:pale_oak_boat",
	}
	chestBoatTypes = [...]string{
		"minecraft:oak_chest_boat", "minecraft:spruce_chest_boat", "minecraft:birch_chest_boat",
		"minecraft:jungle_chest_boat", "minecraft:acacia_chest_boat", "minecraft:dark_oak_chest_boat",
		"minecraft:mangrove_chest_boat", "minecraft:bamboo_chest_raft", "minecraft:cherry_chest_boat",
		"minecraft:pale_oak_chest_boat",
	}
)

// Variant tables (Bedrock variant -> Java network id), from v1's entity_map.go.
var (
	catVariants        = [...]int32{10, 1, 7, 8, 2, 3, 5, 6, 9, 0, 4}
	wolfVariants       = [...]int32{3, 0, 1, 2, 4, 5, 6, 7, 8}
	frogVariants       = [...]int32{1, 0, 2}
	villagerProfession = [...]int32{0, 5, 6, 12, 7, 9, 3, 4, 1, 14, 13, 2, 8, 10, 11}
	villagerType       = [...]int32{2, 0, 1, 3, 4, 5, 6}
	paintingDirections = [...]int32{3, 4, 2, 5} // AddPainting direction (S, W, N, E) -> Java 3D direction
)

// bedrockEffects names the Bedrock effect ids (Java mob_effect keys).
var bedrockEffects = map[int32]string{
	1: "speed", 2: "slowness", 3: "haste", 4: "mining_fatigue", 5: "strength", 6: "instant_health",
	7: "instant_damage", 8: "jump_boost", 9: "nausea", 10: "regeneration", 11: "resistance",
	12: "fire_resistance", 13: "water_breathing", 14: "invisibility", 15: "blindness",
	16: "night_vision", 17: "hunger", 18: "weakness", 19: "poison", 20: "wither", 21: "health_boost",
	22: "absorption", 23: "saturation", 24: "levitation", 25: "poison", 26: "conduit_power",
	27: "slow_falling", 28: "bad_omen", 29: "hero_of_the_village", 30: "darkness",
	31: "trial_omen", 32: "wind_charged", 33: "weaving", 34: "oozing", 35: "infested",
	36: "raid_omen", 37: "breath_of_the_nautilus",
}

// armourStandPoses are the head, body, left arm, right arm, left leg and right leg rotations of
// Bedrock's 13 armour stand poses (javasession's armourstand.go).
var armourStandPoses = [13][6][3]float32{
	{{0, 0, 0}, {0, 0, 0}, {-10, 0, -10}, {-15, 0, 10}, {-1, 0, -1}, {1, 0, 1}},
	{{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}},
	{{15, 0, 0}, {0, 0, 2}, {-30, 15, 15}, {-60, -20, -10}, {-1, 0, -1}, {1, 0, 1}},
	{{-5, 0, 0}, {0, 0, 2}, {10, 0, -5}, {-60, 20, -10}, {-3, -3, -3}, {3, 3, 3}},
	{{-15, 0, 0}, {0, 0, -2}, {20, 0, -10}, {-110, 50, 0}, {5, -3, -3}, {-5, 3, 3}},
	{{-15, 0, 0}, {0, 0, 0}, {-110, 35, 0}, {-110, -35, 0}, {5, -3, -3}, {-5, 3, 3}},
	{{-15, 0, 0}, {0, 0, 0}, {-110, -35, 0}, {-110, 35, 0}, {5, -3, -3}, {-5, 3, 3}},
	{{0, 0, 0}, {0, 0, 0}, {10, 0, -5}, {-70, -40, 0}, {-1, 0, -1}, {1, 0, 1}},
	{{16, 20, 0}, {0, 0, 0}, {4, 8, 237}, {246, 0, 89}, {-14, -18, -16}, {8, 20, 4}},
	{{-10, 0, -5}, {0, 0, 0}, {-105, 0, 0}, {-100, 0, 0}, {7, 0, 0}, {-46, 0, 0}},
	{{-5, 18, 0}, {0, 22, 0}, {8, 0, -114}, {0, 84, 111}, {-111, 55, 0}, {0, 23, -13}},
	{{-10, -20, 0}, {0, -18, 0}, {0, 0, -112}, {8, 90, 111}, {0, 0, 13}, {-119, -42, 0}},
	{{-4, 67, 0}, {0, 8, 0}, {16, 32, -8}, {-99, 63, 0}, {0, -75, -8}, {4, 63, 8}},
}

// paintingName is the Java name of a Bedrock painting title (BurningSkull -> burning_skull).
func paintingName(title string) string {
	var b strings.Builder
	for i, c := range title {
		if unicode.IsUpper(c) {
			if i > 0 {
				b.WriteByte('_')
			}
			c = unicode.ToLower(c)
		}
		b.WriteRune(c)
	}
	return b.String()
}

// paintingSizes are the sizes (width, height) of the paintings bigger than 1x1.
var paintingSizes = map[string][2]int{
	"courbet": {2, 1}, "creebet": {2, 1}, "pool": {2, 1}, "sea": {2, 1}, "sunset": {2, 1},
	"graham": {1, 2}, "wanderer": {1, 2}, "prairie_ride": {1, 2},
	"bust": {2, 2}, "earth": {2, 2}, "fire": {2, 2}, "match": {2, 2}, "skull_and_roses": {2, 2},
	"stage": {2, 2}, "void": {2, 2}, "water": {2, 2}, "wind": {2, 2}, "wither": {2, 2},
	"baroque": {2, 2}, "humble": {2, 2},
	"bouquet": {3, 3}, "cavebird": {3, 3}, "cotan": {3, 3}, "endboss": {3, 3}, "fern": {3, 3},
	"owlemons": {3, 3}, "sunflowers": {3, 3}, "tides": {3, 3}, "dennis": {3, 3},
	"backyard": {3, 4}, "pond": {3, 4},
	"fighters": {4, 2}, "changing": {4, 2}, "finding": {4, 2}, "lowmist": {4, 2}, "passage": {4, 2},
	"donkey_kong": {4, 3}, "skeleton": {4, 3},
	"pointer": {4, 4}, "pigscene": {4, 4}, "burning_skull": {4, 4}, "orb": {4, 4}, "unpacked": {4, 4},
}

// Java entity events (EntityEvent) the translation sends.
const (
	jevDeath           = 3
	jevAttack          = 4
	jevTamingFailed    = 6
	jevTamingSucceeded = 7
	jevShake           = 8
	jevEatGrass        = 10
	jevOfferFlower     = 11
	jevVillagerAngry   = 13
	jevVillagerHappy   = 14
	jevWitchMagic      = 15
	jevZombieConvert   = 16
	jevFirework        = 17
	jevInLove          = 18
	jevSquidAnim       = 19
	jevGuardianSound   = 21
	jevArmourStandHit  = 32
	jevStopFlower      = 34
	jevTotem           = 35
	jevPoof            = 60
)
