# Delta

**Delta plugin for Endstone: allows Java players to play on Endstone BDS servers.**

> **Linux and Windows.** Delta runs on Linux x86-64 servers (glibc 2.34 or newer, as on Ubuntu
> 22.04, Debian 12 and later) and Windows x64 servers (BDS 1.26.65.1, Endstone 0.11.16).

Delta is one [Endstone](https://github.com/EndstoneMC/endstone) plugin. Drop the wheel into
`plugins/`, start the server, and Java Edition players can join it next to Bedrock players. There
is no separate proxy to run: Delta works inside the server. Java players are ordinary players to
BDS, so commands, permissions, ops and other plugins work on them.

## What it does

- **Java 26.2 and 26.3 clients.**
- **Accounts:** `online` (premium Mojang accounts only, the default), `hybrid` (premium players keep
  their account, cracked players can still join) or `offline`. Each player keeps one stable identity
  (XUID) on the server, so ops, permissions and saves stay with them.
- **Skins:** Java skins (classic or slim arms, and the cape) show to Bedrock players, and Java players
  see each other's skins. Cracked players get Steve.
- **Resource packs:** the server's Bedrock packs are converted to a Java pack when the first Java
  player joins: blocks, items, mobs, armour, 3D held items (swords, axes and such) and a
  vanilla-texture fallback for blocks Java doesn't have. Players get the normal Java download
  prompt and download from the Java port itself: nothing to set up, unless they join through a
  Java proxy ([CONFIG.md](docs/CONFIG.md#resource-packs-packs)).
- **Menus:** Bedrock forms open as Java dialogs; chest menus stay chest menus.
- **The world:** signs, item frames, floating text, leads, sounds and particles (mapped with
  Geyser's tables), flying, sneaking, sprinting. Block and attack reach match Bedrock's.
- **Both BDS transports:** NetherNet and RakNet, picked from `server.properties`. No extra Bedrock
  port is needed.

## Install

1. A Linux x86-64 or Windows x64 server with Endstone 0.11 running BDS 1.26.60.6 (Endstone
   0.11.15, Linux) or BDS 1.26.65.1 (Endstone 0.11.16, Linux and Windows). Both speak the Bedrock
   protocol Delta uses.
2. Put the wheel for your system in the server's `plugins/` folder:
   `endstone_delta-<version>-py3-none-linux_x86_64.whl` on Linux,
   `endstone_delta-<version>-py3-none-win_amd64.whl` on Windows.
3. Keep `online-mode=false` in `server.properties` (Endstone's default). Delta checks Java accounts
   itself; BDS's own online mode would refuse Java players.
4. Start the server. Delta writes `plugins/delta/config.toml`; Java players join on port 25565 by
   default (`[java] listen`).

All settings: [docs/CONFIG.md](docs/CONFIG.md).

## Behind proxies

- **Java through Velocity:** add the server to Velocity like any Java server and give Delta the
  forwarding secret (`velocity_secret_file` under `[core.options]`). Velocity then checks the
  accounts, and `/server` moves Java players between Delta and your other servers.
- **Bedrock through Beacon or another proxy:** Bedrock players keep their real Xbox XUID. Delta
  trusts a proxy on the same machine by default, or one elsewhere by Beacon's signed login
  (`forwarding_secret_file` under `[bedrock]`).

## xuidforward

Delta includes [xuidforward](https://github.com/ezchr/xuidforward). If you use it:

- **Replace it:** delete the xuidforward plugin and change nothing else. Delta reads its
  `config.json` (allowed addresses, Beacon forwarding secret, server name).
- **Or keep both:** they work together. Delta loads after xuidforward and uses its hook.

## Known issues

- **Movement corrections:** Java players are sometimes pulled back slightly. Bedrock servers check
  movement differently from Java, and not every case is matched yet:
  - sprint-jumping into block corners, where Bedrock keeps sprinting in the air after Java stopped;
  - walking into end gateways, which are solid on Bedrock but not on Java;
  - a short correction after knockback.

  Normal walking, sprinting, jumping, sneaking and flying match without corrections.
- Bedrock's 3D armour shapes (horns, shoulder pads) show as flat armour on Java.
- Add-on particles and a few Bedrock-only ones have no Java equivalent and are skipped.
- Skin lookups for cracked players' names can hit Mojang's rate limit and fall back to Steve.
- Tested on Endstone 0.11.15 with BDS 1.26.60.6 and Endstone 0.11.16 with BDS 1.26.65.1. A new BDS
  build also needs a new profile for the identity hook; until then players join without stable XUIDs.

## Documentation

- [CONFIG.md](docs/CONFIG.md): every setting
- [LOGIN.md](docs/LOGIN.md): accounts and identity
- [PACKS.md](docs/PACKS.md): resource pack conversion
- [NOTES.md](docs/NOTES.md): what BDS and the Java client actually do, learned the hard way
- [ARCHITECTURE.md](docs/ARCHITECTURE.md): how it works and where things are
- [STATUS.md](docs/STATUS.md): what's tested, how to build and test

## Building

Linux x86-64, Go 1.26+, Python 3: `VERSION=<version> ./build.sh` writes
`dist/endstone_delta-<version>-py3-none-linux_x86_64.whl`.

## Credits

- [go-mcjava](https://github.com/ezchr/go-mcjava): the Java protocol.
- [gophertunnel](https://github.com/Sandertv/gophertunnel) and parts of
  [Dragonfly](https://github.com/df-mc/dragonfly) (MIT): the Bedrock side.
- [Geyser](https://github.com/GeyserMC/Geyser)'s mappings (MIT): sounds and particles.
- See [THIRD_PARTY.md](docs/THIRD_PARTY.md).

## License

MIT
