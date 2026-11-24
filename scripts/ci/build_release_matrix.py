#!/usr/bin/env python3
"""Build release-matrix.json for dual-mirror sync (dl.svc.plus + cn-dl.svc.plus)."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
from typing import Any


def detect_platform(filename: str) -> str:
    low = filename.lower()
    if "macos" in low or "darwin" in low or low.endswith(".dmg"):
        return "macos"
    if "windows" in low or low.endswith(".exe") or low.endswith(".msi"):
        return "windows"
    if "android" in low or low.endswith(".apk") or low.endswith(".aab"):
        return "android"
    if "ios" in low or low.endswith(".ipa"):
        return "ios"
    return "linux"


def detect_arch(filename: str) -> str:
    low = filename.lower()
    if "arm64" in low or "aarch64" in low:
        return "arm64"
    if "amd64" in low or "x86_64" in low or "x64" in low:
        return "amd64"
    return "universal"


def detect_format(filename: str) -> str:
    low = filename.lower()
    for ext in (".tar.gz", ".dmg", ".deb", ".rpm", ".msi", ".exe", ".zip", ".apk", ".aab", ".ipa", ".appimage"):
        if low.endswith(ext):
            return ext.lstrip(".")
    return "binary"


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def build_matrix(product: str, tag: str, channel: str, dist_dir: Path) -> dict[str, Any]:
    version = tag.lstrip("v")
    by_platform: dict[str, list[dict[str, Any]]] = {}
    for file_path in sorted(dist_dir.iterdir()):
        if not file_path.is_file():
            continue
        name = file_path.name
        if name.startswith("SHA256SUMS") or name == "release-matrix.json":
            continue
        platform = detect_platform(name)
        pkg = {
            "name": name,
            "arch": detect_arch(name),
            "format": detect_format(name),
            "size": file_path.stat().st_size,
            "sha256": sha256_file(file_path),
            "url": f"https://dl.svc.plus/releases/{product}/{tag}/{name}",
            "cnUrl": f"https://cn-dl.svc.plus/releases/{product}/{tag}/{name}",
            "latestUrl": f"https://dl.svc.plus/releases/{product}/latest/{name}",
            "cnLatestUrl": f"https://cn-dl.svc.plus/releases/{product}/latest/{name}",
        }
        by_platform.setdefault(platform, []).append(pkg)

    platforms = [{"id": pid, "packages": pkgs} for pid, pkgs in sorted(by_platform.items())]
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    entry = {
        "tag": tag,
        "version": version,
        "channel": channel,
        "publishedAt": now,
        "platforms": platforms,
    }
    return {
        "schemaVersion": 1,
        "product": product,
        "defaultChannel": channel,
        "channels": {channel: entry},
        "history": [entry],
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--product", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--channel", default="latest")
    parser.add_argument("--dist-dir", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    matrix = build_matrix(args.product, args.tag, args.channel, Path(args.dist_dir))
    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(matrix, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {out} ({sum(len(p['packages']) for p in matrix['channels'][args.channel]['platforms'])} packages)")


if __name__ == "__main__":
    main()
