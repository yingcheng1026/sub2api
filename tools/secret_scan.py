#!/usr/bin/env python3
"""Lightweight tracked-file secret scanner.

The scanner never prints matched values. Public OAuth client credentials are
allowed only by an exact SHA-256 digest, never by file or directory.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, Sequence


@dataclass(frozen=True)
class Rule:
    name: str
    pattern: re.Pattern[str]
    placeholder_allowlist: Sequence[re.Pattern[str]]


# These are documented public OAuth client credentials required for protocol
# compatibility. The values themselves are intentionally not stored here.
PUBLIC_CREDENTIAL_SHA256_ALLOWLIST: dict[str, str] = {
    "1d2f041093fd95aa8995a038c711d50a7960da09a505381c09a745d6ad0ecc60": (
        "Antigravity documented public OAuth client credential"
    ),
    "6a5f78b8b99dd4025e41ba11bf54c304c6af29f924c5569cc7865b2428ce03a9": (
        "Gemini CLI documented public OAuth client credential"
    ),
}


RULES: tuple[Rule, ...] = (
    Rule(
        name="google_oauth_client_secret",
        pattern=re.compile(r"GOCSPX-[0-9A-Za-z_-]{24,}"),
        placeholder_allowlist=(
            re.compile(r"GOCSPX-your-"),
            re.compile(r"GOCSPX-REDACTED"),
        ),
    ),
    Rule(
        name="google_api_key",
        pattern=re.compile(r"AIza[0-9A-Za-z_-]{35}"),
        placeholder_allowlist=(
            re.compile(r"AIza\.{3}"),
            re.compile(r"AIza-your-"),
            re.compile(r"AIza-REDACTED"),
        ),
    ),
)


def iter_git_files(repo_root: Path) -> list[Path]:
    try:
        output = subprocess.check_output(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            cwd=repo_root,
            stderr=subprocess.DEVNULL,
        )
    except (OSError, subprocess.CalledProcessError):
        return []

    files: list[Path] = []
    for raw_path in output.split(b"\0"):
        if not raw_path:
            continue
        try:
            relative = raw_path.decode("utf-8")
        except UnicodeDecodeError:
            continue
        path = repo_root / relative
        if path.is_file() and not path.is_symlink():
            files.append(path)
    return files


def iter_walk_files(repo_root: Path) -> Iterable[Path]:
    for dirpath, dirnames, filenames in os.walk(repo_root):
        dirnames[:] = [name for name in dirnames if name != ".git"]
        for name in filenames:
            path = Path(dirpath) / name
            if not path.is_symlink():
                yield path


def should_skip(path: Path, repo_root: Path) -> bool:
    relative = path.relative_to(repo_root).as_posix()
    binary_suffixes = (
        ".png",
        ".jpg",
        ".jpeg",
        ".gif",
        ".pdf",
        ".zip",
        ".tgz",
        ".gz",
    )
    return relative.endswith(binary_suffixes) or relative.startswith("backend/bin/")


def is_exact_public_credential(value: str) -> bool:
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()
    return digest in PUBLIC_CREDENTIAL_SHA256_ALLOWLIST


def scan_file(path: Path, repo_root: Path) -> list[str]:
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return []

    relative = path.relative_to(repo_root).as_posix()
    findings: list[str] = []
    for line_number, line in enumerate(text.splitlines(), start=1):
        for rule in RULES:
            for match in rule.pattern.finditer(line):
                value = match.group(0)
                if any(pattern.search(value) for pattern in rule.placeholder_allowlist):
                    continue
                if is_exact_public_credential(value):
                    continue
                findings.append(f"{relative}:{line_number} ({rule.name})")
    return findings


def main(argv: Sequence[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--repo-root",
        default=str(Path(__file__).resolve().parents[1]),
        help="repository root (default: parent of tools)",
    )
    args = parser.parse_args(argv)

    repo_root = Path(args.repo_root).resolve()
    files = iter_git_files(repo_root)
    if not files:
        files = list(iter_walk_files(repo_root))

    findings: list[str] = []
    for path in files:
        if should_skip(path, repo_root):
            continue
        findings.extend(scan_file(path, repo_root))

    if findings:
        sys.stderr.write("Secret scan FAILED. Potential secrets detected:\n")
        for finding in findings:
            sys.stderr.write(f"- {finding}\n")
        sys.stderr.write("\nRemove the credential or use an explicit placeholder.\n")
        return 1

    print("Secret scan OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
