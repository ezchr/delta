# Configuration (docs/CONFIG.md)

javabridge is one Endstone plugin. On first start it writes `plugins/javabridge/config.toml`:

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
    `hasJoined`). The **verified** profile is used, so the owner gets exactly the identity he has
    in `online` mode: same Java UUID, same derived XUID, same player data, permissions and skin. A
    cracked client using a premium name cannot do that login and is refused (a client that tries
    and fails gets "`<name>` is a premium Minecraft account. Log in with that account to use this
    name; cracked players must choose another name.").
  * **any other name** is let in offline as a cracked player: the offline UUID
    (`OfflinePlayer:<name>`) and a derived XUID from it, so cracked players also keep their data
    between sessions. Anyone can use a free name — cracked servers need their own password login
    if that matters.

  Answers are cached in `plugins/javabridge/premium_cache.json` (premium names 24 h, other names
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

| Setting | Where | What it does for javabridge |
|---|---|---|
| **javabridge `account_mode`** | `plugins/javabridge/config.toml` | Premium Mojang verification of **Java** players. Keep it `online`. |
| **BDS `online-mode`** | `server.properties` | Xbox verification of **Bedrock** logins. **Keep it `false`.** |

**Set BDS `online-mode=false`** (Endstone's default). javabridge's Java players reach BDS over
loopback with a self-signed Bedrock login; BDS `online-mode=true` REFUSES self-signed logins during
validation, so Java players could not join at all. javabridge does the account verification itself
(premium Mojang, on the Java side) and the bundled hook gives each Java player a stable XUID — so
BDS `online-mode` adds nothing and only blocks Java players. If you leave BDS `online-mode=true`
with the hook enabled, the plugin logs a warning telling you to set it to `false`.

(Real Bedrock players are unaffected either way: they still log in with their own Xbox identity.)

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
| `packs` | `off` | Bedrock→Java resource-pack conversion (see PACKS.md). |
| `java_online` | follows `account_mode` | Overrides only the Java listener's Mojang check: `"true"` = every login (online), `"false"` or `"hybrid"` = premium names only (**hybrid**: `"false"` no longer lets anyone use a premium name), `"unverified"` = nobody (tests only; anyone can take any name). Leave unset in production. |
| `identity_hook` | follows `account_mode` | `"true"`/`"false"`: derived XUID via the hook or not, independent of the Mojang check. Testing only. |

## Namespace

`namespace_uuid` must be the same on the core and the hook (it keys the derived UUID/XUID and the
`SelfSignedId` consistency check). The default is baked into both; only change it if you run several
independent javabridge networks that must not share derived ids.

## Bedrock players through a proxy: `[bedrock] trusted_proxies`

A Bedrock proxy in front of this server (Beacon or another relay) verifies its players with Xbox
Live and logs them in to BDS with a self-signed login that carries their real XUID, which an
`online-mode=false` BDS drops. List the addresses the proxy connects from and javabridge restores
that XUID, with the same bundled hook that gives Java players theirs:

```toml
[bedrock]
trusted_proxies = ["127.0.0.1"]   # "local" = any address of this machine
```

This is what the separate xuidforward plugin does; do not install that plugin as well (both would
patch the same BDS function). Whoever connects from a listed address is believed, so only use it
when nothing but the proxy can reach the server's port (firewall). Empty (the default) is off.
Tested 2026-11-15: a Bedrock client through a relay on loopback joined with its Xbox XUID
(`staged [...] (relayed Bedrock player)` -> `Player connected: ..., xuid: <real>`), a Java player
on the same server keeps the derived one.
