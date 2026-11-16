package main

// Bedrock particles Geyser does not map (it maps Java to Bedrock, so Bedrock-only particles and
// those Geyser draws another way are missing). Each gets the Java particle the Java client draws
// for the same thing (named after the Java code that spawns it), or the closest look.

// Java colours used below.
const (
	redstone      = 0xffff0000 // DustParticleOptions.REDSTONE
	sculk         = 0xff39d6e0 // SculkSensorBlock particle colour (3790560)
	stunned       = 0xff7f8392 // Ravager.stunEffect ENTITY_EFFECT (0.498, 0.514, 0.573)
	witherShield  = 0xffb2b2e5 // WitherBoss invulnerable ENTITY_EFFECT (0.7, 0.7, 0.9)
	defaultEffect = 0xffffffff
)

// legacyFallbacks are Bedrock particle types (gophertunnel names) with no Geyser mapping.
var legacyFallbacks = map[string]entry{
	"BubbleManual":        {Java: "bubble"},
	"BlockForceField":     {Java: "block_marker", Block: "barrier"},
	"RisingBorderDust":    {Java: "white_ash"},
	"LargeExplode":        {Java: "explosion"},
	"HugeExplosion":       {Java: "explosion_emitter", Override: true}, // Geyser: explosion
	"MobFlame":            {Java: "flame"},
	"TownAura":            {Java: "mycelium"},
	"MobPortal":           {Java: "portal"},
	"WaterSplashManual":   {Java: "splash"},
	"MobSpellAmbient":     {Java: "entity_effect"},
	"MobAppearance":       {Java: "elder_guardian"},
	"Food":                {Java: "item"},
	"FireworksStarter":    {Java: "firework"},
	"Fireworks":           {Java: "firework"},
	"FireworksOverlay":    {Java: "flash", Colour: defaultEffect},
	"BalloonGas":          {Java: "cloud"},
	"ColouredFlame":       {Java: "flame"},
	"Sparkler":            {Java: "firework"},
	"Conduit":             {Java: "nautilus"},
	"ShulkerBullet":       {Java: "end_rod"},
	"DragonDestroyBlock":  {Java: "explosion"},
	"MyceliumDust":        {Java: "mycelium"},
	"FallingBorderDust":   {Java: "white_ash"},
	"CampfireSmokeTall":   {Java: "campfire_signal_smoke"},
	"DragonBreathFire":    {Java: "dragon_breath"},
	"DragonBreathTrail":   {Java: "dragon_breath"},
	"PortalReverse":       {Java: "reverse_portal"},
	"SculkSensorRedstone": {Java: "dust_color_transition", Colour: sculk, Colour2: redstone},
	"SporeBlossomAmbient": {Java: "spore_blossom_air"},
	"CandleFlame":         {Java: "small_flame"},
	"SonicExplosion":      {Java: "sonic_boom"},
	"BrushDust":           {Java: "block"},
	"WindExplosion":       {Java: "gust_emitter_large"},
	"BreezeWindExplosion": {Java: "gust_emitter_small"},
	"WolfArmourBreak":     {Java: "item", Item: "wolf_armor"},
	"OminousItemSpawner":  {Java: "ominous_spawning"},
}

// legacySkip are Bedrock particle types with no Java equal.
var legacySkip = map[string]bool{
	"TrackerEmitter":  true, // an editor/debug tracker
	"CarrotBoost":     true, // Bedrock-only pig boost puff
	"Bleach":          true, // Education Edition
	"EyeblossomOpen":  true, // a Java trail particle to a target Bedrock does not send
	"EyeblossomClose": true,
}

