#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# setup.sh -- installs what video_capture.py needs, once, without sudo: a Python venv holding the pinned Playwright + PyYAML
# (requirements.txt) and the Chromium build that Playwright release pins. ffmpeg must already be on PATH (it encodes the clips).
#
#   setup.sh           install (re-running is harmless)
#   setup.sh --check   say whether the recorder is ready; exit 0 only when it is
#
# The venv lives at ~/.cache/video-capture/venv (override: VIDEO_CAPTURE_HOME). Run the recorder with that venv's python.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="${VIDEO_CAPTURE_HOME:-$HOME/.cache/video-capture}"
VENV="$HOME_DIR/venv"
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) PY="$VENV/Scripts/python" ;; *) PY="$VENV/bin/python" ;; esac

log() { printf '[video-capture] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

check() {
	command -v ffmpeg >/dev/null 2>&1 || { log "ffmpeg is not on PATH"; return 1; }
	[ -x "$PY" ] || { log "no venv at $VENV"; return 1; }
	"$PY" -c 'import yaml
from playwright.sync_api import sync_playwright
with sync_playwright() as p:
    b = p.chromium.launch()
    b.close()' >/dev/null 2>&1 || { log "Playwright, PyYAML or Chromium does not start (run without --check to install)"; return 1; }
	log "ready: $PY"
}

case "${1:-}" in
--check) check; exit $? ;;
"") ;;
*) die "usage: $0 [--check]" ;;
esac

command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1 || die "python3 is required"
command -v ffmpeg >/dev/null 2>&1 || die "ffmpeg is required on PATH (the clips are encoded with it)"
mkdir -p "$HOME_DIR"
[ -x "$PY" ] || "$(command -v python3 || command -v python)" -m venv "$VENV"
"$PY" -m pip install --quiet --requirement "$HERE/requirements.txt"
"$PY" -m playwright install chromium
log "installed: $PY"
