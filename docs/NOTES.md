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

- Bedrock 3D armour geometry (horns, shoulder pads) can't be drawn by Java's armour layers: sets with
  their own geometry use the 2D textures from the pack's 2D subpack, and a texture is built from the
  model when there is none.
- 3D held items use only the attachable's first pose; animations and Molang are ignored. Java
  mirrors the pose for the off hand but not the model.