// namedFallbacks are vanilla particle effects (identifier without namespace) with no Geyser
// mapping, or with a better Java particle than Geyser's (Override).
var namedFallbacks = map[string]entry{
	"arrow_spell_emitter":                   {Java: "entity_effect", Colour: defaultEffect},
	"balloon_gas_particle":                  {Java: "cloud"},
	"basic_bubble_particle":                 {Java: "bubble"},
	"basic_crit_particle":                   {Java: "crit"},
	"basic_portal_particle":                 {Java: "portal"},
	"basic_smoke_particle":                  {Java: "smoke"},
	"block_destruct":                        {Java: "block"},
	"block_slide":                           {Java: "block"},
	"breeze_ground_particle":                {Java: "block"},
	"camera_shoot_explosion":                {Java: "poof"},
	"cauldron_bubble_particle":              {Java: "bubble_pop"},
	"cauldron_explosion_emitter":            {Java: "explosion"},
	"cauldron_spell_emitter":                {Java: "entity_effect", Colour: defaultEffect},
	"cauldron_splash_particle":              {Java: "splash"},
	"colored_flame_particle":                {Java: "flame"},
	"conduit_absorb_particle":               {Java: "nautilus"},
	"conduit_attack_emitter":                {Java: "nautilus"},
	"conduit_particle":                      {Java: "nautilus"},
	"creaking_crumble_body":                 {Java: "block_crumble", Block: "pale_oak_wood"},
	"creaking_crumble_head":                 {Java: "block_crumble", Block: "creaking_heart"},
	"crop_growth_area_emitter":              {Java: "happy_villager"},
	"crop_growth_emitter":                   {Java: "happy_villager"},
	"death_explosion_emitter":               {Java: "poof"},
	"dragon_breath_fire":                    {Java: "dragon_breath"},
	"dragon_breath_trail":                   {Java: "dragon_breath"},
	"dragon_death_explosion_emitter":        {Java: "explosion_emitter"},
	"dragon_destroy_block":                  {Java: "explosion"},
	"dragon_dying_explosion":                {Java: "explosion_emitter"},
	"egg_destroy_emitter":                   {Java: "item", Item: "egg"},
	"electric_spark_particle":               {Java: "electric_spark"},
	"end_chest":                             {Java: "portal"},
	"evocation_fang_particle":               {Java: "crit"},
	"explosion_particle":                    {Java: "poof"},
	"eye_of_ender_bubble_particle":          {Java: "portal"},
	"eyeofender_death_explode_particle":     {Java: "item", Item: "ender_eye"},
	"falling_border_dust_particle":          {Java: "white_ash"},
	"falling_dust":                          {Java: "falling_dust"},
	"falling_dust_concrete_powder_particle": {Java: "falling_dust"},
	"falling_dust_dragon_egg_particle":      {Java: "falling_dust", Block: "dragon_egg"},
	"falling_dust_gravel_particle":          {Java: "falling_dust", Block: "gravel"},
	"falling_dust_red_sand_particle":        {Java: "falling_dust", Block: "red_sand"},
	"falling_dust_sand_particle":            {Java: "falling_dust", Block: "sand"},
	"falling_dust_scaffolding_particle":     {Java: "falling_dust", Block: "scaffolding"},
	"falling_dust_top_snow_particle":        {Java: "falling_dust", Block: "snow"},
	"fish_hook_particle":                    {Java: "fishing"},
	"fish_pos_particle":                     {Java: "fishing"},
	"guardian_attack_particle":              {Java: "bubble"},
	"guardian_water_move_particle":          {Java: "bubble"},
	"huge_explosion_lab_misc_emitter":       {Java: "explosion_emitter"},
	"ice_evaporation_emitter":               {Java: "cloud"},
	"infested_ambient":                      {Java: "infested"},
	"ink_emitter":                           {Java: "squid_ink"},
	"knockback_roar_particle":               {Java: "poof"},
	"magic_critical_hit_emitter":            {Java: "enchanted_hit"},
	"misc_fire_vapor_particle":              {Java: "smoke"},
	"mob_block_spawn_emitter":               {Java: "flame"},
	"mobflame_emitter":                      {Java: "flame"},
	"mobflame_single":                       {Java: "flame"},
	"mobspell_ambient":                      {Java: "entity_effect", Colour: defaultEffect},
	"mobspell_lingering":                    {Java: "entity_effect", Colour: defaultEffect},
	"nautilus_bubbles_particle":             {Java: "bubble"},
	"obsidian_glow_dust_particle":           {Java: "reverse_portal"},
	"obsidian_tear_particle":                {Java: "dripping_obsidian_tear"},
	"oozing_ambient":                        {Java: "item_slime"},
	"oozing_emitter":                        {Java: "item_slime"},
	"orange_poplar_leaves_particle":         {Java: "orange_poplar_leaves"},
	"pale_oak_leaves_particle":              {Java: "pale_oak_leaves"},
	"pause_mob_growth":                      {Java: "pause_mob_growth"},
	"phantom_trail_particle":                {Java: "mycelium"},
	"portal_directional":                    {Java: "portal"},
	"portal_east_west":                      {Java: "portal"},
	"portal_north_south":                    {Java: "portal"},
	"raid_omen_emitter":                     {Java: "raid_omen"},
	"rain_splash_particle":                  {Java: "rain"},
	"red_poplar_leaves_particle":            {Java: "red_poplar_leaves"},
	"redstone_ore_dust_particle":            {Java: "dust", Colour: redstone},
	"redstone_repeater_dust_particle":       {Java: "dust", Colour: redstone},
	"redstone_torch_dust_particle":          {Java: "dust", Colour: redstone},
	"redstone_wire_dust_particle":           {Java: "dust", Colour: redstone},
	"reset_mob_growth":                      {Java: "reset_mob_growth"},
	"sculk_charge_particle":                 {Java: "sculk_charge"},
	"sculk_charge_pop_particle":             {Java: "sculk_charge_pop"},
	"sculk_sensor_redstone_particle":        {Java: "dust_color_transition", Colour: sculk, Colour2: redstone},
	"shulker_bullet":                        {Java: "end_rod"},
	"silverfish_grief_emitter":              {Java: "poof"},
	"small_flame_particle":                  {Java: "small_flame"},
	"small_soul_fire_flame":                 {Java: "soul_fire_flame"},
	"smash_ground_particle_center":          {Java: "dust_pillar"},
	"sneeze":                                {Java: "sneeze"},
	"snowflake_particle":                    {Java: "snowflake"},
	"sponge_absorb_water_particle":          {Java: "splash"},
	"squid_flee_particle":                   {Java: "squid_ink"},
	"squid_ink_bubble":                      {Java: "bubble"},
	"squid_move_particle":                   {Java: "bubble"},
	"stunned_emitter":                       {Java: "entity_effect", Colour: stunned},
	"trial_spawner_detection":               {Java: "trial_spawner_detection"},
	"trial_spawner_detection_ominous":       {Java: "trial_spawner_detection_ominous"},
	"totem_manual":                          {Java: "totem_of_undying"},
	"trial_omen_ambient":                    {Java: "trial_omen"},
	"trial_omen_single":                     {Java: "trial_omen"},
	"underwater_torch_particle":             {Java: "bubble"},
	"vault_connection_particle":             {Java: "vault_connection"},
	"warden_dig":                            {Java: "block"},
	"water_evaporation_manual":              {Java: "cloud"},
	"wax_particle":                          {Java: "wax_on"},
	"weaving_ambient":                       {Java: "item_cobweb"},
	"weaving_emitter":                       {Java: "item_cobweb"},
	"white_smoke_particle":                  {Java: "white_smoke"},
	"wind_charged_ambient":                  {Java: "small_gust"},
	"witchspell_emitter":                    {Java: "witch"},
	"wither_boss_invulnerable":              {Java: "entity_effect", Colour: witherShield},
	"yellow_poplar_leaves_particle":         {Java: "yellow_poplar_leaves"},
}

// namedSkip are vanilla particle effects with no Java equal.
var namedSkip = map[string]bool{
	"bleach":                              true, // Education Edition chemistry
	"elephant_tooth_paste_vapor_particle": true,
	"lab_table_heatblock_dust_particle":   true,
	"lab_table_misc_mystical_particle":    true,
	"magnesium_salts_emitter":             true,
	"breaking_item_terrain":               true, // a block-textured item break without the item
	"creaking_heart_trail":                true, // Java trail particles need the target Bedrock keeps in molang
	"eyeblossom_close":                    true,
	"eyeblossom_open":                     true,
	"vibration_signal":                    true, // Java vibration needs the destination (LevelEventGeneric carries it)
}
