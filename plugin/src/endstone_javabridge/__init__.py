"""javabridge: lets Java Edition players join this Bedrock server.

Minimal plugin shell (core loader). The full plugin (configuration, identity, commands) lives
alongside it; this module starts and stops the native core, forwards its logs, and (for a server
with Xbox login on, or whenever a stable XUID is wanted) installs the online-identity hook so a
Java player's loopback login is accepted and keyed on a stable XUID.
"""

from __future__ import annotations

import json
import tomllib
import traceback
from pathlib import Path

from endstone.plugin import Plugin

from . import _native
from .auth.hook import IdentityHook

DEFAULT_CONFIG = """\
# javabridge configuration

[java]
# Address Java players connect to.
listen = "0.0.0.0:25565"
motd = "A Bedrock server, open to Java Edition"
# Account mode for Java players:
#   "online"  - premium (DEFAULT): every Java login is verified with Mojang (encryption +
#               session-server check). Names cannot be spoofed; each account gets a stable
#               identity on this server. Works whether this BDS has online-mode true or false.
#   "offline" - no verification: anyone may join under any name (cracked/private servers only).
#               A clear warning is logged; staff names can be impersonated.
account_mode = "online"

[bedrock]
# This server's own Bedrock port over loopback. 0 = the port from server.properties.
port = 0

[core]
# "bridge" (normal) or "spike" (native library self-test).
mode = "bridge"
debug = false

# [core.options]
# Advanced, rarely needed. namespace_uuid keys the derived stable identity; it must match the
# online-identity hook's namespace (leave unset to use the default).
# namespace_uuid = "a3e78ee7-823a-4cb5-9fe0-532b54ccc20d"
"""

_LEVELS = {"DEBUG": "debug", "INFO": "info", "WARNING": "warning", "ERROR": "error"}


class JavaBridgePlugin(Plugin):
    api_version = "0.11"
    name = "javabridge"
    version = "0.2.0"
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
        self._hook: IdentityHook | None = None

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
        options = {str(k): str(v) for k, v in core.get("options", {}).items()}
        # The Java account mode drives the core's online_mode and identity provider. Kept as an
        # option so one config knob ("account_mode") controls both without changing the native
        # config schema. Default is premium (online).
        account_mode = str(java.get("account_mode", "online")).strip().lower() or "online"
        options.setdefault("account_mode", account_mode)
        return {
            "mode": core.get("mode", "bridge"),
            "debug": bool(core.get("debug", False)),
            "listen": java.get("listen", "0.0.0.0:25565"),
            "motd": java.get("motd", ""),
            "max_players": int(self.server.max_players),
            "bedrock": f"127.0.0.1:{port}",
            "data_dir": str(folder),
            "options": options,
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
        self._setup_identity()

    def _bds_online_mode(self) -> bool:
        """Read BDS's server.properties online-mode (default false in Endstone). Best-effort."""
        try:
            # server.properties sits next to the server binary; the plugin runs with the
            # server folder as the working directory.
            for base in (Path.cwd(), Path(self.data_folder).resolve().parents[2]):
                props = base / "server.properties"
                if props.exists():
                    for line in props.read_text(encoding="utf-8", errors="replace").splitlines():
                        line = line.strip()
                        if line.startswith("online-mode="):
                            return line.split("=", 1)[1].strip().lower() == "true"
                    break
        except Exception:  # noqa: BLE001
            pass
        return False

    def _setup_identity(self) -> None:
        # The core hands out the per-start trust secret (online/premium mode only). An empty
        # secret means offline mode - no hook needed, logins stay untouched. Re-read on every
        # enable so a /reload picks up the freshly generated secret.
        secret, namespace = "", ""
        try:
            info = self._native.call("identity_secret")
            secret = str((info or {}).get("secret", "") or "")
            namespace = str((info or {}).get("namespace", "") or "")
        except Exception as e:  # noqa: BLE001
            self.logger.warning(f"online identity: could not read the trust secret ({e}); online identity off")
            return
        if not secret:
            self.logger.info("account mode is offline: online-identity hook not installed (logins untouched)")
            return
        hook = IdentityHook(self.logger, Path(__file__).parent, namespace)
        if not hook.load():
            self.logger.warning("online identity: native hook did not load; Java logins will NOT get a stable XUID "
                                "and online-mode BDS will refuse them. Use account_mode=offline or add a profile.")
            return
        hook.set_secret(secret)
        if self._bds_online_mode():
            self.logger.warning(
                "BDS server.properties has online-mode=true. javabridge Java players log in to BDS "
                "over loopback with a self-signed login, which online-mode=true REFUSES - so they "
                "cannot join. Set online-mode=false: javabridge verifies Java accounts itself "
                "(account_mode=online, premium Mojang) and the hook gives each a stable XUID, so "
                "BDS online-mode adds nothing and only blocks Java players."
            )
        self.register_events(hook)
        if hook.install():
            self._hook = hook
        else:
            # install() already logged why; a failed install is safe (BDS stays stock).
            self._hook = hook  # keep it registered so set_secret/uninstall stay consistent

    def on_disable(self) -> None:
        if self._task is not None:
            self._task.cancel()
            self._task = None
        if self._hook is not None:
            try:
                self._hook.uninstall()
            except Exception as e:  # noqa: BLE001
                self.logger.warning(f"online identity: hook not cleanly removed: {e}")
            self._hook = None
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
            # Never echo the trust secret to a command sender.
            if method == "identity_secret" and isinstance(result, dict):
                result = {"secret": "<hidden>", "namespace": result.get("namespace", "")}
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
                self.logger.info("[debug] " + msg)
                continue
            getattr(self.logger, _LEVELS.get(level, "info"))(msg)
