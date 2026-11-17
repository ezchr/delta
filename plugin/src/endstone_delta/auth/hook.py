"""delta online identity: the native login hook (the owner's xuidforward).

On a BDS with online-mode=true a self-signed (offline) login is normally refused, and even on
online-mode=false it is given an empty XUID, so player storage does not key on a stable id. The
bridge's loopback Bedrock client logs in self-signed. To give a Java player a stable identity we
reuse the owner's xuidforward native shim (docs/THIRD_PARTY.md): it patches BDS's login path so a
staged XUID is written in before player storage is selected.

Trust (two independent facts an outsider cannot both have):
  1. The login arrives from a LOOPBACK source (the bridge connects to 127.0.0.1).
  2. The login carries the per-start trust SECRET (the Go core put it in login.ClientData.Nonce;
     the core generated it with bridge.NewSecret and the plugin reads it with
     jb_call("identity_secret")). The secret never leaves the process, so an outside client has no
     way to present it even from loopback.
A login missing EITHER is left completely untouched: BDS handles it as a normal login (and refuses
it on online-mode, as it should). The native shim additionally checks SelfSignedId ==
uuid5(namespace, xuid) and that the XUID is all digits.

The shim is loaded once and never writes anything until xf_install succeeds against a BDS build
profile it recognises. On a build with no matching profile the hook refuses to install and online
identity is disabled with a clear log line (offline identity still works); BDS is never crashed.
"""

import ctypes
import hmac
import ipaddress
from pathlib import Path

from endstone.event import EventPriority, PacketReceiveEvent, event_handler

from .beacon_claim import ClaimVerifier
from .login_body import is_self_signed, names_for_staging, parse_login_body

LOGIN_PACKET_ID = 0x01


def _is_loopback(host: str) -> bool:
    if not host:
        return False
    h = host.strip().lower()
    if h in ("localhost", "0:0:0:0:0:0:0:1"):
        return True
    h = h.split("%", 1)[0].strip("[]")
    try:
        return ipaddress.ip_address(h).is_loopback
    except ValueError:
        return h.startswith("127.")


def _local_addresses() -> set:
    """Every address of this machine: loopback and what its interfaces have."""
    import socket
    found = {"127.0.0.1", "::1"}
    try:
        for info in socket.getaddrinfo(socket.gethostname(), None):
            found.add(info[4][0].split("%", 1)[0].lower())
    except Exception:
        pass
    for family, probe in ((socket.AF_INET, "192.0.2.1"), (socket.AF_INET6, "2001:db8::1")):
        try:
            s = socket.socket(family, socket.SOCK_DGRAM)
            s.connect((probe, 9))  # no packet is sent: this only asks the routing table
            found.add(s.getsockname()[0].split("%", 1)[0].lower())
            s.close()
        except Exception:
            pass
    return found


