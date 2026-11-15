# Third-party code

Both are vendored under `core/third_party/` and wired in with `replace` directives in
`core/go.mod`. Only the packages the core links are kept; tests of the vendored code were removed.

| Directory | Module | What | License |
|---|---|---|---|
| `core/third_party/dragonfly` | `github.com/df-mc/dragonfly` (a fork with Java Edition support) | `server/java/protocol/*` (Java protocol: wire codec, v776/v777 packet ids and registries, login/configuration server, chunk codec, item components, text), `server/world/chunk`, `server/block/cube`, `server/textcolour` | MIT (`LICENSE`: Dragonfly; `server/java/LICENSE`: the Java protocol code) |
| `core/third_party/gophertunnel` | `github.com/sandertv/gophertunnel` (a fork with the 1.26.60 protocol) | `minecraft/...`: the Bedrock client (RakNet/TCP dial, login, packets) | MIT |

## Local changes

* `dragonfly/server/java/protocol/server/server.go`: `handle` recovers a panic during a Java
  login (logs, releases the pending slot, closes the connection) instead of ending the process.
* `gophertunnel/minecraft/dial.go`: `listenConn` recovers a panic while handling a packet (logs,
  cancels the dial / closes the connection).

Both are marked with a `javabridge:` comment.

## Updating

Copy the new versions of the same directories over these, keep the two patches above, run
`go mod tidy` and the tests in `core/`. When the Bedrock or Java protocol version changes, the
generated tables in `core/internal/{blocks,javamap,sounds,javapack}` and `bridge/recipesets_gen.go`
have to be regenerated as well (the generators are not part of this repository yet).

## Bundled native hook (online identity)

`plugin/src/endstone_javabridge/auth/libxuidforward.so` and `auth/profiles/*.json` are the
owner's **xuidforward** native shim and its BDS build profiles, bundled unchanged so javabridge
ships as one plugin. The shim patches BDS's login path to write a stable XUID for an accepted
self-signed (loopback) login (see docs/LOGIN.md). `auth/login_body.py` is vendored from the same
project (the Login-packet parser), with one addition: it also reads the `Nonce` claim that carries
javabridge's per-start trust secret. `auth/hook.py` is javabridge's own code (load, self-check,
install, and the loopback+secret trust gate). The bundled `.so` and profiles were checked for any
of the owner's network names/paths and contain none (the shim carries only generic strings).
