"""Delta replacing, or running next to, the xuidforward plugin. No BDS needed.

Proves: Beacon's signed claim stages a relayed player from any address and a bad claim does not;
with xuidforward's hook borrowed only Java players are staged here; the trust settings come from
config.toml, else from xuidforward's config.json, else "local" when Java players have an identity.
"""
import base64
import hashlib
import hmac
import json
import sys
import tempfile
import time
import types
from pathlib import Path

ev = types.ModuleType("endstone.event")
ev.PacketReceiveEvent = type("PacketReceiveEvent", (), {})
ev.EventPriority = types.SimpleNamespace(HIGHEST=4, NORMAL=2)
def event_handler(fn=None, **_kw):
    return fn if fn is not None else (lambda f: f)
ev.event_handler = event_handler
endstone = types.ModuleType("endstone"); endstone.__path__ = []; endstone.event = ev
sys.modules["endstone"] = endstone
sys.modules["endstone.event"] = ev

PKG = Path(sys.argv[1])
import importlib.util
auth_pkg = types.ModuleType("jbauth"); auth_pkg.__path__ = [str(PKG / "auth")]
sys.modules["jbauth"] = auth_pkg
def _load(modname, filename):
    spec = importlib.util.spec_from_file_location(modname, str(PKG / "auth" / filename))
    m = importlib.util.module_from_spec(spec); sys.modules[modname] = m; spec.loader.exec_module(m); return m
_load("jbauth.login_body", "login_body.py")
BC = _load("jbauth.beacon_claim", "beacon_claim.py")
H = _load("jbauth.hook", "hook.py")
TC = _load("jbauth.trust_config", "trust_config.py")


class Shim:
    def __init__(self): self.staged, self.cleared = [], []
    def xf_stage(self, n, x, i): self.staged.append((n.decode(), x.decode())); return 0
    def xf_clear_stage_name(self, n): self.cleared.append(n.decode()); return 0
    def xf_clear_stage(self): self.cleared.append("*"); return 0
    def xf_last_error(self): return b""

class Log:
    def __init__(self): self.warnings = []
    def info(self, *a): pass
    def warning(self, m): self.warnings.append(m)
    def error(self, *a): pass

class Ev:
    def __init__(self, host):
        self.packet_id = 0x01; self.payload = b"x"
        self.address = types.SimpleNamespace(hostname=host, port=1)

SECRET = b"beacon-secret"
KEY = "MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAE-test-key"

def claim(xuid="2535000000000009", name="Relayed", server="lobby", secret=SECRET, iat=None, jti="j1"):
    c = {"v": 1, "xuid": xuid, "name": name, "identity": "id", "ip": "198.51.100.1", "server": server,
         "iat": int(iat if iat is not None else time.time()), "jti": jti}
    fields = ["beacon-forwarding-v1", c["xuid"], c["name"], c["identity"], c["ip"], c["server"], str(c["iat"]), c["jti"], KEY]
    c["mac"] = base64.b64encode(hmac.new(secret, "\n".join(fields).encode(), hashlib.sha256).digest()).decode()
    return c

def login(c=None, nonce="", xuid="2535000000000009", name="Relayed"):
    def _p(_):
        return {"auth_type": 2, "xuid": xuid, "xname": name, "third_party_name": "", "framing": "t",
                "self_signed_id": "11111111-1111-1111-1111-111111111111",
                "beacon_claim": c, "client_key": KEY, "nonce": nonce}
    return _p

JAVA = "j" * 64

def hook(parse, claims=None, proxies=None):
    h = H.IdentityHook(Log(), PKG, "a3e78ee7-823a-4cb5-9fe0-532b54ccc20d")
    h._shim, h._installed = Shim(), True
    h.set_secret(JAVA); h.set_proxies(proxies or []); h.set_claims(claims)
    H.parse_login_body = parse
    return h

R = []
def check(name, ok): R.append((name, bool(ok)))

