# Third-party code

The Java protocol is [go-mcjava](https://github.com/ezchr/go-mcjava) (MIT), a normal module
dependency in `core/go.mod`.

Two more are vendored under `core/third_party/` and wired in with `replace` directives in
`core/go.mod`. Only the packages the core links are kept; tests of the vendored code were removed.

| Directory | Module | What | License |
|---|---|---|---|
| `core/third_party/dragonfly` | `github.com/df-mc/dragonfly` | `server/world/chunk`, `server/block/cube`, `server/textcolour` | MIT |
| `core/third_party/gophertunnel` | `github.com/sandertv/gophertunnel` (a fork with the 1.26.60 protocol) | `minecraft/...`: the Bedrock client (RakNet/TCP dial, login, packets) | MIT |

## Local changes

* `gophertunnel/minecraft/dial.go`: `listenConn` recovers a panic while handling a packet (logs,
  cancels the dial / closes the connection).
* `gophertunnel/minecraft/{network,dial}.go`: a `keyDialer` interface; `DialContextNetwork` hands
  the login's private key to a network that implements it (when there is no authorization-service
  token). The bridge's loopback NetherNet transport (`core/bridge/nethernet.go`) needs it: a BDS
  with `transport=nethernet` closes the connection when the NetherNet identity key is not the key
  the Login is signed with.

All are marked with a `Delta:` comment.

## Updating

Copy the new versions of the same directories over these, keep the two patches above, run
`go mod tidy` and the tests in `core/`. When the Bedrock or Java protocol version changes, the
generated tables in `core/internal/{blocks,javamap,sounds,javapack}` and `bridge/recipesets_gen.go`
have to be regenerated as well (the generators are not part of this repository yet).

## Bundled native hook (online identity)

`plugin/src/endstone_delta/auth/libxuidforward.so` and `auth/profiles/*.json` are the
owner's **xuidforward** native shim and its BDS build profiles, bundled unchanged so Delta
ships as one plugin. The shim patches BDS's login path to write a stable XUID for an accepted
self-signed (loopback) login (see docs/LOGIN.md). `auth/login_body.py` is vendored from the same
project (the Login-packet parser), with one addition: it also reads the `Nonce` claim that carries
Delta's per-start trust secret. `auth/beacon_claim.py` is vendored unchanged from xuidforward too (checking Beacon's signed login).
`auth/hook.py` is Delta's own code (load, self-check,
install, and the loopback+secret trust gate). The bundled `.so` and profiles were checked for any
of the owner's network names/paths and contain none (the shim carries only generic strings).
