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
from .auth.beacon_claim import ClaimVerifier, read_secret
from .auth.hook import IdentityHook
from .auth.trust_config import read_xuidforward_config, trust_settings

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
#   "hybrid"  - premium + cracked: names of premium Mojang accounts must log in with that
#               account (they keep their online identity); other names join offline (cracked).
#               If the name lookup fails and the name is not cached, it is treated as premium.
#   "offline" - no verification: anyone may join under any name (cracked/private servers only).
#               A clear warning is logged; staff names can be impersonated.
account_mode = "online"

[bedrock]
# This server's own Bedrock port over loopback. 0 = the port from server.properties.
port = 0
# How the bridge reaches this server over loopback: "auto" (default) follows server.properties
# `transport` (RakNet, or NetherNet for transport=nethernet); "raknet" or "nethernet" force one.
transport = "auto"
# Bedrock players coming through a proxy (Beacon, WaterdogPE) keep their real Xbox XUID: the
# xuidforward plugin is built in. Had xuidforward? Nothing to change: its config.json is read, and
# both may stay installed. Trusted by address ("local" = any address of this machine, the default):
# trusted_proxies = ["local"]
# Or by Beacon's signature, for a Beacon on another machine (Beacon's forwarding.secret):
# forwarding_secret_file = "forwarding.secret"

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
    # Load after the xuidforward plugin when both are installed: it hooks BDS first, and this one
    # uses that hook for Java players instead of trying to patch BDS a second time.
    soft_depend = ["xuidforward"]
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
        target = self._bedrock_target(str(bedrock.get("transport", "auto")), port)
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
            "bedrock": target,
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

    def _server_properties(self) -> dict:
        """BDS's server.properties as a dict (empty when unreadable). Best-effort."""
        out: dict = {}
        try:
            # server.properties sits next to the server binary; the plugin runs with the
            # server folder as the working directory.
            for base in (Path.cwd(), Path(self.data_folder).resolve().parents[2]):
                props = base / "server.properties"
                if props.exists():
                    for line in props.read_text(encoding="utf-8", errors="replace").splitlines():
                        line = line.strip()
                        if line and not line.startswith("#") and "=" in line:
                            k, v = line.split("=", 1)
                            out[k.strip()] = v.strip()
                    break
        except Exception:  # noqa: BLE001
            pass
        return out

    def _bds_online_mode(self) -> bool:
        """Read BDS's server.properties online-mode (default false in Endstone). Best-effort."""
        return self._server_properties().get("online-mode", "").lower() == "true"

    def _trust(self) -> dict:
        """Who may forward Bedrock players' XUIDs ([bedrock] in config.toml, then an old
        xuidforward config.json); see auth/trust_config.py."""
        try:
            raw = tomllib.loads((Path(self.data_folder) / "config.toml").read_text(encoding="utf-8"))
        except Exception:  # noqa: BLE001
            raw = {}
        account = str(raw.get("java", {}).get("account_mode", "online")).strip().lower()
        xf = read_xuidforward_config(Path(self.data_folder).parent)
        return trust_settings(raw.get("bedrock", {}), xf, java_identity=account in ("online", "hybrid", ""))

    def _xuidforward_plugin(self):
        """The xuidforward plugin when it is installed too, else None."""
        try:
            return self.server.plugin_manager.get_plugin("xuidforward")
        except Exception:  # noqa: BLE001
            return None

    def _bedrock_target(self, transport: str, port: int) -> str:
        """The core's Bedrock target for this server: "127.0.0.1:<port>" (RakNet), or
        "nethernet:<host>:<port>" when this BDS runs transport=nethernet (it then has no RakNet
        port; the core dials its NetherNet signaling on the TCP server-port over loopback)."""
        props = self._server_properties()
        transport = transport.strip().lower() or "auto"
        if transport not in ("auto", "raknet", "nethernet"):
            self.logger.warning(f'[bedrock] transport = "{transport}" is not auto/raknet/nethernet; using auto')
            transport = "auto"
        if transport == "auto":
            transport = "nethernet" if props.get("transport", "").lower() == "nethernet" else "raknet"
        if transport != "nethernet":
            return f"127.0.0.1:{port}"
        # server-ip binds NetherNet's signaling server (it is ignored for RakNet). Unset or a
        # wildcard: loopback. A specific local address: dial that one (still this machine).
        host = props.get("server-ip", "").strip().strip("[]")
        if host in ("", "0.0.0.0", "::"):
            host = "127.0.0.1"
        if ":" in host:
            host = f"[{host}]"
        self.logger.info("this server runs transport=nethernet: Java players are connected to it over "
                         "loopback NetherNet (no RakNet port needed, no Xbox account needed)")
        return f"nethernet:{host}:{port}"

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
        trust = self._trust()
        proxies = trust["sources"]
        claims = None
        try:
            key = read_secret(trust["secret"], trust["secret_file"])
        except Exception as e:  # noqa: BLE001
            self.logger.error(f"could not read forwarding_secret_file: {e} - trusting proxies by address only")
            key = b""
        if key:
            claims = ClaimVerifier(key, trust["server_name"], trust["window"])
        if not secret and not proxies and claims is None:
            self.logger.info("account mode is offline: online-identity hook not installed (logins untouched)")
            return
        if not namespace:
            namespace = "a3e78ee7-823a-4cb5-9fe0-532b54ccc20d"  # the relay's and xuidforward's namespace
        hook = IdentityHook(self.logger, Path(__file__).parent, namespace)
        hook.set_proxies(proxies)
        hook.set_claims(claims)
        if claims is not None:
            self.logger.info(f"Bedrock players relayed by Beacon keep their Xbox XUID (Beacon's signed claim; settings from {trust['origin']})")
        elif proxies:
            self.logger.info(f"Bedrock players relayed from {', '.join(proxies)} keep their Xbox XUID (settings from {trust['origin']})")
        xf = self._xuidforward_plugin()
        if xf is not None and getattr(xf, "_hook_installed", False) and getattr(xf, "_shim", None) is not None:
            # Both installed and xuidforward hooked BDS first: only one hook can patch BDS, so
            # Java players get their identity through xuidforward's, and it keeps handling
            # relayed players by its own config.
            hook.borrow(xf._shim)
            hook.set_secret(secret)
            self.register_events(hook)
            self._hook = hook
            self.logger.info("online identity: the xuidforward plugin is installed too and hooked BDS first: it keeps "
                             "handling relayed Bedrock players, Java players use its hook. Both work; you can remove "
                             "xuidforward (this plugin reads its config.json).")
            return
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
            if xf is not None:
                self.logger.info("online identity: the xuidforward plugin is installed too: this plugin does its job "
                                 "now (with its config.json); you can remove xuidforward.")
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
