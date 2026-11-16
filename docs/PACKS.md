# Resource packs: Bedrock -> Java

`packconv/` (Go module `delta/packconv`, standard library only) turns the Bedrock resource
packs a server uses, plus the custom item/block definitions of its behaviour packs or of its
runtime registries, into

* a **Java resource pack** (zip, deterministic: the same input gives the same bytes and SHA-1),
* a **mapping** (`Result.Mapping`, `mapping.json`): what the translator sends for each custom
  item, block, entity and sound,
* a **report** (`Result.Report`, Markdown): what was converted, approximated or skipped, and why.

It started as a copy of the converter of the bridge the core is based on (`core/internal/javapack`)
and keeps its API, so the core can switch to it by changing the import (see "Using it from the
core").

## What converts

| Bedrock | Java | Notes |
|---|---|---|
| Packs: folders, `.zip`, `.mcpack`, `.mcaddon`, several in stack order | | lenient JSON (comments, trailing commas, BOM); manifests; subpacks (`SetSubpack`, default: the first listed); encrypted packs are decrypted when the content key is known (`ReadSource`'s key, or a BDS `<pack>.key` file), otherwise detected and reported |
| `textures/item_texture.json`, `terrain_texture.json`, `flipbook_textures.json`, PNG/TGA | `textures/item/...`, `textures/block/...`, `.png.mcmeta` animations | TGA is converted to PNG; texture variants: the first is used |
| Custom item icon | `items/<id>.json` + `item/generated` model (`item/handheld` for tools) | icon keys of vanilla items the pack does not ship use the Java client's own texture |
| Attachable with geometry (held) | 3D item model, `select` on `display_context`: GUI/ground/frame/shelf show the flat icon, hands the 3D model | placed like a vanilla handheld sprite: grip (lower part of a `handle` bone, else of the model) on the sprite's grip, half a turn so the front points where vanilla tools point; shrunk into -16..32 when needed, display scale compensates (max 4) |
| Attachable on a wearable, game armour geometry (`geometry.humanoid.armor.*`, `geometry.player.armor.*`) | equipment asset (`equipment/<set>.json`, layers `humanoid` / `humanoid_leggings`) | one asset per set (ids sharing a name without the slot word); helmet/chest/boots regions combined into one layer |
| Attachable on a wearable, own 3D geometry | equipment asset with the 3D armour flattened onto the vanilla boxes; 3D helmets: a `head` display context model | flattening copies the base cube whose size matches the vanilla box (exact), else projects each texel onto the covering cubes; extras (pads, horns, wings) are lost |
| Item components (behaviour pack JSON or the runtime ItemRegistry form) | `java_base`, `max_stack`, `max_damage`, `equipment`, `food` in the mapping | see "Java base items" |
| Custom block: `blocks.json`, `terrain_texture.json`, block JSON `material_instances`, `geometry`, `transformation`, `permutations`, `states`/traits | block model on a spare Java block state (`assets/minecraft/blockstates/*` replaced) | one state per distinct look over all state combinations (permutation conditions are evaluated: `q.block_state`, `&& || ! == != < > ?:`); quarter-turn `transformation` rotations become blockstate `x`/`y`; other transforms are baked |
| Client entity + geometry + texture (render controller resolved statically: first array entry) | whole-model item model and one item model per bone (pivot at 8,8,8) | rest pose baked; `scripts.scale` baked; bones too big for -16..32 are shrunk and scaled back by their `fixed` display transform (max 4: beyond that the translator scales the display) |
| Vanilla retexture: a file at a vanilla Bedrock texture path (`textures/blocks`, `textures/items`) or a vanilla `terrain_texture`/`item_texture` key pointed at the pack's own file | `assets/minecraft/textures/block|item/<java name>.png` | name table: renames + patterns + `vanilla_gen.go` (vanilla Bedrock texture names); 1827 of the 2214 vanilla Bedrock block/item textures (PBR maps left out) map (the rest are mostly entity/GUI textures in Java, or Bedrock-only) |
| `sounds/sound_definitions.json` with `.ogg` files | `sounds.json` events `<ns>:<bedrock event name>` + the `.ogg` files | `.fsb`/`.wav` are skipped (Java plays Ogg Vorbis only); `sounds.json` entity events of custom entities are listed in the mapping |
| `texts/*.lang` (all languages) | `lang/<code>.json` (lower-case codes: `en_US` -> `en_us`) | every key, so `name_key` in the mapping can be sent as a translatable component |

Not converted, listed in the report: JSON UI, particles, fogs, custom materials/shaders,
animation controllers, animations (entities are static; attachable animations are replaced by
vanilla-like placement), render controller logic beyond the default texture/geometry, fonts and
glyphs, biome colours, `poly_mesh` geometry, attachables of vanilla items.

## Java base items

The Java client predicts behaviour from the item it is sent as, so each custom item has a base:

| Bedrock components | `kind` | `java_base` |
|---|---|---|
| `minecraft:wearable` slot head/chest/legs/feet (or the game armour geometry in its attachable) | helmet/chestplate/leggings/boots | `minecraft:diamond_<kind>` |
| tag `minecraft:is_sword|axe|pickaxe|shovel|hoe`, or `minecraft:enchantable` slot of that name | sword/axe/... | `minecraft:diamond_<kind>` |
| enchantable slot `bow`/`crossbow`/`fishing_rod`, `minecraft:shooter` (`charge_on_draw` -> crossbow) | bow/crossbow/fishing_rod | the same vanilla item |
| `minecraft:food` (`use_animation` drink -> drink) | food/drink | `minecraft:bread` / `minecraft:honey_bottle` |
| `minecraft:throwable`, `minecraft:projectile` | throwable | `minecraft:snowball` |
| `minecraft:block_placer`, anything else | block/item | `minecraft:paper` (the client neither places, eats nor uses it) |
| a tool/weapon of no known class (`hand_equipped`, `damage`, `digger`) | from the name (`_axe`, `pickaxe`...), else sword | |

The translator still sends `max_stack_size`, `max_damage`, `food`/`consumable` and `equippable`
components from the mapping: the base only fixes what the client does by itself (mining
prediction, arm swing, eating animation, equipping on use, the durability bar).

## Custom block states

Pools of Java states the vanilla mapping never sends, in this order (`Candidates()`): the 3 x 64
huge mushroom block face combinations, the 27 x 25 powered note block states (Java's client
recomputes a note block's instrument when a block is placed above or below it, so they come
second), and 81 unpowered redstone wire connection states for blocks without collision. The
caller removes the states its own block mapping uses and passes the rest as `Input.Slots`; a
block that gets none is reported with a fallback (`minecraft:stone`, `minecraft:air` for
non-solid). Non-cube geometry on a full-cube state is an approximation: the client culls
neighbouring faces as for a full block.

## Coordinate conventions

Bedrock geometry is Java's entity model space with y up. A Bedrock point (x, y, z) is (-x, y, z)
in Java model space and a rotation (rx, ry, rz) is (-rx, -ry, rz) applied Z, Y, X (Java 26.2+
euler element rotation; single-axis rotations are written with `axis`/`angle`). Box UV follows
the entity cube: strips right side (Java east), front (north), left side (west), back (south);
top above the front strip (Java `up`, turned 180 degrees), bottom above the left strip (Java
`down`, mirrored in u). Per-face `up`/`down` UVs are flipped on both axes (as Blockbench does).
Planes get their back face added (Java culls back faces, Bedrock draws them).

An entity part's model has the bone's pivot at (8, 8, 8). Shown on an `item_display` (which
turns its item half a turn about Y) at `offset` (blocks) from the entity, for an entity facing
south (yaw 0), it appears where the Bedrock bone is. `model_yaw: 180` is the
`javasession.EntityModel.Yaw` value that keeps that half turn when the caller undoes it.

## mapping.json (schema 1)

```json
{
  "schema": 1, "converter_version": 2, "namespace": "Delta", "pack_sha1": "<hex>",
  "items": {
    "ns:blade": {"java_base": "minecraft:diamond_sword", "item_model": "Delta:ns/blade", "kind": "sword",
                 "model": "3d", "name": "Blade", "name_key": "item.ns:blade", "max_stack": 1, "max_damage": 250,
                 "glint": false, "source": "behavior_pack"},
    "ns:helmet": {"java_base": "minecraft:diamond_helmet", "item_model": "...", "kind": "helmet", "model": "flat",
                  "equipment": {"slot": "head", "asset": "Delta:ns"}, "source": "runtime"},
    "ns:snack": {"java_base": "minecraft:bread", "kind": "food", "model": "flat", "item_model": "...",
                 "food": {"nutrition": 4, "saturation": 4.8, "use_seconds": 1.2, "animation": "eat"}, "source": "..."}
  },
  "blocks": {
    "ns:ore": {"java_state": "minecraft:brown_mushroom_block[down=true,east=true,north=true,south=true,up=true,west=true]",
               "slot": 0, "solid": true, "name": "Ore", "name_key": "tile.ns:ore.name", "item_model": "Delta:ns/ore",
               "variants": [{"states": {"minecraft:cardinal_direction": "east", "ns:lit": false},
                             "java_state": "minecraft:brown_mushroom_block[...]", "slot": 1}],
               "approximate": "", "fallback": ""}
  },
  "entities": {
    "ns:critter": {"model": "Delta:entity/ns/critter", "scale": 1, "translation": [0, 0.3125, 0],
                   "model_yaw": 180, "width": 0.75, "height": 0.75, "stand_in": "minecraft:fox",
                   "parts": [{"bone": "head", "parent": "body", "model": "Delta:entity/ns/critter/head",
                              "offset": [0, 0.5, 0.375], "pivot": [0, 8, -6], "rotation": [15, 30, 0],
                              "scale": 1, "display": "fixed"}],
                   "bones": [{"name": "body", "pivot": [0, 6, 0], "rotation": [0, 0, 0], "cubes": 1}],
                   "unsupported": ["animation walk (animation.ns.walk)"]}
  },
  "sounds": {"ns.chirp": {"java_event": "Delta:ns.chirp", "category": "neutral", "files": 2}},
  "entity_sounds": {"ns:critter": {"ambient": "ns.chirp"}},
  "overrides": ["block/stone"],
  "languages": ["de_de", "en_us"]
}
```

* `items[id].item_model`: the `minecraft:item_model` component; empty when nothing could be
  drawn (send the base item as it is). `model`: flat, handheld, 3d, head, block, none.
* `blocks[id]`: `java_state` is the default look; the first `variants` entry whose `states` all
  match the block's states wins. `slot` indexes the `Input.Slots` the conversion was given.
* `entities[id]`: `model`/`scale`/`translation` draw the whole entity with one item display
  (display scale = scale x the entity's scale, translation x the entity's scale); `parts` draw it
  bone by bone (rest rotations of the bone and its parents are in the part models; `scale` is how
  much a part was shrunk, its `fixed` display transform scales it back).
* Fields are only added within a schema version; a removed or changed field bumps `schema`.

## Go API (what the core calls)

```go
// Read packs: from the network (the bytes BDS sends, with the content key from ResourcePacksInfo)
src, err := packconv.ReadSource(r io.ReaderAt, size int64, uuid, version, contentKey string)
src.SetSubpack(name)                        // the subpack the stack names (ResourcePackStack.SubPackName)
// ... or from disk: one pack, an .mcaddon / folder of packs, or a BDS world's stack
src, err := packconv.ReadPack(path)
srcs, err := packconv.ReadPacks(path)
bds, err := packconv.FromBDS(bdsRoot, world)   // bds.Resource, bds.Behaviour (bottom of the stack first)

res, err := packconv.Convert(&packconv.Input{
	Packs:         resourcePacks,  // bottom of the stack first
	BehaviorPacks: behaviourPacks, // optional: item/block definitions from disk
	Items:         items,          // packconv.Item{ID, Data: the ItemRegistry entry's data} (Data may be nil)
	Blocks:        blocks,         // packconv.Block{Name, Props: StartGame's block properties}
	Slots:         freeSlots,      // packconv.Candidates() minus the states the block mapping uses
	Description:   "...",
})
res.Zip, res.SHA1                    // serve these
res.ItemModels, res.ItemNames        // id -> item_model / en_US name (as before)
res.Blocks                           // block name -> index in Slots (default look, as before)
res.Entities                         // id -> Entity{Model, Scale, Translation, Name, Parts, Bones, ...}
res.Mapping                          // everything, incl. java_base, equipment, food, variants, sounds
res.Mapping.MarshalIndent()          // mapping.json
res.Report.Markdown()                // report.md
res.Empty()
```

Call `Convert` when a connection has the server's packs and registries (StartGame, ItemRegistry):
the core already does this once per distinct set (cache key: `packconv.Version`, the packs'
uuid/version/size, the items and blocks). Items discovered later (an item first seen in an
inventory) need no reconversion when the item has an icon in `item_texture.json` under its own
name: pass its id in `Items` (no Data) and its icon is guessed. When the set of custom items or
blocks changes, convert again: the result is a new pack (new SHA-1) to push.

## Serving the pack to Java clients

The Java protocol only takes a URL (plus the SHA-1, which the client checks) in
`resource_pack_push`, so the plugin must serve the zip over HTTP: the core's pack server (from the
original bridge, `bridge/packs.go`) listens on a configurable address and serves
`/<sha1>.zip` from its cache directory; the plugin needs a config entry for that address and for
the public URL players reach it at (the host's address and port, or a URL behind a proxy/CDN).
The push carries `required` (the server's `TexturePackRequired`) and a prompt.

## Command line and tools

```
go run ./cmd/packconv -in <packs> [-bp <behaviour packs>] [-subpack folder|uuid=folder] [-items items.json]
                      -out java.zip -map mapping.json -report report.md
go run ./cmd/packconv -bds <BDS root> [-world name] -out java.zip ...
go run ./cmd/packpreview -in <packs> -java java.zip -map mapping.json [-ref handmade.zip] -out dir \
    item:<id>[=<ref item_model>] head:<id> armor:<piece id>[=<ref asset>] entity:<id> part:<id>/<bone> block:<id>
go run ./tools/genvanilla -rp <vanilla Bedrock resource_pack> > vanilla_gen.go
```

`packpreview` draws the Bedrock geometry (as the game textures entity cubes; independent of the
converter's face mapping) next to the converted Java model (as the Java client builds model faces)
from three angles and prints the share of pixels that differ; 0 means the model, UVs and pivots
match.

## Tests

`go test ./...`: fixture packs written in `fixtures_test.go` (a flat item, a 3D attachable item
with nested rotations, mirrored and per-face UV cubes, an armour set, a block with a facing state
and a texture state, an entity with two bones in the legacy format, vanilla retextures by path
and by key, sounds, languages, subpacks, lenient JSON, an encrypted pack, runtime-form items);
the 3D item and the entity are rendered both ways and must match pixel for pixel.

`realpacks_test.go` converts real packs found by environment variables (`PACKCONV_*`, see the
file) and writes comparison tables against hand-made Java packs; nothing of those packs is in the
repository and the tests skip without them.
