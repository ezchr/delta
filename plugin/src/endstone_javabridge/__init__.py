"""javabridge: lets Java Edition players join this Bedrock server.

Minimal plugin shell (core loader). The full plugin (configuration, identity, commands) lives
alongside it; this module only has to start and stop the native core and forward its logs.
"""

from __future__ import annotations

import json
import tomllib
import traceback
from pathlib import Path

from endstone.plugin import Plugin

from . import _native

DEFAULT_CONFIG = """\
# javabridge configuration
[java]
# Address Java players connect to.
listen = "0.0.0.0:25565"
motd = "A Bedrock server, open to Java Edition"

[bedrock]
# This server's own Bedrock port over loopback. 0 = the port from server.properties.
port = 0

[core]
# "bridge" (normal) or "spike" (native library self-test).
mode = "bridge"
debug = false
"""

_LEVELS = {"DEBUG": "debug", "INFO": "info", "WARNING": "warning", "ERROR": "error"}


class JavaBridgePlugin(Plugin):
    api_version = "0.11"
    name = "javabridge"
    version = "0.1.0"
    description = "Java Edition players on a Bedrock server"
    commands = {
        "jbcore": {
            "description": "Send a request to the javabridge core (debugging).",
            "usages": ["/jbcore <method: message>"],
            "permissions": ["javabridge.command.core"],
        }
    }
    permissions = {
        "javabridge.command.core": {"description": "Use /jbcore", "default": "op"},
    }

    def __init__(self) -> None:
        super().__init__()
        self._native: _native.Native | None = None
        self._task = None

    def _core_config(self) -> dict:
        folder = Path(self.data_folder)
        folder.mkdir(parents=True, exist_ok=True)
        path = folder / "config.toml"
        if not path.exists():
            path.write_text(DEFAULT_CONFIG, encoding="utf-8")
        with path.open("rb") as f:
            raw = tomllib.load(f)
        java, bedrock, core = raw.get("java", {}), raw.get("bedrock", {}), raw.get("core", {})
        port = int(bedrock.get("port", 0)) or int(self.server.port)
        return {
            "mode": core.get("mode", "bridge"),
            "debug": bool(core.get("debug", False)),
            "listen": java.get("listen", "0.0.0.0:25565"),
            "motd": java.get("motd", ""),
            "max_players": int(self.server.max_players),
            "bedrock": f"127.0.0.1:{port}",
            "data_dir": str(folder),
            "options": {str(k): str(v) for k, v in core.get("options", {}).items()},
        }

    def on_enable(self) -> None:
        try:
            self._native = _native.load(self.data_folder)
        except Exception as e:  # noqa: BLE001 - report any load failure, keep the server up
            self.logger.error(f"could not load the native core: {e}")
            return
        if getattr(self._native, "stale", False):
            self.logger.warning("the plugin was updated while the server ran: restart the server to use the new core")
        try:
            cfg = self._core_config()
            self._native.start(cfg)
        except Exception as e:  # noqa: BLE001
            self.logger.error(f"could not start the core: {e}" + chr(10) + traceback.format_exc())
            self._drain()
            return
        self.logger.info(f"core {self._native.version()} started (Java on {cfg['listen']}, Bedrock {cfg['bedrock']})")
        self._task = self.server.scheduler.run_task(self, self._drain, delay=1, period=1)

    def on_disable(self) -> None:
        if self._task is not None:
            self._task.cancel()
            self._task = None
        if self._native is not None:
            try:
                self._native.stop(5000)
            except Exception as e:  # noqa: BLE001
                self.logger.error(f"core did not stop cleanly: {e}")
            self._drain()

    def on_command(self, sender, command, args) -> bool:
        if command.name != "jbcore" or self._native is None:
            return False
        method, _, params = " ".join(args).partition(" ")
        try:
            result = self._native.call(method, json.loads(params) if params else None)
            sender.send_message(f"{method}: {json.dumps(result)}")
        except Exception as e:  # noqa: BLE001
            sender.send_error_message(f"{method}: {e}")
        self._drain()
        return True

    def _drain(self) -> None:
        if self._native is None:
            return
        for level, msg in self._native.poll_log():
            if level == "DEBUG":
                # Endstone's plugin loggers drop debug records by default: the core's debug
                # lines only come when debug is on in the core, so show them as info.
                self.logger.info("[debug] " + msg)
                continue
            getattr(self.logger, _LEVELS.get(level, "info"))(msg)
