# Things learned the hard way

Facts about BDS and the Java client that cost time to find. Read the matching section before
changing that area, and add a section when you learn something new: what actually happens, why,
where the code is, and how to test it.

## Identity and login

- **BDS gives a self-signed login an empty XUID** (with `online-mode=false`), and so does
  gophertunnel's dialer unless `Dialer.KeepXBLIdentityData` is set. The identity hook (the
  xuidforward shim) writes the staged XUID into `ServerNetworkHandler::_processValidatedLoginPacket`,
  which runs after BDS validates the login: it does not get around `online-mode=true`, which refuses
  self-signed logins first. Keep BDS at `online-mode=false`; Java accounts are checked on the Java side.
- **Relayed logins come from the public address, not 127.0.0.1**, when BDS runs
  `transport=nethernet`: its NetherNet socket listens on the machine's public IPs, so a Bedrock proxy
  on the same machine connects from one of those. Trust "local" (every address of the machine), not
  "127.0.0.1". With RakNet on loopback, 127.0.0.1 is right.
- **xuidforward next to this plugin**: only one shim can patch BDS. The plugin `soft_depend`s on
  xuidforward, so xuidforward enables first and hooks; the plugin then stages Java players through
  xuidforward's shim (`IdentityHook.borrow`) and its login handler runs at `HIGHEST`, after
  xuidforward's (`NORMAL`), which would otherwise clear a Java login it doesn't trust. Without
  xuidforward its `config.json` is still read (`auth/trust_config.py`). Tested live:
  `plugin/tests/test_xuidforward_compat.py` covers the logic.
- **A malformed XUID in a broadcast player list can crash other Bedrock clients.** Derived XUIDs
  must look like real ones: 16 digits starting 2535.

## Skins

- **BDS trusts a skin only if** `PersonaSkin || (PremiumSkin && isValidSkinGeometry(geometry))`,
  and `isValidSkinGeometry` passes only empty geometry data or Marketplace geometry (BDS 1.21.3
  dump). An untrusted skin is replaced by a default one picked from the player's UUID. The
  `TrustedSkin` field in ClientData doesn't count. Send Java skins as Geyser does: premium, no
  geometry data, and a resource patch naming the vanilla `geometry.humanoid.custom` or
  `geometry.humanoid.customSlim` (`bridge/skin.go`).
- Mojang's API rate-limits a server address quickly (429 after two name lookups); online and
  Velocity logins carry signed textures and need no lookup.

## Movement

- **BDS ignores a sprint stop sent while the player is in the air**, and on the landing input. A Java
  client stops sprinting when it runs into a wall, so BDS keeps sprinting, gives the next jump the
  sprint boost Java didn't, and corrects the player 0.3 to 0.9 blocks: the stutter at block corners
  (2x2 gens). Not fixed yet; `JB_DEBUG_MOVE=1` logs every input tick to find which input BDS takes
  the stop on.
- Java 26.x keeps sprinting while crouching; BDS ends the sprint. When a player starts sneaking on
  the ground BDS stops them dead: the bridge stops the Java client too.
- Bedrock resolves collisions in the order Y, X, Z; Java does the larger horizontal axis first.
  This can still cause small corrections at corners.
- End gateways are solid on BDS but not on Java: walking into one gets corrected.
- A teleport into chunks the Java client doesn't have yet makes it fall and get corrected: the
  teleport waits for those chunks (3 s at most).
- **Reach** (BDS dump, `GameMode::getPickRange`): 5.7 blocks with mouse in both modes, 5.6 with a
  controller, 6.7 on touch (12 in creative); attacks reach 3 (5 in creative).

## Sounds and particles

- BDS sends `attack.nodamage` for a player's own swings; vanilla Java makes no sound for a swing at
  the air, so the player's own one is dropped unless the swing hit an entity. A 26.3 client's `punch`
  comes with every swing, so the missed-swing input flag is set only when no dig, use or hit came in
  the same tick (`bridge/swingair.go`).
- Particle tables are generated from Geyser's mappings (`tools/genparticles`); don't hand-edit
  `internal/particles/particles_gen.go`.
- On 26.2 there is no "quiet block break" event, and wax-on and potion splash play their own sound
  (sending ours too doubled it).