class IdentityHook:
    """Loads and drives the native shim for delta's online identity."""

    def __init__(self, logger, package_dir, namespace: str):
        self._log = logger
        self._dir = Path(package_dir)
        self._namespace = namespace
        self._shim = None
        self._installed = False
        self._secret = b""
        self._proxies: set = set()
        # A Beacon forwarding secret (ClaimVerifier): Beacon's signed claim then decides for
        # relayed players instead of their address, as in xuidforward's forwarding mode.
        self._claims: ClaimVerifier | None = None
        # Borrowed: the xuidforward plugin hooked BDS first. This hook then stages only Java
        # players' identities, through xuidforward's own shim, and leaves relayed players to it.
        self._borrowed = False

    def set_proxies(self, sources) -> None:
        """Addresses a Bedrock proxy (Beacon or another relay) logs players in from
        ([bedrock] trusted_proxies). Its logins are self-signed and carry the player's real XUID,
        which the hook then restores: what the separate xuidforward plugin does, built in. The word
        "local" stands for every address of this machine."""
        out = set()
        for s in sources or []:
            s = str(s).strip().lower()
            if s == "local":
                out |= _local_addresses()
            elif s:
                out.add(s.split("%", 1)[0].strip("[]"))
        self._proxies = out

    def _from_proxy(self, host: str) -> bool:
        if not self._proxies or not host:
            return False
        return host.strip().lower().split("%", 1)[0].strip("[]") in self._proxies

    def set_claims(self, verifier: "ClaimVerifier | None") -> None:
        """Beacon's forwarding secret, as a claim verifier (None: trust by address only)."""
        self._claims = verifier

    def borrow(self, shim) -> None:
        """Use the xuidforward plugin's installed shim for Java players only (both plugins
        installed, xuidforward hooked BDS first)."""
        self._shim = shim
        self._installed = True
        self._borrowed = True

    @property
    def borrowed(self) -> bool:
        return self._borrowed

    def set_secret(self, secret: str) -> None:
        """The per-start trust secret (jb_call('identity_secret')). Re-read after every reload."""
        self._secret = (secret or "").encode("utf-8")

    @property
    def installed(self) -> bool:
        return self._installed

    def load(self) -> bool:
        so = self._dir / "auth" / "libxuidforward.so"
        if not so.exists():
            self._log.error(f"online identity: native hook missing at {so} - online identity unavailable")
            return False
        try:
            lib = ctypes.CDLL(str(so))
        except OSError as e:
            self._log.error(f"online identity: could not load the native hook ({e}) - online identity unavailable")
            return False
        for name in ("xf_clear_stage", "xf_self_check", "xf_install", "xf_stage", "xf_uninstall",
                     "xf_version", "xf_last_error"):
            if not hasattr(lib, name):
                self._log.error(f"online identity: the native hook is missing {name} - not using it")
                return False
        lib.xf_version.restype = ctypes.c_char_p
        lib.xf_last_error.restype = ctypes.c_char_p
        lib.xf_register_contract.restype = ctypes.c_char_p
        lib.xf_self_check.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_int]
        lib.xf_install.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_int,
                                   ctypes.c_int, ctypes.c_int]
        lib.xf_stage.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p]
        lib.xf_clear_stage.restype = ctypes.c_int
        if hasattr(lib, "xf_clear_stage_name"):
            lib.xf_clear_stage_name.argtypes = [ctypes.c_char_p]
            lib.xf_clear_stage_name.restype = ctypes.c_int
        self._shim = lib
        self._log.info(f"online identity: native hook loaded ({lib.xf_version().decode()})")
        return True

    def install(self) -> bool:
        if self._shim is None:
            return False
        buf = ctypes.create_string_buffer(512)
        candidates = [""] + sorted((str(p) for p in (self._dir / "auth" / "profiles").glob("*.json")), reverse=True)
        profile = None
        for cand in candidates:
            rc = self._shim.xf_self_check(cand.encode(), buf, len(buf))
            if rc == 0:
                shown = Path(cand).name or "built-in"
                self._log.info(f"online identity: BDS build recognised ({shown}): {buf.value.decode()}")
                profile = cand
                break
        if profile is None:
            self._log.warning(
                "online identity: this BDS build is not in any bundled profile - refusing to patch anything. "
                "ONLINE identity is disabled (offline identity still works). A new BDS build needs a new profile."
            )
            return False
        rc = self._shim.xf_install(profile.encode(), 0, str(self._namespace).encode(), 5, 1, 1)
        if rc != 0:
            err = self._shim.xf_last_error().decode()
            self._log.warning(f"online identity: hook not installed (rc={rc}): {err}. "
                              "This is the safe outcome - BDS keeps stock behaviour, online identity disabled.")
            return False
        try:
            self._log.info("online identity: hook installed. " + self._shim.xf_register_contract().decode())
        except Exception:
            self._log.info("online identity: hook installed.")
        self._installed = True
        return True

    def uninstall(self) -> None:
        self._installed = False
        if self._borrowed:
            return  # xuidforward's hook: it removes it itself
        if self._shim is not None:
            try:
                self._shim.xf_uninstall()
                self._log.info("online identity: hook removed - BDS is back to stock behaviour")
            except Exception as e:  # noqa: BLE001
                self._log.warning(f"online identity: hook not cleanly removed: {e}")

    # HIGHEST: after the xuidforward plugin's own handler (NORMAL) when both are installed, so it
    # can't clear a Java player's identity staged here.
    @event_handler(priority=EventPriority.HIGHEST)
    def on_packet_receive(self, event: PacketReceiveEvent) -> None:
        if self._shim is None or not self._installed:
            return
        if int(event.packet_id) != LOGIN_PACKET_ID:
            return
        try:
            host = (event.address.hostname or "").lower()
        except Exception:
            host = ""

        parsed = parse_login_body(event.payload)

        loopback = _is_loopback(host)
        secret_ok = bool(self._secret) and parsed is not None and hmac.compare_digest(
            self._secret, (parsed.get("nonce") or "").encode("utf-8")
        )
        # Two kinds of login get an identity: delta's own Java players (loopback and this
        # start's secret), and players a trusted Bedrock proxy relays (its address, no secret:
        # the proxy has verified them with Xbox Live and passes their real XUID on).
        if loopback and secret_ok:
            source = "java"
        elif self._borrowed:
            return  # xuidforward decides for every other login
        elif self._claims is not None:
            # Beacon's forwarding secret is set: its signed claim decides, wherever the login
            # comes from (Beacon may run on another machine).
            if not parsed:
                self._clear(parsed)
                return
            ok, reason = self._claims.verify(
                parsed.get("beacon_claim"), parsed.get("client_key", ""), parsed.get("xuid", ""), parsed.get("xname", "")
            )
            if not ok:
                self._clear(parsed)
                if parsed.get("xuid") and (parsed.get("auth_type") is None or is_self_signed(parsed)):
                    self._log.warning(f"online identity: login {parsed.get('xname')!r} from {host} keeps no XUID: {reason}")
                return
            source = "proxy"
        elif self._from_proxy(host):
            source = "proxy"
        else:
            self._clear(parsed)
            return

        if not parsed:
            return
        if parsed["auth_type"] is not None and not is_self_signed(parsed):
            return
        xuid = parsed["xuid"]
        names = names_for_staging(parsed)
        self_id = parsed["self_signed_id"]
        if not xuid or not names:
            return
        staged = []
        for name in names:
            rc = self._shim.xf_stage(name.encode(), xuid.encode(), str(self_id).encode())
            if rc == 0:
                staged.append(name)
        if staged:
            self._log.info(f"online identity: staged {staged} xuid={xuid}" + (" (relayed Bedrock player)" if source == "proxy" else ""))
        else:
            self._log.warning(f"online identity: NOT staged (names={names}): "
                              f"{self._shim.xf_last_error().decode()}")

    def _clear(self, parsed) -> None:
        if self._shim is None:
            return
        names = []
        if parsed:
            for key in ("xname", "third_party_name"):
                v = parsed.get(key)
                if isinstance(v, str) and v:
                    names.append(v)
        targeted = getattr(self._shim, "xf_clear_stage_name", None)
        try:
            if names and targeted is not None:
                for n in names:
                    targeted(n.encode())
            else:
                self._shim.xf_clear_stage()
        except Exception:
            pass
