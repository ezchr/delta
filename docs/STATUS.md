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

## What is stubbed / not done

* Identity: `OfflineIdentity` only (Java name, UUID derived from the Java UUID, no XUID). The
  Java listener runs offline (`online_mode` option exists, untested). Agent B owns this.
* Resource packs: off by default (`packs = on` in options works as in the original bridge, but the
  Java client must reach the pack HTTP server: `pack_listen`/`pack_url`). Agent C owns packs.
* Skins of other players: looked up from a public skin API by XUID; offline Bedrock players have
  none, so they are Steve/Alex.
* `/jb status|ping|reconnect` is the only bridge command.
* Windows build: not done (see Build).

## Open problems (by risk)

1. **NetherNet-only servers**: a BDS with `transport=nethernet` cannot be joined over loopback
   without an Xbox-signed identity (NetherNet refuses anonymous peers). Main target (RakNet) is
   fine. Possible ways: an Xbox account token for the bridge, or hooking BDS's network layer
   in-process. Not started.
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
