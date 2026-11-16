"""Loader for the native core (a Go c-shared library) and its C interface.

The library is loaded once per process and kept for the life of the process: a Go runtime
cannot be unloaded, and loading a second copy (e.g. after a plugin reload reinstalled the wheel
at the same path, which gives the file a new inode) would put two Go runtimes in one process.
So the loaded handle is kept on the ``sys`` module, which survives Endstone's plugin reloads, and
the file is first copied to a content-addressed path, so the same build always loads from the
same file.

C interface (see docs/NATIVE.md in the repository):
    char* jb_version(void)
    int   jb_start(const char* config_json)   0 ok, -1 error (jb_last_error), -2 already running
    int   jb_stop(int timeout_ms)             0 ok, -1 error / timeout
    int   jb_running(void)
    char* jb_poll_log(void)                   "LEVEL\\tmessage\\n"..., or NULL
    char* jb_call(const char* request_json)   {"ok":..., "result"|"error":...}
    char* jb_last_error(void)
    void  jb_free(char*)
Every returned char* must be freed with jb_free.
"""

from __future__ import annotations

import ctypes
import hashlib
import json
import os
import platform
import shutil
import sys
from pathlib import Path
from typing import Any

_SYS_KEY = "_delta_native_library"


def library_filename() -> str:
    """The file name of the native library for this platform inside the package."""
    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(machine, machine)
    if sys.platform.startswith("linux"):
        return f"libdelta-linux-{arch}.so"
    if sys.platform == "win32":
        return f"delta-windows-{arch}.dll"
    raise OSError(f"unsupported platform {sys.platform}/{machine}")


class NativeError(RuntimeError):
    pass


class Native:
    """The loaded library. Use :func:`load` to get the process-wide instance."""

    def __init__(self, lib: ctypes.CDLL, path: str, digest: str):
        self._lib = lib
        self.path = path
        self.digest = digest
        c = ctypes
        lib.jb_version.restype = c.c_void_p
        lib.jb_start.argtypes = [c.c_char_p]
        lib.jb_start.restype = c.c_int
        lib.jb_stop.argtypes = [c.c_int]
        lib.jb_stop.restype = c.c_int
        lib.jb_running.restype = c.c_int
        lib.jb_poll_log.restype = c.c_void_p
        lib.jb_call.argtypes = [c.c_char_p]
        lib.jb_call.restype = c.c_void_p
        lib.jb_last_error.restype = c.c_void_p
        lib.jb_free.argtypes = [c.c_void_p]
        lib.jb_free.restype = None

    def _take(self, ptr: int | None) -> str | None:
        if not ptr:
            return None
        try:
            return ctypes.string_at(ptr).decode("utf-8", "replace")
        finally:
            self._lib.jb_free(ptr)

    def version(self) -> str:
        return self._take(self._lib.jb_version()) or ""

    def last_error(self) -> str:
        return self._take(self._lib.jb_last_error()) or ""

    def start(self, config: dict[str, Any]) -> None:
        rc = self._lib.jb_start(json.dumps(config).encode())
        if rc == -2:
            raise NativeError("the core is already running")
        if rc != 0:
            raise NativeError(self.last_error() or f"jb_start returned {rc}")

    def stop(self, timeout_ms: int = 5000) -> None:
        if self._lib.jb_stop(int(timeout_ms)) != 0:
            raise NativeError(self.last_error() or "jb_stop failed")

    def running(self) -> bool:
        return bool(self._lib.jb_running())

    def poll_log(self) -> list[tuple[str, str]]:
        text = self._take(self._lib.jb_poll_log())
        if not text:
            return []
        out = []
        for line in text.splitlines():
            level, _, msg = line.partition("\t")
            out.append((level, msg.replace("\\n", "\n")))
        return out

    def call(self, method: str, params: Any = None) -> Any:
        raw = self._take(self._lib.jb_call(json.dumps({"method": method, "params": params}).encode()))
        answer = json.loads(raw or "{}")
        if not answer.get("ok"):
            raise NativeError(answer.get("error", "no answer"))
        return answer.get("result")


def load(cache_dir: str | os.PathLike) -> Native:
    """Load the library shipped in this package (once per process)."""
    existing = getattr(sys, _SYS_KEY, None)
    src = Path(__file__).with_name("native") / library_filename()
    if not src.exists():
        raise NativeError(f"native library missing: {src}")
    digest = hashlib.sha256(src.read_bytes()).hexdigest()[:16]
    if existing is not None:
        if existing.digest != digest:
            # The wheel was replaced while the server ran. The old library stays in use
            # until the server restarts.
            existing.stale = True
        return existing
    cache = Path(cache_dir) / "native"
    cache.mkdir(parents=True, exist_ok=True)
    stem, ext = os.path.splitext(src.name)
    dst = cache / f"{stem}-{digest}{ext}"
    if not dst.exists():
        tmp = dst.with_suffix(ext + ".tmp")
        shutil.copyfile(src, tmp)
        os.replace(tmp, dst)
    for old in cache.glob(f"{stem}-*{ext}"):
        if old != dst:
            try:
                old.unlink()
            except OSError:
                pass
    native = Native(ctypes.CDLL(str(dst)), str(dst), digest)
    native.stale = False
    setattr(sys, _SYS_KEY, native)
    return native
