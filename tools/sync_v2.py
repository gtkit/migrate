#!/usr/bin/env python3

from __future__ import annotations

import argparse
import shutil
from pathlib import Path


SYNC_ITEMS = [
    "command",
    "console",
    "file",
    "make",
    "migration",
    ".gitignore",
    "go.mod",
    "go.sum",
    "Makefile",
    "migrate.go",
    "README.md",
    "version.go",
]

TEXT_SUFFIXES = {".go", ".mod", ".sum", ".md", ".stub"}
TEXT_NAMES = {"Makefile", ".gitignore"}
ROOT_MODULE = b"github.com/gtkit/migrate"
V2_MODULE = b"github.com/gtkit/migrate/v2"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Synchronize the v2 module using byte-safe replacements.",
    )
    parser.add_argument(
        "--repo",
        type=Path,
        default=Path.cwd(),
        help="Repository root containing the legacy module and the v2 folder.",
    )
    parser.add_argument(
        "--version",
        help='Override v2/version.go, for example "v2.0.4".',
    )
    return parser.parse_args()


def copy_source_tree(repo: Path, target: Path) -> None:
    if target.exists():
        shutil.rmtree(target)
    target.mkdir()

    for name in SYNC_ITEMS:
        source = repo / name
        destination = target / name
        if source.is_dir():
            shutil.copytree(source, destination)
        else:
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)


def rewrite_paths(target: Path) -> None:
    for path in target.rglob("*"):
        if not path.is_file():
            continue
        if path.suffix not in TEXT_SUFFIXES and path.name not in TEXT_NAMES:
            continue

        data = path.read_bytes()
        updated = data.replace(ROOT_MODULE, V2_MODULE)
        if updated != data:
            path.write_bytes(updated)


def rewrite_version(target: Path, version: str | None) -> None:
    if not version:
        return

    version_path = target / "version.go"
    version_path.write_text(
        f'package migrate\n\nconst Version = "{version}"\n',
        encoding="utf-8",
    )


def normalize_v2_makefile(target: Path) -> None:
    makefile_path = target / "Makefile"
    content = makefile_path.read_text(encoding="utf-8")
    content = content.replace(
        ".PHONY: lint check tag gittag sync-v2",
        ".PHONY: lint check tag gittag",
    )
    content = content.replace("PYTHON ?= python\n", "")
    content = content.replace("V2_VERSION ?=\n", "")
    block = (
        "sync-v2:\n"
        "\t@if [ -n \"$(V2_VERSION)\" ]; then \\\n"
        "\t\t$(PYTHON) tools/sync_v2.py --repo . --version $(V2_VERSION); \\\n"
        "\telse \\\n"
        "\t\t$(PYTHON) tools/sync_v2.py --repo .; \\\n"
        "\tfi\n\n"
    )
    if block in content:
        content = content.replace(block, "")
        makefile_path.write_text(content, encoding="utf-8")


def main() -> None:
    args = parse_args()
    repo = args.repo.resolve()
    target = repo / "v2"

    copy_source_tree(repo, target)
    rewrite_paths(target)
    rewrite_version(target, args.version)
    normalize_v2_makefile(target)


if __name__ == "__main__":
    main()
