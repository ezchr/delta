#!/usr/bin/env python3
"""Build the plugin wheel without any build backend (standard library only).

    python3 tools/mkwheel.py --version 0.1.0 --plat linux_x86_64 \
        --native build/libjavabridge-linux-amd64.so [--native ...] --out dist

The package is plugin/src/endstone_javabridge; native libraries go into its native/ folder.
The result is deterministic for the same inputs (fixed timestamps, sorted entries).
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import os
import zipfile
from pathlib import Path

NAME = "javabridge"  # the project name: the only place the wheel takes it from
DIST = f"endstone_{NAME}"
STAMP = (2020, 1, 1, 0, 0, 0)


def record_hash(data: bytes) -> str:
    return "sha256=" + base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", required=True)
    ap.add_argument("--plat", required=True, help="wheel platform tag, e.g. linux_x86_64 or win_amd64")
    ap.add_argument("--src", default="plugin/src")
    ap.add_argument("--native", action="append", default=[])
    ap.add_argument("--out", default="dist")
    a = ap.parse_args()

    files: dict[str, bytes] = {}
    pkg = Path(a.src) / DIST
    for p in sorted(pkg.rglob("*")):
        if p.is_file() and "__pycache__" not in p.parts and p.suffix not in (".pyc", ".so", ".dll"):
            files[p.relative_to(a.src).as_posix()] = p.read_bytes()
    for n in a.native:
        files[f"{DIST}/native/{Path(n).name}"] = Path(n).read_bytes()

    info = f"{DIST}-{a.version}.dist-info"
    files[f"{info}/METADATA"] = (
        "Metadata-Version: 2.1\n"
        f"Name: endstone-{NAME}\n"
        f"Version: {a.version}\n"
        "Summary: Lets Java Edition players join an Endstone Bedrock server\n"
        "License: MIT\n"
        "Requires-Python: >=3.10\n"
    ).encode()
    files[f"{info}/WHEEL"] = (
        "Wheel-Version: 1.0\nGenerator: javabridge-mkwheel\nRoot-Is-Purelib: false\n"
        f"Tag: py3-none-{a.plat}\n"
    ).encode()
    files[f"{info}/entry_points.txt"] = f"[endstone]\n{NAME} = {DIST}:JavaBridgePlugin\n".encode()
    record = "".join(f"{k},{record_hash(v)},{len(v)}\n" for k, v in sorted(files.items()))
    record += f"{info}/RECORD,,\n"
    files[f"{info}/RECORD"] = record.encode()

    os.makedirs(a.out, exist_ok=True)
    out = Path(a.out) / f"{DIST}-{a.version}-py3-none-{a.plat}.whl"
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for k in sorted(files):
            zi = zipfile.ZipInfo(k, STAMP)
            zi.external_attr = (0o755 if k.endswith((".so", ".dll")) else 0o644) << 16
            zi.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(zi, files[k])
    print(out)


if __name__ == "__main__":
    main()
