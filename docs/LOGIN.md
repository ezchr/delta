# Login and identity (docs/LOGIN.md)

How a Java player's loopback connection becomes a correctly identified player on BDS,
on servers with Xbox login on as well as off.

## The problem

BDS with `online-mode=true` rejects any login that is not Xbox-signed. A Java player has no
Xbox account, so the bridge's loopback Bedrock client must present a self-signed (offline) login,
which BDS would normally refuse. Even on `online-mode=false`, BDS gives a self-signed login an
empty XUID, so player storage, permissions and bans do not key on a stable id across sessions.

## The mechanism (reuses the owner's xuidforward)

xuidforward solves exactly this for Beacon-relayed Bedrock players: a self-signed login that
carries a claimed XUID is trusted (by loopback source and/or a signed claim) and that XUID is
written into BDS's login path before player storage is selected. The patch is in
`libxuidforward.so`, loaded into the BDS process with ctypes; the plugin reads the claimed XUID
out of the login packet (`PacketReceiveEvent`) and stages it (`xf_stage(name, xuid, self_id)`).

javabridge reuses this unchanged:

1. The Go core builds the Bedrock login for a Java player (`bridge.IdentityProvider`):
   - `OfflineIdentity` (default, for `online-mode=false` servers): name = Java name, Identity =
     uuid5("javabridge:" + Java UUID), XUID empty. Stable per player, no hook needed.
   - `OnlineIdentity` (for `online-mode=true` servers, or whenever a stable XUID is wanted): the
     same, plus a digit XUID derived from the Java account UUID (so the same Java player is the
     same BDS player every time and across both editions if the network links them later), and
     `SelfSignedID` set so xuidforward's `require_self_signed_id_match` passes.
2. The login carries a per-start trust secret (see below) in a client-data field the plugin reads.
3. The javabridge plugin bundles xuidforward's hook and, for each loopback login it recognises as
   its own (secret matches, source is loopback), stages the XUID. BDS then accepts the login and
   keys the player on that XUID.

## Trust: only the bridge's own loopback logins

A real Bedrock player connecting from outside must never get the bypass, and an outside client
must not be able to forge one. Trust rests on two things an outsider cannot both have:

- **Source is loopback.** The bridge's Bedrock client connects to `127.0.0.1:<bedrock port>`;
  the plugin stages only logins whose source address is loopback. (On a box where the relay runs
  too, both are loopback; the secret below separates them.)
- **A per-start secret.** On `jb_start` the core generates a random 32-byte secret and puts it in
  the login (a dedicated field). The plugin is told the secret through its own config call and
  stages only logins carrying it. The secret never leaves the process, so an outside client has no
  way to present it even from loopback.

A login missing the secret, or from a non-loopback source, is left untouched: BDS handles it as a
normal login (and refuses it on online-mode, as it should).

## Skins

Already done in the core (`bridge/skin.go`): the Java player's Mojang profile textures (64x64
skin, slim/wide) become a Bedrock skin with the right geometry, attached to the login's ClientData.
No work needed here.

## Versions and failure

The hook self-checks against BDS build profiles (`1.26.51`, `1.26.60` present). On a BDS build with
no matching profile, the hook refuses to install and the plugin disables *online* identity only
(offline-mode servers still work), with a clear log line - it never patches a build it does not
recognise, and never crashes BDS. A new BDS build needs a new profile (documented).

## What is the owner's call

The vendored Java protocol keeps `Copyright (c) 2026 ezchr`; the hardcoded
the hard-coded fallback shim path fallback path in xuidforward names a live
server and must be stripped from any copy that ships in javabridge.


## The player-list crash (owner, seen in play)

A Bedrock client can crash when it opens the pause-menu player list if another player's broadcast
entry carries a malformed XUID. javabridge players appear as ordinary Bedrock players to any real
Bedrock player on the same server, so their XUID must be well-formed, not an arbitrary number:

- The derived XUID is `2535` + 12 hash digits = a 16-digit decimal in the real-XUID key space
  (`OnlineIdentity`, `online_identity.go`). It is stable per Java account and a collision with a
  real Xbox account is vanishingly unlikely.
- **Must be verified in game:** with a javabridge player and a real Bedrock player on the same BDS,
  the Bedrock player opens the player list and does not crash. If BDS copies the login XUID into
  the broadcast entry and even a well-formed derived XUID still crashes some client version, the
  fallback is to broadcast these players with an empty XUID (like an offline player) while keeping
  the stable XUID only in BDS storage - which needs the native hook to separate the two. This is
  the top open item for the native-hook integration.
