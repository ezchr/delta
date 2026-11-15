# Origin of the core

`core/bridge`, `core/internal/{blocks,javamap,javapack,sounds}`, `core/cmd/jbbot` and
`core/tools/cmdcheck` started (2026-11-15) as a copy of the same author's standalone
Java-to-Bedrock bridge (a separate program that connects Java players to any Bedrock server,
including friends' worlds and Realms through Xbox Live). Fixes made there can be ported here file
by file: the file names and the handler structure were kept.

## Taken over unchanged (apart from names)

Every translation area: world/chunks (`world.go`, `subchunk.go`), own state and movement
(`self.go`, `movement.go`, `respawn.go`), entities (`entities_*.go`), windows/inventories/recipes/
stations/trading/mounts (`windows.go`, `inventory.go`, `items.go`, `recipes.go`, `stations.go`,
`station_*.go`, `trade.go`, `mounts.go`), breaking/interaction (`breaking.go`, `interact.go`,
`liquid.go`, `food.go`, `hold.go`), block entities (`chests.go`, `doors.go`, `signs.go`,
`blockentities.go`, `cmdblock.go`), text/commands (`text.go`, `lang.go`, `commands.go` tree part,
`cmdtree.go`, `ping.go`, `leave.go`), forms/NPCs (`forms.go`, `npc.go`), scoreboard, sounds,
particles, resource packs (`packs.go`, `entities_custom.go`), login client data and skins
(`clientdata.go`, `skin.go`).

## Removed

* Xbox accounts and token files (`account.go`), friends' worlds (`friend.go`), Realms
  (`realms.go`), the network control endpoint (`control.go`), the PC "local mode" with login
  cookies (`localmode.go`), the Velocity forwarding and premium-name check in `serve`, the hub
  and `/join`/`/hub`/`/bb world|realm|servers|connect` commands, short names.
* The tools that need the full game data or a Dragonfly checkout to regenerate tables
  (`genblockdata`, `genbreakdata`, `genjavastates`, `genjavatextures`, `gensounds`,
  `genrecipesets`): the generated `*_gen.go` files are kept as data.

## Changed

* One target: every player is connected to `Config.Target` (the server's own port). A dial
  slower than 8 s shows the waiting area meanwhile; a failed dial or a server-side disconnect
  disconnects the Java player with the reason (the original moved them to the waiting area).
* `config.go`: `Config` (target, data dir, identity provider, options), `Run`/`Stop` that can run
  any number of times in one process, `Players()`, panic-safe goroutine helpers.
* `identity.go` (new): `IdentityProvider`, default `OfflineIdentity`.
* `player.go`: `serve` without accounts; every Java packet/tick in `locked` (per-packet panic
  recovery that releases the lock and disconnects only that player).
* `remote.go`: dial with the identity provider's login (RakNet or `tcp:`), per-packet panic
  recovery in the read loop, `startReading`/`wait`, chunk capture for tests, unknown block
  counting.
* Packs: settings from `Config.Options` instead of the environment, resettable, the HTTP server
  stops with the bridge; off by default.
* Device ids and the skin cache under the data directory.
* `/jb status|ping|reconnect` instead of `/bb ...`; names shown to players come from
  `ProductName`; the form/NPC click action ids are `javabridge:form`/`javabridge:npc`; the pack
  namespace is `javabridge`.
* Logging: most per-player lines are debug.

## Online identity (agent B)

* `bridge/online_identity.go`: `OnlineIdentity`, the derived well-formed XUID, the per-start trust
  secret and `SelfSignedId = uuid5(namespace, xuid)`.
* `service/bridge.go`: `account_mode` (premium default), the identity provider, the per-start
  secret exposed via `jb_call("identity_secret")`, `session_server` and Velocity modern-forwarding
  options.
* `bridge/remote.go`: stitches the secret (Nonce) and SelfSignedId into the online login and keeps
  the derived XUID (`KeepXBLIdentityData`).
* `plugin/src/endstone_javabridge/auth/`: the bundled xuidforward hook and the plugin-side staging
  (loopback + constant-time secret). See docs/THIRD_PARTY.md for the credit.
