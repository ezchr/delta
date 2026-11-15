# Native interface

The core is a Go library built with `go build -buildmode=c-shared` (`core/cmd/libjavabridge`).
The plugin loads it with `ctypes` (`plugin/src/endstone_javabridge/_native.py`, class `Native`).
The host always calls in; the library never calls back into Python. Logs are polled.

| C function | Python (`Native`) | Meaning |
|---|---|---|
| `char* jb_version(void)` | `version()` | Library version (`-X main.version`). |
| `int jb_start(const char* config_json)` | `start(dict)` | Start the core. 0 ok, -1 error (see `jb_last_error`), -2 already running. |
| `int jb_stop(int timeout_ms)` | `stop(ms)` | Stop everything `jb_start` started; blocks at most `timeout_ms`. 0 ok, -1 timeout/error. |
| `int jb_running(void)` | `running()` | 1 while the core runs. |
| `char* jb_poll_log(void)` | `poll_log() -> [(level, msg)]` | Queued log lines, `LEVEL\tmessage\n` each (`\n` inside a message escaped as `\n`), or NULL. Levels: DEBUG, INFO, WARNING, ERROR. The queue holds 4096 lines; older lines are dropped with a warning when the host does not poll. |
| `char* jb_call(const char* request_json)` | `call(method, params)` | Request/response for everything else: `{"method": "...", "params": ...}` -> `{"ok": true, "result": ...}` or `{"ok": false, "error": "..."}`. |
| `char* jb_last_error(void)` | `last_error()` | Last start/stop error, cleared on read. |
| `void jb_free(char*)` | (internal) | Every `char*` returned above must be freed with this. |

## Configuration (`jb_start`)

JSON object, unknown keys ignored (`core/service/service.go`, `Config`):

| Key | Default | Meaning |
|---|---|---|
| `mode` | `"bridge"` | `"bridge"`, or `"spike"` (self-test: TCP echo listener and the `stress` call). |
| `listen` | `"0.0.0.0:25565"` | Java listener address. |
| `bedrock` | `"127.0.0.1:19132"` | The server's own Bedrock address over loopback: `host:port` (RakNet), `tcp:host:port`, or `nethernet:host:port` for a BDS with `transport=nethernet` (the plugin picks this from server.properties; see CONFIG.md). |
| `data_dir` | `""` | Directory the core may write to (caches). |
| `debug` | `false` | Debug log lines. |
| `motd` | `""` | Java server list text. |
| `max_players` | `20` | Shown in the Java server list. |
| `options` | `{}` | String map passed to the bridge. Keys include `account_mode`/`online_mode` (premium vs offline Java auth), `namespace_uuid`, `session_server`, `velocity_secret`/`velocity_secret_file` (Velocity modern forwarding), `view_distance`, `lang_file`, `packs`; see docs/CONFIG.md. |

## Calls (`jb_call`)

| Method | Params | Result |
|---|---|---|
| `debug` | bool | Turns debug lines on/off (works while stopped). |
| `players` | - | Bridge mode: the connected Java players (`name`, `uuid`, `protocol`, `address`, `connected`). |
| `status` | - | Bridge mode: `online`, `listen`. |
| `goroutines` | - | Goroutine count (works while stopped: leak check; 3 when idle). |
| `stacks` | - | All goroutine stacks (debugging). |
| `stress` | seconds | Spike mode: preemption/GC/panic/SIGSEGV/EPIPE self-test. |
| `goroutines` | - | Spike mode: goroutine count. |
| `identity_secret` | - | Bridge mode: the per-start trust secret and namespace for the online-identity hook (the plugin reads it on enable; empty in offline mode). Returned only through this in-process call; never logged. |

## Rules for the host

* Load the library once per process and keep it (the Go runtime cannot be unloaded; a second
  copy of the library would start a second Go runtime). `_native.load` keeps the handle on the
  `sys` module, which survives Endstone's `/reload`, and loads from a content-addressed copy in
  the plugin's data folder. A wheel replaced while the server runs keeps using the old library
  until the server restarts (the plugin warns).
* `jb_start`/`jb_stop` can be called any number of times in turn.
* All calls are thread safe. ctypes releases the GIL during calls; `jb_stop` may block up to
  `timeout_ms` (the plugin uses 5000).
