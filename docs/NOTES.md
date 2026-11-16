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
  mirrors the pose for the off hand but not the model.

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
