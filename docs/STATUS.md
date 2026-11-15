# Status

Last update: Stage 1 (join, world, movement, chat), 2026-11-15.

## Spike: Go c-shared inside BDS (PASSED)

A Go 1.27 `-buildmode=c-shared` library loaded with ctypes from the Endstone 0.11.15 Python plugin
inside BDS 1.26.60 (Linux x86-64), `mode = "spike"` (`core/service/spike.go`, `/jbcore stress N`):

| Check | Result |
|---|---|
| Load + `jb_start`, TCP listener inside BDS | works |
| Tight loops on every P + forced GCs (needs async preemption, SIGURG) | 16 GCs done while 8 goroutines spun; no stall |
| `panic` in a goroutine (`host.Go`) | recovered, logged through Endstone's logger |
| nil pointer dereference (SIGSEGV in Go code) | becomes a recoverable Go panic; crashpad (BDS's crash handler) records nothing |
| write after `shutdown(SHUT_WR)` (EPIPE, SIGPIPE) | write returns "broken pipe"; process unaffected |
| write to a reset connection | error returned; process unaffected |
| 5x `/reload` (stop + start of the core) | goroutines back to the baseline each time, port rebinds |
| BDS `stop` | "Quit correctly", no crash report |
| logs | polled every tick by a scheduler task; no calls from Go into Python |

Constraints found: the library must be loaded once per process (a Go runtime cannot unload; a
second copy would be a second runtime) - `_native.load` keeps it on the `sys` module and loads
from a content-addressed copy. Endstone reinstalls wheels on `/reload`, so a changed wheel takes
effect after a restart (the plugin warns). A method called `_config` on the Plugin subclass is
shadowed by Endstone (it was `None`): avoid that name.

## Stage 1 (DONE, verified with a real Java client)

Real client: Fabric 26.2 (protocol 776) with the MCPFabric test mod, offline account
`S2JavaTest`, through an SSH tunnel to `127.0.0.1:25890` on the test server (Endstone 0.11.15,
BDS 1.26.60, `online-mode=false`, `transport=raknet`). Second player and scripted checks:
`core/cmd/jbbot` (Java 26.3, protocol 777). Screenshots: see the report of this stage.

| # | Item | Verified how | Result |
|---|---|---|---|
| 1 | c-shared spike | see above | works |
| 2 | Java login/config/play, offline | real 26.2 client and 26.3 bot joined; registries/tags/known packs from the Java protocol package | works |
| 3 | Bedrock side: loopback join, resource pack handshake, StartGame | BDS logs "Player connected/Spawned"; packs answered (none needed on the test server) | works |
| 4 | World: LevelChunk + SubChunk request mode, block updates, time, weather, respawn | world renders (forest, snow biome, frozen lake, rain/snow); 369 columns, 0 decode errors, 0 retries; unit test compares 6.4M blocks of 65 captured columns | works |
| 5 | Movement | 48 + 93 blocks walked/sprinted with jumps over 9 chunk borders: **0 server corrections**; sneak (edge stop on ice), fall damage and death match BDS ("fell from a high place") | works |
| 6 | Other players, mobs, chat, commands, disconnect reasons, keep-alive | second Java player (bot) visible with name tag and moving; cow/zombie/chickens; chat Java->Bedrock and console->Java; `/list`; kick reason shown ("Kicked by the server: ..."); stayed **6 min 44 s** in one session (through three deaths/respawns, until the kick test ended it) | works |
| 7 | Lifecycle | Java leave -> BDS "left the game"; kick -> Java disconnect with reason; `/reload` -> Java "The server is restarting."; goroutines back to 3 after every player left | works |

Also verified on the real client (from the original bridge, unchanged): combat (hitting a cow and
a zombie: knockback both ways, death "slain by Zombie", respawn), inventory sync (`/give`), placing
a chest, opening it, moving items in and out (server kept them: reopened), death/respawn screens.
Survival block breaking verified with the scripted bot (start/finish dig: the chest broke on the
server). Not verified live in this stage: crafting, furnaces/stations, forms/NPC dialogues,
trading, mounts, scoreboards, signs, resource packs (unit tests from the original bridge cover
forms, NPC dialogues, command blocks, sounds, block entities, doors, packs).

## Stage 2 - login and identity (DONE, 2026-11-15)

Premium Java accounts are the default (`account_mode=online`). A Java login is verified with Mojang
(encryption + session-server `hasJoined`); the verified profile drives the Bedrock identity. The
bundled native hook (owner's xuidforward) writes a stable derived 16-digit XUID for the loopback
login on an `online-mode=false` BDS. "online mode" here is the JAVA account mode, separate from
BDS `server.properties online-mode` (which must stay false; the plugin warns otherwise).

| # | Item | Verified how | Result |
|---|---|---|---|
| a | Online Java login ACCEPTED on BDS, player keyed on the stable XUID | headless 26.3 bot over loopback: `online identity: staged ... xuid=2535996142166303` -> BDS `Player connected/Spawned ... xuid: 2535996142166303`, walked 369 chunks | works |
| b | A login WITHOUT the secret / not from loopback is NOT staged | plugin trust-gate unit test (`plugin/tests/test_hook_trust.py`, 7 cases, constant-time compare, loopback+secret both required) + live: a no-secret login connects with an EMPTY xuid | works |
| c | Identity STABLE across logins | three sequential joins all keyed `xuid: 2535996142166303` | works |
| d | Real Bedrock client + javabridge player together: player list does not crash | Cinnabar (Xbox-auth gamertag, real XUID) + javabridge player on one BDS; broadcast entry decoded (0 errors) and player rendered in 3D point-blank; no crash/panic in the client | works (no crash) |
| premium | Verified Mojang UUID drives identity; invalid session rejected; stable | stubbed-session-server Go tests (`core/service/premium_test.go`): accepted login uses the VERIFIED profile UUID (not the handshake name), a 403 session is refused, same account -> same XUID | works |

Fail-safe: on a BDS build with no bundled profile, ONLINE identity is disabled with a log line and
offline still works; BDS is never crashed.

Open manual confirmations: a vanilla retail Bedrock client opening the pause-menu player list
(Cinnabar has no such screen; the crash mechanism was exercised); a 1.21.x client fully playing a
26.3 javabridge through ViaVersion (premium auth via Velocity forwarding is PROVEN with a real
account, but the 1.21->26.3 registry translation drops the client - see docs/LOGIN.md).

## What is stubbed / not done

* Identity: DONE. `OfflineIdentity` (offline) and `OnlineIdentity` (premium, the default): premium
  Mojang verification on the Java side + the bundled xuidforward hook gives each Java player a
  stable derived XUID on an `online-mode=false` BDS. See docs/LOGIN.md, CONFIG.md and Stage 2 below.
* Resource packs: off by default (`packs = on` in options works as in the original bridge, but the
  Java client must reach the pack HTTP server: `pack_listen`/`pack_url`). Agent C owns packs.
* Skins of other players: looked up from a public skin API by XUID; offline Bedrock players have
  none, so they are Steve/Alex.
* `/jb status|ping|reconnect` is the only bridge command.
* Windows build: not done (see Build).

## NetherNet servers (DONE, 2026-11-15)

A BDS with `transport=nethernet` (BDS 1.26.60.6, Endstone 0.11.15) opens no RakNet port, so the
RakNet loopback dial was refused. Now the plugin reads `server.properties` (`[bedrock] transport =
"auto"`) and the core dials the server's NetherNet signaling (`http://127.0.0.1:<server-port>`)
instead (`core/bridge/nethernet.go`, CONFIG.md "Bedrock transport").

Approach chosen: a NetherNet dial in the core, with a **self-signed connection identity, no Xbox
token**. Rejected: making BDS also listen on RakNet. In 1.26.60 `NetworkSystem` holds ONE
`RemoteConnector` (a `RakNetConnector` or a `NetherNetConnector`, picked from `transport`), not the
old composite connector; a second, RakNet listener would mean constructing a `RakNetConnector`
in-process (stripped binary, unknown constructor and layout), wiring its connection callbacks,
ticking it and routing closes for its peers - per build, with a real risk of crashing BDS. The
NetherNet dial needs no patching of BDS at all.

| Check | How | Result |
|---|---|---|
| Anonymous NetherNet offer | standalone go-nethernet probe -> test BDS | refused, signal error 37 (also with `online-mode=false`) |
| Self-signed identity (fresh P-384 key, `idp.domain` "self" or the auth service URL) | probe | accepted (~0.7-1.1 s) on `online-mode=false` AND `online-mode=true` |
| Server-style identity (JWK token) | probe | refused (37) |
| Identity key != Login key | first bridge build | BDS closes right after the Login ("the server refused: no reason given"); fixed by signing the NetherNet identity with the Login's key (gophertunnel `keyDialer`, THIRD_PARTY.md) |
| Java join, account_mode=online + hook | 26.3 bot | `online identity: staged ['NNBot1'] xuid=2535991169975246` -> `[xuidforward] restored xuid=...` -> `Player connected/Spawned: NNBot1 xuid: 2535991169975246`; 369 chunks, chat, `/list`, sprint walk |
| Loopback trust gate | same | staged = Endstone saw the NetherNet peer as 127.0.0.1 (loopback-only ICE candidates) |
| account_mode=offline | bot | joined with an empty XUID, 369 chunks, chat |
| Two Java players at once | 2 bots | both joined, keyed on their own XUIDs, both left cleanly |
| `/reload`, leak | rejoin after `/reload`; `/jbcore goroutines` | rejoin works; goroutines back to 3 |
| BDS restart right after a join | stop + start at once | starts (signaling connections are closed with a reset, so BDS's port is not left in TIME_WAIT; with plain closes BDS failed with "Port [..] may be in use" for a minute) |
| RakNet regression | same server, `transport=raknet` | joined, 369 chunks, chat |
| `online-mode=true` | bot | NetherNet connects, the self-signed Login is refused (same as RakNet; the plugin's existing warning applies) |

Limits: relies on BDS accepting self-signed NetherNet identities (true on 1.26.60.6; see Open
problems 1). Each join costs one HTTP request to the local signaling port and a WebRTC handshake
(~0.7 s on loopback). The NetherNet ports must accept loopback traffic.

## Open problems (by risk)

1. **NetherNet servers depend on BDS accepting a self-signed connection identity.** Done (see
   "NetherNet servers" above). If a future BDS build insists on an identity issued by Xbox
   services, the fallback is an Xbox account token for the bridge (one account signs every
   connection's NetherNet identity; the Bedrock login stays self-signed); not built.
2. **Knockback/teleport corrections**: when mobs hit the player, BDS corrects by ~0.5-1 block
   (logged at debug "server corrected our movement"); after `/tp` into unloaded chunks the client
   falls a few blocks before the column arrives and gets corrected back (BDS does not kill for it).
3. **Breaking with a client that aborts and finishes** the same block (the MCPFabric test tool
   does: start, abort, finish): the bridge drops the finish; vanilla Java servers accept it. Real
   clients hold the key and are unaffected; worth matching vanilla later.
4. **Two Go runtimes**: if another plugin also loads a Go c-shared library, two runtimes share
   the process (works in practice for most hosts, not guaranteed).
5. Debug lines are shown at INFO with a `[debug]` prefix (Endstone's plugin loggers drop debug).
6. Logging volume: most per-player lines are debug now; "java player joined/left", kicks and
   block-mapping summaries stay at INFO.

## Java versions

Java Edition **26.3** (protocol 777, native) and **26.2** (protocol 776, translated by the Java
protocol package). Older/newer clients are refused at login.

## Build

On Linux amd64 with Go >= 1.26 and Python 3:

```
./build.sh                       # build/libjavabridge-linux-amd64.so + dist/*.whl
VERSION=0.1.1 ./build.sh         # PEP 440 version
```

On a shared machine: `BUILD_WRAP='flock /tmp/build.lock env GOMAXPROCS=2 nice -n 19' GOFLAGS_EXTRA='-p 1' ./build.sh`.
The library needs glibc >= 2.34 (BDS itself needs newer). Wheel tag `py3-none-linux_x86_64`.

Windows (not done yet): `GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc go
build -buildmode=c-shared -o build/javabridge-windows-amd64.dll ./cmd/libjavabridge` needs mingw-w64
on the build machine; `_native.library_filename()` already looks for that name; the wheel tag would
be `win_amd64` (`tools/mkwheel.py --plat win_amd64`). On Windows a loaded DLL cannot be replaced,
which matches the load-once rule.

## Install

Copy `dist/endstone_javabridge-*.whl` into the server's `plugins/` and start the server. The
plugin writes `plugins/javabridge/config.toml` on first start:

```toml
[java]
listen = "0.0.0.0:25565"
motd = "..."
[bedrock]
port = 0            # 0 = the server's own port
transport = "auto"  # follows server.properties transport (raknet / nethernet)
[core]
mode = "bridge"
debug = false
[core.options]      # optional, strings: view_distance, online_mode, packs, pack_listen, pack_url,
                    # lang_file, capture_dir (tests)
```

## Test

* `cd core && go test ./bridge ./internal/... ./host` (unit tests, no server needed):
  `TestCapturedChunks` (BDS 1.26.60 chunks -> Java columns, block by block),
  `TestPaletteMapping`, `TestMovementInput`/`TestCorrectionNudges` (PlayerAuthInput building),
  host tests (log queue, panics), plus the original bridge's tests (commands, forms, NPCs,
  sounds, block entities, packs...). `TestJavaClientCommands` needs `JB_CLIENT_JAR` and
  `JB_CLIENT_LIBS` (a 26.3 client jar and libraries) and is skipped otherwise.
* `core/cmd/jbbot`: scripted Java 26.3 client: `go build -o jbbot ./cmd/jbbot`, then
  `./jbbot -addr 127.0.0.1:25565 -name Bot -script 'wait 5; chat hi; cmd list; walk 3 sprint; quit'`.
  Steps are listed in its source (main.go, inv.go, entities.go ...).
* `/jbcore <method> [json]` (operators) calls the core: `players`, `status`, `goroutines`,
  `stacks`, `debug true|false`.
