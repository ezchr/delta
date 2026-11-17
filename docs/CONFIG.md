# Configuration (docs/CONFIG.md)

Delta is one Endstone plugin. On first start it writes `plugins/delta/config.toml`:

```toml
[java]
listen = "0.0.0.0:25565"   # address Java players connect to
motd = "A Bedrock server, open to Java Edition"
account_mode = "online"    # "online" (premium, DEFAULT), "hybrid" or "offline" (see below)

[bedrock]
port = 0                   # this server's own Bedrock port over loopback; 0 = server.properties
transport = "auto"         # "auto" (follows server.properties transport), "raknet" or "nethernet"

[core]
mode = "bridge"            # "bridge" (normal) or "spike" (native self-test)
debug = false

# [core.options]           # advanced, rarely needed (strings); see below
```

## Account mode (`[java] account_mode`) — the important one

`account_mode` is **the Java account mode**, i.e. how a Java player proves who they are. It is
**separate from and unrelated to BDS's `server.properties` `online-mode`** (see the big note
below).

* **`online` (the default — premium):** every Java login is verified with Mojang: the encryption
  handshake plus the session-server `hasJoined` check. Names and UUIDs cannot be spoofed; a player
  who does not own the Mojang account is refused at login. Each verified account is given a
  **stable Bedrock identity** on this server (a derived 16-digit XUID), so player data, permissions
  and bans key on the same id across sessions and the player shows up correctly to real Bedrock
  players. This needs the online-identity hook (bundled; see LOGIN.md).
