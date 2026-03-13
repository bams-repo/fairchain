#!/usr/bin/env python3
"""
Cross-platform chaos test entrypoint.

This script provides OS detection and launches the existing chaos test backend
script in a platform-aware way:
  - Linux/macOS: bash
  - Windows: Git Bash (if available) or WSL bash

Usage:
  python scripts/chaos_test.py [--skip PHASES]
"""

from __future__ import annotations

import argparse
import os
import platform
import re
import shlex
import shutil
import subprocess
import sys
from pathlib import Path

EXPECTED_SKIP_PHASES = {
    "2",
    "3",
    "4",
    "5",
    "6",
    "7",
    "8",
    "9",
    "10",
    "11",
    "12",
    "13",
    "14",
    "15",
    "16",
    "A",
    "B",
    "C",
    "D",
    "E",
    "F",
    "H",
    "I",
    "J",
    "K",
    "L",
    "M",
}

EXPECTED_GROUPS = {"chaos", "adversarial", "consensus", "utxo"}


def _is_windows() -> bool:
    return platform.system().lower().startswith("win")


def _project_root() -> Path:
    return Path(__file__).resolve().parent.parent


def _backend_script() -> Path:
    return _project_root() / "scripts" / "chaos_test.sh"


def _find_windows_bash() -> list[list[str]]:
    """
    Return candidate shell commands for running bash scripts on Windows.
    """
    candidates: list[list[str]] = []

    # Git Bash / MSYS on PATH
    for name in ("bash", "bash.exe"):
        p = shutil.which(name)
        if p:
            candidates.append([p])

    # Common Git for Windows install path
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    if git_bash.exists():
        candidates.append([str(git_bash)])

    # Common MSYS2 path
    msys_bash = Path("C:/msys64/usr/bin/bash.exe")
    if msys_bash.exists():
        candidates.append([str(msys_bash)])

    # WSL fallback
    wsl = shutil.which("wsl") or shutil.which("wsl.exe")
    if wsl:
        candidates.append([wsl, "bash"])

    return candidates


def _verify_backend_parity(backend: Path) -> tuple[bool, str]:
    """
    Ensure the shell backend still exposes the full expected phase and group set.
    """
    try:
        text = backend.read_text(encoding="utf-8")
    except OSError as exc:
        return False, f"cannot read backend script: {exc}"

    phase_hits = set(re.findall(r'should_skip\s+"([0-9A-Z]+)"', text))
    missing_phases = sorted(EXPECTED_SKIP_PHASES - phase_hits)

    groups_found = set(re.findall(r"^\s*(chaos|adversarial|consensus|utxo)\)", text, re.MULTILINE))
    missing_groups = sorted(EXPECTED_GROUPS - groups_found)

    if missing_phases or missing_groups:
        details = []
        if missing_phases:
            details.append(f"missing phases: {','.join(missing_phases)}")
        if missing_groups:
            details.append(f"missing skip groups: {','.join(missing_groups)}")
        return False, "; ".join(details)

    return True, "ok"


def _run_backend(skip: str | None) -> int:
    backend = _backend_script()
    if not backend.exists():
        print(f"[chaos] backend script not found: {backend}", file=sys.stderr)
        return 1

    parity_ok, parity_msg = _verify_backend_parity(backend)
    if not parity_ok:
        print(f"[chaos] backend parity check failed: {parity_msg}", file=sys.stderr)
        return 1
    print("[chaos] backend parity: verified")

    extra_args: list[str] = []
    if skip:
        extra_args.extend(["--skip", skip])

    env = os.environ.copy()
    env["CHAOS_LAUNCHER"] = "python"
    env["CHAOS_HOST_OS"] = platform.system().lower()

    if _is_windows():
        # Try bash-compatible shells in order.
        for shell_cmd in _find_windows_bash():
            if shell_cmd[0].lower().endswith("wsl.exe") or shell_cmd[0].lower().endswith("wsl"):
                quoted = " ".join(shlex.quote(x) for x in [backend.as_posix(), *extra_args])
                cmd = shell_cmd + ["-lc", quoted]
            else:
                cmd = shell_cmd + [str(backend)] + extra_args

            try:
                return subprocess.run(cmd, cwd=str(_project_root()), env=env).returncode
            except FileNotFoundError:
                continue

        print(
            "[chaos] no bash-compatible shell found on Windows.\n"
            "Install Git Bash or enable WSL, then re-run:\n"
            "  python scripts/chaos_test.py",
            file=sys.stderr,
        )
        return 1

    # Linux/macOS: require bash.
    bash = shutil.which("bash")
    if not bash:
        print("[chaos] bash not found in PATH", file=sys.stderr)
        return 1

    cmd = [bash, str(backend)] + extra_args
    return subprocess.run(cmd, cwd=str(_project_root()), env=env).returncode


def main() -> int:
    parser = argparse.ArgumentParser(description="Cross-platform Fairchain chaos test launcher")
    parser.add_argument(
        "--skip",
        default="",
        help="Comma-separated phase IDs or aliases to skip (passed through to backend)",
    )
    parser.add_argument(
        "--check-parity",
        action="store_true",
        help="Verify phase parity against the shell backend and exit",
    )
    args = parser.parse_args()
    skip = args.skip.strip() or None

    print(f"[chaos] launcher OS: {platform.system()}")
    if args.check_parity:
        ok, msg = _verify_backend_parity(_backend_script())
        if ok:
            print("[chaos] backend parity: verified")
            return 0
        print(f"[chaos] backend parity check failed: {msg}", file=sys.stderr)
        return 1
    return _run_backend(skip)


if __name__ == "__main__":
    raise SystemExit(main())
