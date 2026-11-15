# javabridge

Lets Java Edition players join a Bedrock Dedicated Server that runs
[Endstone](https://github.com/EndstoneMC/endstone), as one Endstone plugin: drop the wheel into
`plugins/` and start the server. Java players appear on the server as ordinary players, so
commands, permissions and other plugins work on them.

Status: early. Java Edition 26.3 and 26.2 clients, offline logins. See `docs/STATUS.md`.

* `docs/ARCHITECTURE.md` - how it works and where things are
* `docs/NATIVE.md` - the native library's C interface
* `docs/STATUS.md` - what works, how to build, install and test
* `docs/THIRD_PARTY.md`, `docs/ORIGIN.md` - vendored code and where the core came from

Build (Linux amd64, Go >= 1.26, Python 3): `./build.sh` -> `dist/endstone_javabridge-*.whl`.