* **`hybrid` (premium + cracked players):** like the HybridAuth plugin on a Velocity proxy. At
  each login the name is looked up with Mojang (`api.minecraftservices.com`, falling back to
  `api.mojang.com`):
  * **a premium account's name** must log in the online way (encryption + session-server
    `hasJoined`). The **verified** profile is used, so the owner gets exactly the identity they have
    in `online` mode: same Java UUID, same derived XUID, same player data, permissions and skin. A
    cracked client using a premium name cannot do that login and is refused (a client that tries
    and fails gets "`<name>` is a premium Minecraft account. Log in with that account to use this
    name; cracked players must choose another name.").
  * **any other name** is let in offline as a cracked player: the offline UUID
    (`OfflinePlayer:<name>`) and a derived XUID from it, so cracked players also keep their data
    between sessions. Anyone can use a free name — cracked servers need their own password login
    if that matters.

  Answers are cached in `plugins/delta/premium_cache.json` (premium names 24 h, other names
  1 h; kept up to 30 days as a fallback). **When the lookup fails** (Mojang down, HTTP 429 rate
  limit, network error, or more than 120 lookups in a minute) the last cached answer is used, even
  an expired one; with nothing cached the name is treated as **premium** (fail closed: its real
  owner still gets in, a cracked player with that name is refused until the lookup works). Every
  decision is logged (`hybrid login: premium name, Mojang login required name=...`,
  `hybrid login: not a premium name, admitted offline (cracked) ...`,
  `hybrid login: premium lookup failed ...`, `java login refused: Mojang login failed ...`).
* **`offline` (opt-in — cracked/private servers):** no verification. Anyone may join under any name.
  A clear warning is logged (`account_mode=offline: Java logins are NOT verified with Mojang ...`).
  The identity is derived from the name only (no XUID), as a stock offline Bedrock login. Use this
  only for a private or cracked server where you accept name spoofing.

Back-compat: the old boolean option `online_mode` ("true"/"false") still works and maps to
`online`/`offline`; `account_mode` wins when both are set.

Behind a Velocity proxy with modern forwarding (`velocity_secret`) the proxy authenticates the
players and the listener does no Mojang checks in any mode; the mode then only picks the identity
(`online`/`hybrid`: derived XUID, `offline`: none).

## BIG NOTE: "online mode" means the Java account mode, NOT BDS online-mode

There are two different "online mode" settings and they are unrelated:

| Setting | Where | What it does for Delta |
|---|---|---|
| **Delta `account_mode`** | `plugins/delta/config.toml` | Premium Mojang verification of **Java** players. Keep it `online`. |
| **BDS `online-mode`** | `server.properties` | Xbox verification of **Bedrock** logins. **Keep it `false`.** |

**Set BDS `online-mode=false`** (Endstone's default). Delta's Java players reach BDS over
loopback with a self-signed Bedrock login; BDS `online-mode=true` REFUSES self-signed logins during
validation, so Java players could not join at all. Delta does the account verification itself
(premium Mojang, on the Java side) and the bundled hook gives each Java player a stable XUID — so
BDS `online-mode` adds nothing and only blocks Java players. If you leave BDS `online-mode=true`
with the hook enabled, the plugin logs a warning telling you to set it to `false`.

(Real Bedrock players are unaffected either way: they still log in with their own Xbox identity.)

**Troubleshooting:** a Java player who gets `Could not join the server: the server refused: it wants
an Xbox Live sign-in` (older versions: `the server refused: no reason given`) right after joining,
on every try, means this setting: put `online-mode=false` into `server.properties` and restart the
server. The console line `could not join the server` has the reason as `why=`. Leave `transport`
as it is: it is the Bedrock players' connection too.

## Bedrock transport (`[bedrock] transport`)

Each Java player is one loopback Bedrock connection to this server. BDS has two transports
(`server.properties` `transport=`):

* **`raknet`**: the bridge dials `127.0.0.1:<port>` over RakNet, as before.
* **`nethernet`**: BDS opens **no RakNet port at all**; it serves NetherNet's HTTP signaling on its
  TCP `server-port` and WebRTC over UDP. The bridge then dials `http://127.0.0.1:<port>` over
  NetherNet (`core/bridge/nethernet.go`). No Xbox account or token is needed: BDS requires *a*
  connection identity on every NetherNet peer (an offer without one is refused, signal error 37),
  but accepts a self-signed one (a fresh key per connection, the same key the Bedrock login is
  signed with). The bridge gathers loopback ICE candidates only, so the connection stays on the
  machine and the player's address is `127.0.0.1` (the online-identity hook's loopback check
  works unchanged).

`transport = "auto"` (the default, also when the key is missing) reads `server.properties` and
picks the right one; `"raknet"` / `"nethernet"` force it. On NetherNet, `server-ip` (if set to a
specific address) is used as the signaling host instead of `127.0.0.1`.

Keep the NetherNet `server-port` (TCP) and the UDP ports reachable on loopback; firewalling them
from outside is fine. Everything else (identity hook, `online-mode=false`, account modes) is the
same on both transports.

## Advanced options (`[core.options]`, all strings)

| Key | Default | Meaning |
|---|---|---|
| `namespace_uuid` | `a3e78ee7-823a-4cb5-9fe0-532b54ccc20d` | Namespace the stable identity is derived under. Must match the hook's `namespace_uuid` (the default already does). |
| `view_distance` | `10` | Java server-list / render distance. |
| `lang_file` | — | An `en_US.lang` to translate server texts. |
| `session_server` | Mojang | Base URL of the session server for premium verification (a custom auth proxy, else Mojang's). |
| `velocity_secret` / `velocity_secret_file` | — | Behind a Velocity proxy with modern forwarding: the shared `forwarding.secret` (inline or a file path). When set, Velocity verifies the Mojang account and forwards the profile; the Java listener trusts that instead of doing its own handshake, and must be reachable only by the proxy. |
| `pack_max_mb` | `100` | Packs bigger than this are not downloaded from the server. Packs in the server's own folders are read from there and have no limit. |
| `pack_cache_mb` | `512` | Size limit of the pack cache (`plugins/delta/packs`). |
| `form_icons` | `on` | `"off"`: the converted pack has no font of form button pictures and form buttons are their labels only. The font has a small picture of every texture of the server's packs that a form could name, which makes the pack some percent bigger (2.7 MB on a 25.5 MB pack of an add-on with 12000 such textures). Changing it converts the pack again (another pack hash). |
| `java_online` | follows `account_mode` | Overrides only the Java listener's Mojang check: `"true"` = every login (online), `"false"` or `"hybrid"` = premium names only (**hybrid**: `"false"` no longer lets anyone use a premium name), `"unverified"` = nobody (tests only; anyone can take any name). Leave unset in production. |
| `animations` | `on` | `"off"`: custom mobs are not animated, each is its one static model (fewer entities and packets for the client). |
| `anim_parts` | `24` | The most item displays an animated custom mob is drawn with: a number, or `"all"` for a display per bone that moves (bones that move least are merged into their parents to meet a number). Changing it converts the pack again (another pack hash). |
| `anim_full_mobs` | `0` | With `anim_parts` a number: this many of the nearest animated mobs are drawn in full detail (as `"all"`), the others merged to `anim_parts`. Needs a second set of part models in the pack. |
| `login_chunk_radius` | `2` | The chunk radius asked of the server while a player logs in (1 to 16). The server lets the player in once it has sent those chunks, and the rest follow; `16` is the old behaviour, about 3.5 s slower per join. |
| `hurt_flash_ticks` | `10` | How long a hurt custom mob stays red, in ticks (1 to 200). Java's own flash is 10; a long one is for checking the flash in a client. |
| `debug_anim` | — | Any value: log when a custom mob's part displays come and go and what its animation controllers play. |
| `identity_hook` | follows `account_mode` | `"true"`/`"false"`: derived XUID via the hook or not, independent of the Mojang check. Testing only. |

## Resource packs (`[packs]`)

```toml
[packs]
enabled = true
# listen = "0.0.0.0:8080"
# url = "http://play.example.com:8080"
```

The server's Bedrock packs are converted for Java players (see PACKS.md) and offered with the
normal Java prompt. A Java client downloads a pack over HTTP, so it needs an address it can reach:

| Setup | What to set |
|---|---|
| Players join the Java port directly (also through a port forward, an SRV record, or a TCP tunnel such as playit.gg) | Nothing. The Java port answers the download too, at the address the player joined with. |
| Players join through Velocity or another Java proxy | `listen`: a port of its own for the downloads, open to players. They use the host they joined with and that port. |
| That port is reached at another address (a reverse proxy, a CDN) | `url` as well: the public base URL. |

`enabled = false` turns the conversion off. When a player's client cannot download or load the pack,
the server log has `java client could not use the resource pack` with the address it was given.

## Namespace

`namespace_uuid` must be the same on the core and the hook (it keys the derived UUID/XUID and the
`SelfSignedId` consistency check). The default is baked into both; only change it if you run several
independent Delta networks that must not share derived ids.

## Bedrock players through a proxy

A Bedrock proxy in front of this server (Beacon or another relay) verifies its players with Xbox
Live and logs them in to BDS with a self-signed login that carries their real XUID, which an
`online-mode=false` BDS drops. Delta restores it, with the same bundled hook that gives Java
players theirs. Which proxies it believes, under `[bedrock]`:

| Setting | Default | What it does |
|---|---|---|
| `trusted_proxies` | `["local"]` when Java players get an identity (`online`/`hybrid`); off in `offline` | addresses whose logins keep their XUID; `"local"` = every address of this machine. `[]` turns it off |
| `forwarding_secret_file` / `forwarding_secret` | none | Beacon's `forwarding.secret`: Beacon's signed login decides instead of the address, so Beacon may run on another machine |
| `beacon_server_name` | any | with the secret: only claims for this server name (its name in `beacon.toml`) |

Address trust believes whoever connects from a listed address, so only use it when nothing but the
proxy can reach the server's port (firewall). With BDS on `transport=nethernet`, a proxy on the same
machine connects from the machine's public address, not 127.0.0.1: use `"local"`.

**xuidforward:** a server that used the xuidforward plugin needs no new settings. When
`config.toml` leaves these out, Delta reads `plugins/xuidforward/config.json` (allowed sources,
forwarding secret, server name), so the plugin can simply be deleted. Both may also stay installed:
Delta loads after xuidforward, which hooks BDS, and Java players then get their identity through
its hook.
