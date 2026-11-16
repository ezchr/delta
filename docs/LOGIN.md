# Login and identity (docs/LOGIN.md)

How a Java player's loopback connection becomes a correctly identified, stably-keyed player on BDS.

## "online mode" here means the JAVA account mode, not BDS online-mode

Two unrelated settings share the name "online mode":

* **javabridge `account_mode`** (`plugins/javabridge/config.toml`) — premium Mojang verification of
  **Java** players. Default `online`; `hybrid` verifies only premium names; `offline` none. This is
  what "online" means throughout this document.
* **BDS `server.properties online-mode`** — Xbox verification of **Bedrock** logins. **Keep it
  `false`** (Endstone's default). See below.

javabridge verifies Java accounts itself and gives each a stable Bedrock XUID; BDS online-mode is
neither used nor wanted for the loopback logins (it would block them). See CONFIG.md.

## The problem

A Java player has no Xbox account, so the bridge's loopback Bedrock client presents a self-signed
(offline) Bedrock login. On a stock BDS such a login gets an **empty XUID**, so player storage,
permissions and bans do not key on a stable id across sessions, and the player shows up to real
Bedrock players with a malformed/empty id.

## The mechanism (reuses the owner's xuidforward)

The bundled native hook (`auth/libxuidforward.so`, the owner's xuidforward — see THIRD_PARTY.md)
patches `ServerNetworkHandler::_processValidatedLoginPacket`: for a login BDS has **already
accepted**, it rewrites the empty XUID to a staged one, before player storage is selected. So it
**upgrades an accepted self-signed login's XUID**; it does **not** make BDS accept a login it would
otherwise reject.

Therefore the BDS must run `online-mode=false` (which accepts self-signed logins). Then:

1. The Go core builds the Bedrock login for a Java player (`bridge.IdentityProvider`):
   * `OfflineIdentity` (for `account_mode=offline`): name = Java name, Identity = uuid5 of the Java
     UUID, XUID empty. No hook needed.
   * `OnlineIdentity` (for `account_mode=online`): the same, plus a **digit XUID** derived from the
     VERIFIED Java account UUID (`2535` + 12 hash digits = a well-formed 16-digit XUID in the
     real-XUID key space), the `SelfSignedId` the hook checks, and the per-start trust secret.
2. The plugin's hook (`auth/hook.py`) sees the loopback login in a `PacketReceiveEvent`, and only
   when it is BOTH from a loopback source AND carries the per-start secret, stages
   `xf_stage(xname, xuid, selfSignedId)`. BDS then keys the player on that stable XUID.

### Where each field rides on the login

The core's offline Bedrock login is gophertunnel's `EncodeOffline` output: a `Token` JWT with
`AuthenticationType: 2` carrying `xid` (the XUID) and `xname` (the name), plus a client-data JWT
carrying `SelfSignedId` and `Nonce`. The plugin parses these (`auth/login_body.py`):

| Value | Where it rides | Set by |
|---|---|---|
| derived XUID | `Token` JWT `xid` | `OnlineIdentity.IdentityData.XUID` |
| name | `Token` JWT `xname` | `OnlineIdentity.IdentityData.DisplayName` |
| `SelfSignedId` = uuid5(namespace, xuid) | client-data JWT `SelfSignedId` | `remote.go` copies `BedrockLogin.selfSignedID` into `ClientData.SelfSignedID` |
| per-start trust secret | client-data JWT `Nonce` | `remote.go` copies `BedrockLogin.trustSecret` into `ClientData.Nonce` |

Two core details that make this work (both only on the online path, never for offline logins):

* **`Dialer.KeepXBLIdentityData = true`** — gophertunnel's dialer clears XBL identity data
  (including the XUID) before `EncodeOffline` unless this is set, which would wipe the derived XUID.
* **`ClientData.SelfSignedID = uuid5(namespace, xuid)`** — the hook's `require_self_signed_id_match`
  checks `SelfSignedId == uuid5(namespace_uuid, XUID)`; the stock random per-device SelfSignedID
  would fail it.

## Trust: only the bridge's own loopback logins

A real Bedrock player from outside must never get the XUID bypass, and an outside client must not be
able to forge one. The plugin stages a login only when BOTH hold (an outsider cannot have both):

* **Source is loopback.** The bridge connects to `127.0.0.1`; the plugin checks the login's source
  address is loopback (IPv4 `127.0.0.0/8` or IPv6 `::1`).
* **The per-start secret.** On each `jb_start` the core generates a random 32-byte secret
  (`bridge.NewSecret`) and puts it in the login's `Nonce`. The plugin reads the same secret through
  `jb_call("identity_secret")` and compares it in **constant time** (`hmac.compare_digest`). The
  secret never leaves the process and is regenerated on every reload; the plugin re-reads it after a
  reload, and never logs it.

A login missing either is left untouched (the plugin clears any stale staged entry it could have
picked up), so BDS handles it as an ordinary self-signed login — with an empty XUID, as it should.
The native hook additionally enforces `SelfSignedId == uuid5(namespace, xuid)` and that the XUID is
all digits.

## Premium (online) Java-account verification

In `account_mode=online` the Java listener runs the real Mojang path: the encryption handshake and
the session-server `hasJoined` check (`third_party/.../server/{auth,server}.go`). On success the
**verified profile** from the session-server response (its `id` and `name`) replaces the name/UUID
the client sent in its handshake, and that verified UUID/name is what `OnlineIdentity` derives the
stable XUID and BDS UUID from. So a player cannot claim another player's Java name: an unverified
login is refused at the Java login, and the identity toward BDS is always the verified one. Behind a
Velocity proxy with modern forwarding (`velocity_secret`), Velocity does the Mojang verification and
forwards the signed profile instead, which the Java listener trusts.

## Hybrid: premium names verified, other names cracked

`account_mode=hybrid` (`core/service/premium_names.go`, `account_mode.go`) sets the listener's
`OnlineModeFor` to a Mojang name lookup instead of `OnlineMode`:

1. The client's login hello carries a name. The core asks Mojang's profile lookup
   (`api.minecraftservices.com/minecraft/profile/lookup/name/<name>`, then
   `api.mojang.com/users/profiles/minecraft/<name>`): 200 with a profile = premium, 204/404 = no
   such account; anything else is a failure. Answers are cached (premium 24 h, others 1 h,
   `premium_cache.json` in the plugin folder).
2. **Premium name:** the listener runs the online path above (encryption + `hasJoined`), and the
   verified profile replaces the hello's name/UUID. `OnlineIdentity` then derives the XUID from
   the verified UUID, so the player has the same BDS identity as with `account_mode=online`
   (Ezchr: `2535483566072468`, not the offline-UUID one `2535905407707780`). A cracked client
   cannot answer the encryption request with a Mojang session and is not admitted; one that sends
   a key without a session is refused with a text that says the name belongs to a premium account.
   Name case does not matter (the lookup is case-insensitive), so `ezchr` is protected too.
3. **Other name:** admitted offline with `OfflineUUID(name)`; `OnlineIdentity` derives a stable XUID
   from that UUID. Offline UUIDs are version 3 and premium UUIDs version 4, so the two never give
   the same XUID.
4. **Lookup failure** (network, 5xx, 429, our own 120-per-minute limit): the last cached answer is
   used even if expired; with none the name is treated as premium (fail closed). A premium player
   is never turned into an offline identity by an outage; the cost is that an unknown cracked name
   cannot join until the lookup works again. Logged as `hybrid login: premium lookup failed ...`.

With Velocity modern forwarding the proxy does all of this (HybridAuth) and the listener trusts the
forwarded profile, in every account mode.

## Versions and failure (fail-safe)

The hook self-checks against bundled BDS build profiles (`auth/profiles/*.json`: `1.26.51`,
`1.26.60`). On a BDS build with no matching profile, the hook refuses to install and **online
identity is disabled** (a clear log line), while **offline identity still works** — it never patches
a build it does not recognise and never crashes BDS. A new BDS build needs a new profile.

## Skins

Done in the core (`bridge/skin.go`): the Java player's Mojang textures become a Bedrock skin on the
login. No work here.

## The player-list crash (owner's concern) — tested, no crash

A Bedrock client can crash when it opens the pause-menu player list if another player's broadcast
entry carries a malformed XUID. javabridge players appear as ordinary Bedrock players, so their XUID
must be well-formed. The derived XUID (`2535` + 12 hash digits) is a well-formed 16-digit decimal in
the real-XUID key space.

**Verified in game (2026-11-15):** with a javabridge Java player and a real Xbox-authenticated
Bedrock client on the same `online-mode=false` BDS, teleported together, the Bedrock client
rendered the javabridge player's full model and processed its broadcast player-list entry (derived
XUID) with **zero decode errors and no crash**. The empty-XUID broadcast fallback described in
earlier drafts is therefore **not needed**. (The real Bedrock client used was Cinnabar, which has no
vanilla pause-menu player-list screen to click; the crash mechanism — decoding and rendering the
broadcast entry — was fully exercised. A vanilla retail client opening the roster is a trivial
remaining manual confirmation.)

## Manual confirmations still open

1. **Vanilla Bedrock roster screen:** join a retail Bedrock client to an `online-mode=false` test
   BDS alongside a javabridge Java player and open the pause-menu player list. Expected: no crash
   (the entry already decodes and renders cleanly on a real client).
2. **Real premium Java playthrough via Velocity:** premium auth + Velocity modern forwarding to
   javabridge is **proven** (a real Ezchr Mojang account authenticated through Velocity and the
   backend connected). What is **not** working is a **1.21.x client fully playing a 26.3 javabridge
   through ViaVersion** — the 1.21→26.3 registry/config translation drops the client with a
   "Network Protocol Error" after the backend connects. This is a client-version-breadth matter, not
   a login matter. Fix options (future work): javabridge natively supporting 1.21.x clients, or a
   closer Via path. A native 26.2/26.3 Java client joins directly without this.
