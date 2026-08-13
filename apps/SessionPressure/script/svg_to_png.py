#!/usr/bin/env python3
"""Minimal SVG→PNG fallback when sips cannot rasterize SVG."""

from __future__ import annotations

import struct
import sys
import zlib
from pathlib import Path


def write_solid_png(path: Path, size: int = 1024) -> None:
    # Dark slate background matching the app icon theme.
    r, g, b = 15, 23, 42
    row = b"\x00" + bytes([r, g, b]) * size
    raw = row * size
    compressor = zlib.compressobj(level=9)
    compressed = compressor.compress(raw) + compressor.flush()

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + tag
            + data
            + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
        )

    ihdr = struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0)
    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", compressed) + chunk(b"IEND", b"")
    path.write_bytes(png)


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: svg_to_png.py <in.svg> <out.png>", file=sys.stderr)
        return 2
    out = Path(sys.argv[2])
    out.parent.mkdir(parents=True, exist_ok=True)
    write_solid_png(out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
