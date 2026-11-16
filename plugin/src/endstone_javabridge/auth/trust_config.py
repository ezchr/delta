"""Who may forward a Bedrock player's real XUID to this server.

The settings come from [bedrock] in config.toml. A server that used the xuidforward plugin before
needs no new settings: its plugins/xuidforward/config.json fills in whatever config.toml leaves
out, so removing xuidforward changes nothing. Kept free of Endstone so it can be unit tested.
"""

from __future__ import annotations

import json
from pathlib import Path

# xuidforward's own default: a proxy on this machine.
DEFAULT_SOURCES = ["local"]


def read_xuidforward_config(plugins_dir: Path) -> dict | None:
    """The old xuidforward plugin's config.json, or None when there is none (or it was disabled)."""
    path = Path(plugins_dir) / "xuidforward" / "config.json"
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001 - no file or unreadable: nothing to carry over
        return None
    if not isinstance(data, dict) or data.get("enabled") is False:
        return None
    return data


def trust_settings(bedrock: dict, xuidforward: dict | None, java_identity: bool) -> dict:
    """The effective trust settings.

    sources: addresses whose self-signed logins keep their XUID ("local" = this machine).
    secret / secret_file: Beacon's forwarding secret; when set, Beacon's signed claim decides
    instead of the address, so Beacon may run on another machine (xuidforward's forwarding mode).
    server_name, window: the claim's server name to require ("" = any) and its maximum age.
    origin: where the settings came from, for the log.

    java_identity is whether Java players get an identity of their own (account mode online or
    hybrid). Without it and without any setting, nothing is trusted: an offline-mode server
    keeps BDS's own handling.
    """
    xf = xuidforward or {}
    origin = "config.toml"
    if "trusted_proxies" in bedrock:
        v = bedrock.get("trusted_proxies") or []
        sources = [v] if isinstance(v, str) else list(v)
    elif xuidforward is not None:
        sources = (["local"] if xf.get("auto_allow_local_addresses", True) else []) + list(xf.get("allowed_sources", []) or [])
        origin = "xuidforward's config.json"
    elif java_identity:
        sources = list(DEFAULT_SOURCES)
        origin = "default"
    else:
        sources = []
    sources = [str(s).strip() for s in sources if str(s).strip()]

    def pick(key: str, default=""):
        if str(bedrock.get(key, "") or "").strip():
            return bedrock[key]
        return xf.get(key, default) or default

    return {
        "sources": sources,
        "secret": str(pick("forwarding_secret")).strip(),
        "secret_file": str(pick("forwarding_secret_file")).strip(),
        "server_name": str(pick("beacon_server_name")).strip(),
        "window": int(pick("claim_window_seconds", 60) or 60),
        "origin": origin,
    }
