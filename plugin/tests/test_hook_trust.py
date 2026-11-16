"""Unit test for the online-identity hook's trust gate, with no BDS.

Proves: a login is staged only when it is BOTH from a loopback source AND carries the exact
per-start secret (constant-time compare); a login missing either is not staged and its stale entry
is cleared; the secret is never required-equal to a wrong value. Endstone is stubbed so the module
imports without a server.
"""
import sys
import types
from pathlib import Path

# --- stub endstone.event so hook.py imports without Endstone ---
ev = types.ModuleType("endstone.event")
ev.PacketReceiveEvent = type("PacketReceiveEvent", (), {})
ev.EventPriority = types.SimpleNamespace(HIGHEST=4, NORMAL=2)
def event_handler(fn=None, **_kw):  # passthrough decorator, with or without arguments
    return fn if fn is not None else (lambda f: f)
ev.event_handler = event_handler
endstone = types.ModuleType("endstone")
endstone.__path__ = []  # mark as package
endstone.event = ev
plugin_mod = types.ModuleType("endstone.plugin")
plugin_mod.Plugin = type("Plugin", (), {})
cmd_mod = types.ModuleType("endstone.command")
cmd_mod.Command = type("Command", (), {})
cmd_mod.CommandSender = type("CommandSender", (), {})
sys.modules["endstone"] = endstone
sys.modules["endstone.event"] = ev
sys.modules["endstone.plugin"] = plugin_mod
sys.modules["endstone.command"] = cmd_mod

PKG = Path(sys.argv[1])  # path to the endstone_delta package dir
sys.path.insert(0, str(PKG.parent))

import importlib.util
# Import login_body then hook by file path, as a tiny standalone package, to avoid the
# plugin package __init__ (which imports _native/ctypes and the Plugin base).
auth_pkg = types.ModuleType("jbauth")
auth_pkg.__path__ = [str(PKG / "auth")]
sys.modules["jbauth"] = auth_pkg
def _load(modname, filename):
    spec = importlib.util.spec_from_file_location(modname, str(PKG / "auth" / filename))
    m = importlib.util.module_from_spec(spec); sys.modules[modname] = m; spec.loader.exec_module(m); return m
_load("jbauth.login_body", "login_body.py")
H = _load("jbauth.hook", "hook.py")



class FakeShim:
    def __init__(self):
        self.staged = []
        self.cleared = []
    def xf_stage(self, name, xuid, self_id):
        self.staged.append((name.decode(), xuid.decode(), self_id.decode()))
        return 0
    def xf_clear_stage_name(self, name):
        self.cleared.append(name.decode())
        return 0
    def xf_clear_stage(self):
        self.cleared.append("*ALL*")
        return 0
    def xf_last_error(self):
        return b""


class FakeLogger:
    def info(self, *a, **k): pass
    def warning(self, *a, **k): pass
    def error(self, *a, **k): pass


class FakeAddr:
    def __init__(self, host):
        self.hostname = host
        self.port = 12345


class FakeEvent:
    def __init__(self, packet_id, host, payload):
        self.packet_id = packet_id
        self.address = FakeAddr(host)
        self.payload = payload


SECRET = "a" * 64


def make_hook(monkey_parse):
    h = H.IdentityHook(FakeLogger(), PKG, "a3e78ee7-823a-4cb5-9fe0-532b54ccc20d")
    h._shim = FakeShim()
    h._installed = True
    h.set_secret(SECRET)
    # Stub the body parser to a controlled result.
    H.parse_login_body = monkey_parse
    return h


def good_parse(nonce, host_ok=True):
    def _p(_payload):
        return {
            "auth_type": 2, "xuid": "2535000000000001", "xname": "Prem",
            "third_party_name": "", "self_signed_id": "11111111-1111-1111-1111-111111111111",
            "beacon_claim": None, "client_key": "", "nonce": nonce, "framing": "test",
        }
    return _p


def run():
    results = []

    # 1. loopback + correct secret -> STAGED
    h = make_hook(good_parse(SECRET))
    h.on_packet_receive(FakeEvent(0x01, "127.0.0.1", b"x"))
    results.append(("loopback+secret -> staged", len(h._shim.staged) == 1 and h._shim.staged[0][1] == "2535000000000001"))

    # 2. loopback + WRONG secret -> NOT staged, cleared
    h = make_hook(good_parse("b" * 64))
    h.on_packet_receive(FakeEvent(0x01, "127.0.0.1", b"x"))
    results.append(("loopback+wrong-secret -> not staged", len(h._shim.staged) == 0 and len(h._shim.cleared) == 1))

    # 3. loopback + EMPTY secret in login -> NOT staged
    h = make_hook(good_parse(""))
    h.on_packet_receive(FakeEvent(0x01, "127.0.0.1", b"x"))
    results.append(("loopback+empty-secret -> not staged", len(h._shim.staged) == 0))

    # 4. NON-loopback + correct secret -> NOT staged (source gate)
    h = make_hook(good_parse(SECRET))
    h.on_packet_receive(FakeEvent(0x01, "203.0.113.7", b"x"))
    results.append(("outside+secret -> not staged", len(h._shim.staged) == 0 and len(h._shim.cleared) == 1))

    # 5. loopback IPv6 ::1 + secret -> STAGED
    h = make_hook(good_parse(SECRET))
    h.on_packet_receive(FakeEvent(0x01, "::1", b"x"))
    results.append(("ipv6-loopback+secret -> staged", len(h._shim.staged) == 1))

    # 5b. a trusted proxy's address, no secret -> STAGED (a relayed Bedrock player's real XUID)
    h = make_hook(good_parse(""))
    h.set_proxies(["127.0.0.1"])
    h.on_packet_receive(FakeEvent(0x01, "127.0.0.1", b"x"))
    results.append(("trusted proxy, no secret -> staged", len(h._shim.staged) == 1))

    # 5c. the same login from an address that is not a trusted proxy -> NOT staged
    h = make_hook(good_parse(""))
    h.set_proxies(["10.0.0.5"])
    h.on_packet_receive(FakeEvent(0x01, "203.0.113.7", b"x"))
    results.append(("untrusted address, proxies set -> not staged", len(h._shim.staged) == 0 and len(h._shim.cleared) == 1))

    # 6. hook not installed -> nothing happens
    h = make_hook(good_parse(SECRET))
    h._installed = False
    h.on_packet_receive(FakeEvent(0x01, "127.0.0.1", b"x"))
    results.append(("not installed -> no stage/clear", len(h._shim.staged) == 0 and len(h._shim.cleared) == 0))

    # 7. non-login packet ignored
    h = make_hook(good_parse(SECRET))
    h.on_packet_receive(FakeEvent(0x02, "127.0.0.1", b"x"))
    results.append(("non-login packet -> ignored", len(h._shim.staged) == 0 and len(h._shim.cleared) == 0))

    ok = True
    for name, passed in results:
        print(("PASS" if passed else "FAIL"), name)
        ok = ok and passed
    print("ALL PASS" if ok else "SOME FAILED")
    sys.exit(0 if ok else 1)


run()