- How BDS packs the data of crack, generic-spawn and dragon-egg events comes from the 1.21.3 dump,
  not from captures.

## Resource packs and models

- **A Java client downloads a pack from a URL, and the URL has to work from the player's
  machine.** Until 0.3.2 packs were off by default and, when on, the URL was `http://127.0.0.1:<random
  port>`: the prompt showed and the download failed for everyone not on the server machine ("Your
  client could not load the server's resource pack"). Now the Java port answers HTTP too
  (`service/portmux.go`: a connection that starts with `GET ` or `HEAD` goes to the pack handler)
  and each player gets the host and port of their own handshake (`(*Player).packURL`), which also
  works through port forwards, SRV records and TCP tunnels, and on hosts that give one port. Behind
  Velocity the handshake address is the proxy's: `[packs] listen` opens a port of its own.
- **gophertunnel asks `DownloadResourcePack` before it looks in the pack cache.** The size limit for
  downloads therefore also dropped packs that are in the server's folders and need no download:
  `packDownload` asks `serverHasPack` first. Seen with the Cobblemon port (186 MB zipped).
- The converter's own limits were for hostile servers, not for real add-ons: 20000 files refused
  Cobblemon (36606). Sounds, structures and scripts are not read at all (`unusedFile`).
- Reading a 290 MB pack folder takes about 15 s on the first Java login after a start (it is zipped
  in memory); later logins reuse it.
- **A Java 26 client refuses a model whose face UV leaves its texture** ("Unable to bake model:
  Cannot compute translucency out of bounds: [71, 62, 74, 62] in 64x64 image") and draws the
  missing-model cube; the rest of the pack still loads. Bedrock geometry does this often (box UVs
  past the edge). `faceUV`/`fitUV` in `internal/javapack/geometry.go` wrap a span that is wholly
  outside and cut what sticks out. Only a real client shows this: the bot checks that JSON parses.
- **A dialog (a server form) replaces the pack prompt screen.** Forms that arrive while the prompt is
  pending wait (`packPending`, `heldForms` in `bridge/packs.go`) until the client answered, for two
  minutes at most.
- A client whose server list entry has "Server Resource Packs: Disabled" answers "declined" at once,
  with no prompt: check the test client's `servers.dat` (`acceptTextures`) before blaming the bridge.
- Test: a small retexture pack and a big add-on in a test world, the bot (`jbbot -packlist` logs
  the pack it is pushed, downloads it and checks its JSON), and a static check of the Java zip
  (resource paths, model parents, texture references).

- Bedrock 3D armour geometry (horns, shoulder pads) can't be drawn by Java's armour layers: sets with
  their own geometry use the 2D textures from the pack's 2D subpack, and a texture is built from the
  model when there is none.
- 3D held items use only the attachable's first pose; animations and Molang are ignored. Java
  mirrors the pose for the off hand but not the model. (Custom mobs do animate: next section.)

## Custom mob animations

Custom (add-on) mobs animate for Java players since 0.3.4. Code: `core/internal/molang` (Molang
to closures), `core/internal/anim` (animation files, controllers, the running state, the plan
of which bones get a display), `core/internal/javapack/entities.go` (the models),
`core/bridge/entities_anim.go` (the displays), `core/cmd/animpreview` (pictures).

**What Bedrock does.** A client entity file names animations and animation controllers
(`animations`: short name -> id) and plays the ones in `scripts.animate` (a name, or `{name:
condition}`). `scripts.initialize` runs once, `scripts.pre_animation` every frame before the
animations; packs compute their own state there (the Cobblemon port sets `v.pose` from
`q.ground_speed`, `q.is_on_ground`, `q.is_in_water`, `q.is_sleeping` and actor properties, and
its controller only compares `v.pose`). An animation gives bones a rotation, position and scale
(numbers, Molang, or keyframes with `pre`/`post` and `lerp_mode: catmullrom`); animations add
up, weighted (rotation and position add, scale multiplies); a controller is a state machine
whose states play animations and blend over `blend_transition` seconds when it changes state.
`scripts.scale` scales the whole model, on top of the actor's scale (Cobblemon: two actor
properties multiplied; the bridge did not apply it at all before, so every Pokemon was drawn
at the base size).

**What Java can do.** A custom mob is item displays. A display shows one item model and takes
a translation, a left rotation (quaternion), a scale and a right rotation; when a packet that
changes them also carries entity data 8 (`start_interpolation`, sent as 0) the client
interpolates from what it shows to the new values over data 9 ticks
(`interpolation_duration`). A display that rides another entity moves with it, on the client.
Its own yaw (`move_entity_rot`) is interpolated over data 10 ticks. So: one display per moving
part, riding the mob's own (now empty) display; a pose every few ticks; the mob's yaw as each
part's entity rotation. There is no skinning and no per-vertex anything: a part is rigid.
BetterModel (the Paper plugin) does the same thing the same way: a display per Blockbench
group, mounted on the base entity, yaw by entity rotation with a pos/rot interpolation
duration, a keyframe every 3 ticks (`lerp-frame-time`), packets bundled.

**Conventions (verified with `animpreview`, see below; getting one wrong gives a pose that is
plainly broken).**

- All animation maths is in Java model space, pixels: a Bedrock point (x, y, z) is (-x, y, z),
  a Bedrock rotation (rx, ry, rz) is the matrix Rz(rz) Ry(-ry) Rx(-rx), an animation position
  (px, py, pz) moves the bone by (-px, py, pz) in its parent's frame. A bone's rotation is its
  bind rotation plus the animations' (the angles add, they are not multiplied as matrices).
- A bone: `T(position) * RotAbout(pivot, rotation) * ScaleAbout(pivot, scale)`, after its
  parent's.
- A display draws its model's point m (0..16) at `T + L (S * Ry180 (m - 8) / 16)`, for yaw 0 =
  facing south (the half turn is the item display's own). A part's model has the part's pivot
  at (8, 8, 8) and is shrunk by k when it does not fit -16..32, so for the bone's transform p
  -> R (s * p) + t and its posed pivot c: `T = Ry180 c / 16`, `L = Ry180 R Ry180` (the
  quaternion (x, y, z, w) becomes (-x, y, -z, w)), `S = s / k`. The mob's scale multiplies T
  and S. `anim.PartPose`, `TestPartPose`.
- A model is built in the **reference pose**: what the rig plays for a mob standing idle on
  the ground (after two seconds, for controllers to settle), not the geometry's bind pose,
  which is a T-pose in most packs. The whole-mob model (shown beyond the animation range) is in
  that pose too: far Charizards no longer hang in a T-pose either.
- `q.modified_move_speed` is the game's walk animation speed (four times the blocks moved per
  tick, at most 1) and `q.modified_distance_moved` grows by it every tick: vanilla-style walks
  are `cos(q.modified_distance_moved * 38.17) * q.modified_move_speed * 80`. With blocks per
  second for both, legs swung 120 degrees.
- Packs hide bones by scaling them to 0 (`wing_open_*` scaled away while standing, `wing_closed_*`
  while flying). Such a bone has no place in the reference pose: its part is built in the first
  sampled pose that shows it, and a display with scale 0 draws nothing, which is what is wanted.

**Which bones get a display (`anim.MakePlan`).** The rig is run in eight situations (idle,
walking, running, hovering, flying, floating, swimming, sleeping; 12 poses each) and each bone
with cubes is scored by how far its cubes move against its parent (pixels, times the square root
of its size). Bones that do not move at all are merged into their parent for free; then, while
there are more than `anim.MaxParts` (24) groups, the lowest score is merged, a left bone
together with its right twin (or a mob limps). A merged bone keeps its reference-pose place on
its parent. Consequences: a bone that only moves in an animation none of the eight situations
plays (an attack, a one-shot quirk) is frozen; Charizard (130 bones, 81 with moving cubes)
keeps torso, one neck part, head, 2x2 arm parts, 2x4 wing parts, 4 tail parts, the flame and
2x2 leg parts, and loses fingers, jaw, tongue, claws, eyelids and one joint each of neck, tail
and feet. Where that shows: asleep, its curled tail has one segment sticking out; flying, the
tail's end is a little off. Raising `anim.MaxParts` trades that against packets and entities.

**Budget (`bridge/entities_anim.go`).** Per player: the 6 nearest animated mobs are stepped
every 3 ticks, the next 10 every 6 ticks, each with its interpolation duration set to that; a
mob further than 40 blocks (48 to leave), or beyond those 16, is one display with the whole
model. Only parts whose pose changed are sent. Worst case 384 part displays and 6*24*6.7 +
10*24*3.3 = 1760 entity data packets a second (about 40 bytes each); measured with 16 Pokemon
around the bot (they breathe, so every bone moves all the time): 232 displays, 1140 packets a
second. One mob near: 24 displays, 150 packets a second. A mob whose bones are still costs
nothing.

**Only mobs the player can see get a slot (`animSees`, `sightLine`, `clearLine`).** Before a
mob is ranked, a line from the player's eyes to five points of its box (low, middle and high on
its axis, and its two sides at half height as the player sees them) is walked block by block
through the chunks the bridge holds; a block stops it when it is solid and lets no light
through (`Palette.Solid` and `Filter` 15), so glass, leaves, water, fences, slabs and plants do
not. One clear line is enough: a mob half behind a hill counts. Chunks the bridge does not have
count as see-through. Each mob is tested once a second (`animSightTicks`), at most about 400
block lookups; an animated mob keeps its slot until three tests in a row failed, a static one
gets it at the first test that sees it. BetterModel's `sight-trace` is one ray from the eyes
to the entity's location per tick with no hysteresis; ours is cheaper per second and does not
flicker at an edge. Not covered: the player's view direction (a mob behind the player keeps
its slot: turning round would otherwise show a static mob for up to a second).
Tests: `TestAnimSight` (wall, glass, a hole, a tall mob, a mob at the wall's end, one slot);
live `/root/jbtest/sightlive.sh`: a Charizard behind a stone wall stayed static while a Pikachu
in the open animated, was animated a second after the wall was removed and static again three
seconds after it came back.

**What is not evaluated degrades to still.** A channel whose Molang does not compile or uses a
query the bridge has no answer for is left out (that bone keeps still on that channel); an
animation that is not in the packs (vanilla `animation.humanoid.*`, which add-on NPCs reuse) is
skipped; a transition on such a condition never fires; an animation played on such a condition
is not played. Script lines with unknown queries still run (the query is 0), or their variables
would stay unset. `Rig.Notes` lists all of it (`animpreview` prints it). Not done: particle and
sound effects of animations, `relative_to`, locators, loops and arrows in Molang, render
controllers that switch geometry or textures by variant (the first is used, as before),
materials (emissive layers).

**Costs.** Converting the test world's packs (the Cobblemon port: 992 entities, 930 animated)
takes 12 s instead of 3.5 s (9 s of it the entities
on 4 cores), on the first Java login after a start. 14300 part models:
the Java pack grows from 12.9 MB to 28.1 MB (each part needs a model and an item definition
file; 28600 files with about 230 bytes of zip overhead each are a good third of the growth; one
item definition per entity that picks the part by custom model data would save half of those
files). The animation files a
conversion used are kept compressed (5.6 MB for Cobblemon) and a type's rig is compiled when a
mob of that type is first shown, for its parts' bones only.

**How to test.**

- `go test ./internal/molang ./internal/anim ./internal/javapack ./bridge`:
  keyframes, controllers, the pose maths, the plan, a converted fixture entity (elements inside
  -16..32, UVs inside the texture), and `TestAnimatedMob` (displays spawned as passengers, updates
  with the interpolation start, walking changes the controller's state, the 6/10/rest tiers,
  removal).
- Pictures: `go build -o animpreview ./cmd/animpreview; ./animpreview -pack <unpacked pack> -out
  dir 'cobblemon:p0006_charizard=walk@0,0.25,0.5' 'cobblemon:p0016_pidgey=fly'`. Left: the
  Bedrock geometry with every bone posed (packconv's renderer, Bedrock model space); right: the
  part models from the converted zip placed by the display values the bridge would send. With
  `-parts 500` (a display for every bone) the two differ in 0.00 to 0.04 % of the pixels; with
  the budget of 24, up to 0.9 % for Charizard walking and flying and 1.9 % asleep (the frozen
  tail segment), and 0.0 % for mobs with fewer moving bones than the budget (Eevee: 5 parts). Look at the sheets: a number says the two sides agree, not
  that the pose is right.
- Live: `[core.options] debug_anim = "1"` logs `anim start/stop` and what each controller plays
  (`anim ... playing="pose=walking blink=open"`). jbbot counts the updates of part displays
  instead of logging them (`-animlog` logs each): step `animstat` prints one `ANIM mob` line
  per animated mob. `/root/jbtest/animlive.sh` (outside the repository) installs a wheel on the
  loopback server, summons mobs at the bot and prints both.
- `[core.options] animations = "off"` shows every custom mob as its one static model.
- Not tested: a real Java client. The bot checks packets, the pictures check the maths; nobody
  has looked at an animated mob in the game yet. First things to look at there: parts in step
  with the mob while it walks and turns (they ride it; their yaw is interpolated over the same
  ticks as their pose), no gaps at joints, and the frame rate with 16 mobs around.

## Custom entities with a vanilla shape (NPCs)

- **An add-on's own entity type can name a geometry only the vanilla pack has**
  (`geometry.villager_v2`, `geometry.humanoid.custom`). The converter never sees that geometry,
  so there was no model and the actor was not shown to Java at all (four NPCs of the Cobblemon
  port). `internal/javapack/vanillashapes.go` is the table of such shapes; one line per shape.
- What a shape becomes: a humanoid shape is a **mannequin** wearing the pack's texture; any other
  shape with the vanilla texture is the Java mob of that shape (`geometry.villager_v2` -> a
  villager); any other shape with a texture of the pack's own is a model built from the
  geometry in the table, when it has one (the old villager: `cobblemon:trainer_association`),
  because a Java mob cannot be given a texture per entity.
- **Mannequin** (Java 1.21.9+, in 26.2 and 26.3): a player-shaped entity that is not a player:
  no tab list entry, no profile lookup. Entity data 16 skin parts (byte), 17 profile
  (serializer 41), 18 immovable, 19 description (optional text: "NPC" under the name unless
  sent as none). The profile is `bool full` (false: partial = optional name, optional id,
  properties), then the skin patch: optional body, cape and elytra textures (resource
  locations) and an optional model (bool: slim). **The body texture is a resource location**:
  `delta:entity/x` is `assets/delta/textures/entity/x.png` of the pack the client has, so a
  texture of the converted pack is a skin with no skin server and no signature. Bedrock's
  64x64 skin layout is Java's.
- Which texture: the entity's first render controller. `textures: ["Array.skins[<Molang>]"]`
  gives the array's textures and the index expression, evaluated per actor (variant, mark
  variant, actor properties) and again on SetActorData; `Texture.x` gives one; anything else
  the entity's default. `cobblemon:rct_trainer` has 1559 skins picked by the property
  `cobblemon:trainer`: all are copied into the Java pack (3.2 MB of it).
- **Using them**: nothing special. A Java right click on any shown entity is a UseItemOnEntity
  interact (`entities_events.go`), which a script's `playerInteractWithEntity` and an NPC
  component both see. In the Cobblemon port all four answer with a script form
  (ModalFormRequest -> `forms.go` dialog): the nurse and the Poke Mart clerk an ActionForm of
  offers (the script cancels the interaction, so the `minecraft:npc` component's dialogue never
  opens), `rct_trainer` a `cbm:dialogue` form, the Trainer Association an ActionForm. A click
  on a dialog button goes back as the form response. The Java client opens no trade screen for
  a villager by itself; only the server can.
- Seen on the way: a player without a starter gets the `cbm:starter` form on every interaction,
  next to the NPC's own. A Bedrock client shows one form at a time and refuses the other; the
  bridge shows whichever came last on top. Not changed.
- Test: `bridge.TestStandIns`, `javapack.TestVanillaShapes`; live `/root/jbtest/npclive.sh`
  (outside the repository): summons each of the four at the bot, `expectent villager|mannequin`,
  `use`, `form N`; the bot logs a mannequin's profile as `17=profile(skin="delta:entity/...")`.
  jbbot's NPC handler used to eat the dialog packet before the form handler read the form id
  (`DIALOG form 0:` with nothing after it): fixed.

## Leads

- **BDS tells what holds a lead with actor data `LEASH_HOLDER`** (key 37, int64): the holder's
  unique id, `-1` (or `0`) for none, with the `LEASHED` flag (30) set alongside
  (`Actor::setLeashHolder`). It is in AddActor's full data and in SetActorData when it changes; BDS
  1.26.60 also repeats it in a SetActorData right after the AddActor.
- **Every actor unique id on BDS 1.26.60 is negative**, mobs as well as players (a wandering trader
  was `-38654705501`). Only `-1` and `0` mean "none"; treating negative ids as none hid every lead.
- **BDS adds a wandering trader after its trader llamas**, so the holder is often unknown when the
  leashed mob is added.
- Java 26.2 and 26.3 take `set_entity_link` (int32 mob id, int32 holder id, 0 for none) after the
  mob's `add_entity`. The client keeps the id and looks the holder up when it draws the lead
  (`Leashable.getLeashHolder`), but keeps the entity it found: a holder removed and added again needs
  the link again. The bridge gives a re-added entity a new Java id anyway.
- Code: `bridge/entities_leash.go`; `showEntity` sends a mob's lead and the leads of the mobs it
  holds, `removeEntity` takes those back (holder 0), SetActorData updates it (also before the
  entity is shown). Tests: `TestLeash`, `TestLeashBeforeSpawn`. Live: jbbot logs `ENT leash A -> B`;
  stand near a wandering trader.

## Inventory requests and shift-click

- A Java client applies a click itself (shift-click: vanilla `quickMoveStack` - stacks of the item
  first, then empty slots, the rest stays when room runs out; armour to its empty slot, a shield to
  the off hand; a crafting table's grid first) and sends the slots it changed. The bridge compares
  them with the server's slots and sends one ItemStackRequest; a shift-click becomes Place actions
  from slot to slot (a Bedrock client does the same; the source needn't be the cursor).
- **BDS 1.26.60 refuses a slot** (49 `FailedToValidateSrcSlot`, 50 `FailedToValidateDstSlot`) when
  its stack network id isn't the server's (an empty slot is 0; a slot changed earlier in the same
  request goes by the request id; a still unanswered earlier request's id also works for a few
  seconds), or when its container isn't part of the open screen: **with no inventory screen open
  (the HUD screen) every main inventory slot (container 29) is refused** (drops from the hotbar
  still work).
  A different item on the destination is 55. Found with a plain Bedrock client sending hand-made
  requests.
- **Opening the inventory**: Interact `OpenInventory` is answered by ContainerOpen type `0xff` with a
  window id from a 1..99 counter, not 0. A second open while it is open is ignored, and so is an open
  while a container (a chest) is open on the server (`Player::canOpenContainerScreen`). Our
  ContainerClose with window 0 works; BDS answers with ContainerClose of that window id (type `0xf7`).
- **A refused request changes nothing and BDS sends nothing**, so a stale stack id or a closed screen
  made every following click fail the same way (seen: 28 refusals with status 50 in a row, the items
  jumping back each time).
- **InventoryTransaction of type mismatch** (no actions) makes BDS send every player inventory again
  (windows 0, 119, 120, 124) with its current stack ids. After a refusal the bridge sends it (at most
  every 10 ticks) and opens the inventory screen again before the next click; the inventory screen's
  window id is kept so a close by that id isn't missed. Two stacks of one item whose counts look
  exchanged (a full stack shift-clicked onto a partial one with no room for the rest: 64/60 ->
  60/64) are a move of the difference, not a swap.
- Not found: what put the owner's session in that state; the translation worked everywhere it was
  tried (inventory, chest, barrel, shulker box, ender chest, hopper, dropper, crafting table,
  armour, off hand, rapid clicks and bursts, after creative, death, teleport, game mode changes and
  block placing).
- Code: `bridge/inventory.go` (`click`, `plan`, `responses`, `refused`, `openInventory`),
  `bridge/windows.go` (ContainerOpen/ContainerClose). Tests: `bridge/inventory_quickmove_test.go`.

## Sleeping

- **BDS puts a sleeper at the bed's head block + (0.5, 0.90625, 0.5)**, and that is the player's
  eye position: while asleep its box is 0.2 x 0.2 with the eyes 0.2 above the feet
  (`Player::startSleepInBed`, 1.21.3 dump; 1.26.60 sends the same numbers). It sets the SLEEPING
  flag (flags 2, not flags 1), the bed position (actor data 28: the head block; it is also the
  spawn bed, so it is only re-sent when it changes) and the respawn point, and tells the
  sleeper's own client with a CorrectPlayerMovePrediction, not a MovePlayer.
- Translating that position like any other (eyes minus 1.62) put the Java player 0.71 below the
  bed block, inside the floor: the client fell, the bridge sent the fall to BDS, BDS corrected it
  back, and so on for as long as the player lay there ("falling through the bed").
- **A Java client lies in its bed by itself** once its own entity data has `sleeping_pos` (14) and
  the block there is a bed on the client (`LivingEntity.setPosToBed`: the bed's top + 0.125, then
  it falls onto the top, 0.5625). It takes no input then and opens the bed screen; "Leave Bed" and
  escape send `player_command` STOP_SLEEPING (action 0 on 26.2 and 26.3). So the bridge sends that
  data, never moves the client to the server's sleeping position, and sends BDS inputs at BDS's
  own position with no movement until the flag goes.
- A Bedrock client sends PlayerAction StartSleeping once it is in bed (BDS answers with "X is
  sleeping in a bed..." to the others) and StopSleeping to get up. On waking (the button,
  morning, `/time set day`) BDS clears the flag, animates StopSleep and puts the player next to
  the bed (eyes 1.72 above the block) with a correction; the bridge sends WAKE_UP (animate 0 on
  26.3, 2 on 26.2: the client casts the entity to a player) and teleports the client there.
- Other sleeping players: a Java client draws them lying at their position, so the bridge places
  them on the bed's top (BDS's y minus 0.34375 instead of 1.62; AddPlayer has the feet, 0.2 under
  that), and moves them again when the flag changes, whichever comes first.
- Bed messages (night only, monsters, too far, home bed missing, respawn point set) come as Text
  rawtext and show in chat, as on Bedrock.
- Code: `bridge/sleep.go`; tests `TestSleepSelf`, `TestSleepOtherPlayer`. Live: jbbot `useon` on a
  bed at night, `sleepstat`, `wake` (`SLEEP` and `SELF` lines); a second bot logs the sleeper's
  `ENT data` (6=2, 14=bed).

## jbbot (scripted Java client)

- Build: `cd core && go build -o jbbot ./cmd/jbbot`. Run: `jbbot -addr HOST:PORT -name NAME
  -blocks <26.3 generated/reports/blocks.json> -jlog -secs 60 -script 'wait 16; inv; click 36 0 1;
  quit'`. Steps are in the source (`main.go`, `inv.go`, `entities.go`, ...).
- `-velocity FILE` (Velocity's `forwarding.secret`): the bot answers the `velocity:player_info`
  login query as the proxy would (HMAC-SHA256 of version 1, address, the offline UUID of `-name`, the
  name, no properties), so it can join a bridge set up with `velocity_secret(_file)` directly.
- Inventory steps: `click SLOT BUTTON MODE` (mode 1 = shift-click; the bot moves items as vanilla
  does, armour and shield rules included), `qclick` (the same without waiting for the answer),
  `burst S1 S2 ...` (shift-clicks back to back, like a shift-double-click), `inv`, `close`, `hold N`,
  `useon X Y Z FACE`, `expectslot`. `ents` lists entities; adds and leads are logged as `ENT`.
- Sleep steps: `wake` (Leave Bed), `sleepstat`; the bot lies in a bed as a 26.3 client does when
  its own entity data says so (`sleep.go`).
- The bot is not an operator: give items and effects from the server console. On a flat world at
  night mobs push it around; give it resistance and teleport it somewhere quiet (or onto a platform)
  before clicking blocks.
- A changed core can be tried without installing it: a small main that imports `core/service` and
  calls `host.Start` with `{"listen": <another port>, "bedrock": <the server's RakNet address>,
  "options": {"account_mode": "offline"}}` runs the bridge as a normal process next to the server.