v = lambda server="": BC.ClaimVerifier(SECRET, server, 60)

h = hook(login(claim()), claims=v())
h.on_packet_receive(Ev("203.0.113.9"))
check("signed claim, other machine -> staged", h._shim.staged == [("Relayed", "2535000000000009")])

h = hook(login(claim(secret=b"wrong")), claims=v())
h.on_packet_receive(Ev("127.0.0.1"))
check("bad signature -> not staged, cleared, warned", not h._shim.staged and h._shim.cleared and h._log.warnings)

h = hook(login(claim(server="kits", jti="j2")), claims=v("lobby"))
h.on_packet_receive(Ev("127.0.0.1"))
check("claim for another server -> not staged", not h._shim.staged)

h = hook(login(None), claims=v())
h.on_packet_receive(Ev("127.0.0.1"))
check("secret set, no claim, local address -> not staged", not h._shim.staged)

h = hook(login(claim(jti="j3")), claims=v())
h.on_packet_receive(Ev("127.0.0.1")); h.on_packet_receive(Ev("127.0.0.1"))
check("replayed claim -> staged once", len(h._shim.staged) == 1)

h = hook(login(None, nonce=JAVA, name="JavaGuy", xuid="2535000000000077"), claims=v())
h.on_packet_receive(Ev("127.0.0.1"))
check("Java player with secret set -> staged", h._shim.staged == [("JavaGuy", "2535000000000077")])

h = hook(login(None), proxies=["127.0.0.1"])
xfshim = Shim(); h.borrow(xfshim)
h.on_packet_receive(Ev("127.0.0.1"))
check("borrowed: relayed player left to xuidforward", not xfshim.staged and not xfshim.cleared)

h = hook(login(None, nonce=JAVA, name="JavaGuy", xuid="2535000000000077"))
xfshim = Shim(); h.borrow(xfshim)
h.on_packet_receive(Ev("127.0.0.1"))
check("borrowed: Java player staged in xuidforward's shim", xfshim.staged == [("JavaGuy", "2535000000000077")])
h.uninstall()
check("borrowed: uninstall leaves xuidforward's hook", True)

t = TC.trust_settings({"trusted_proxies": ["10.0.0.2"]}, {"allowed_sources": ["9.9.9.9"]}, True)
check("config.toml wins", t["sources"] == ["10.0.0.2"] and t["origin"] == "config.toml")
t = TC.trust_settings({}, {"auto_allow_local_addresses": True, "allowed_sources": ["127.0.0.1", "::1"],
                           "forwarding_secret_file": "fw.secret", "beacon_server_name": "lobby"}, True)
check("xuidforward's config read", t["sources"] == ["local", "127.0.0.1", "::1"] and t["secret_file"] == "fw.secret"
      and t["server_name"] == "lobby" and t["origin"] == "xuidforward's config.json")
check("default local with Java identity", TC.trust_settings({}, None, True)["sources"] == ["local"])
check("offline, nothing set -> nothing trusted", TC.trust_settings({}, None, False)["sources"] == [])
check("explicit empty list -> off", TC.trust_settings({"trusted_proxies": []}, None, True)["sources"] == [])
with tempfile.TemporaryDirectory() as d:
    (Path(d) / "xuidforward").mkdir()
    (Path(d) / "xuidforward" / "config.json").write_text(json.dumps({"enabled": False}))
    check("disabled xuidforward config ignored", TC.read_xuidforward_config(Path(d)) is None)
    (Path(d) / "xuidforward" / "config.json").write_text(json.dumps({"forwarding_secret": "s"}))
    check("xuidforward config found", TC.read_xuidforward_config(Path(d)) == {"forwarding_secret": "s"})

ok = all(p for _, p in R)
for n, p in R: print("PASS" if p else "FAIL", n)
print("ALL PASS" if ok else "SOME FAILED"); sys.exit(0 if ok else 1)
