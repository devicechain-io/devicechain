#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# console-recorder-setup.sh -- installs what console_capture.py needs, once, without sudo: a Python venv holding the pinned Playwright
# (console-recorder-requirements.txt) and the Chromium build that release pins (downloaded to ~/.cache/ms-playwright). ffmpeg must already
# be on PATH (it encodes the clips).
#
#   console-recorder-setup.sh           install (re-running is harmless)
#   console-recorder-setup.sh --check   say whether the recorder is ready; exit 0 only when it is
#
# The venv lives at ~/.cache/sitepulse-recorder/venv (override: SP_RECORDER_HOME). video_take.py and video-take.sh look for it there (or at
# $SP_RECORDER_PYTHON) and use the console recorder only when it exists.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RECORDER_HOME="${SP_RECORDER_HOME:-$HOME/.cache/sitepulse-recorder}"
VENV="$RECORDER_HOME/venv"

log() { printf '[console-recorder] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

check() {
	command -v ffmpeg >/dev/null 2>&1 || { log "ffmpeg is not on PATH"; return 1; }
	[ -x "$VENV/bin/python" ] || { log "no venv at $VENV"; return 1; }
	"$VENV/bin/python" -c 'from playwright.sync_api import sync_playwright
with sync_playwright() as p:
    b = p.chromium.launch()
    b.close()' >/dev/null 2>&1 || { log "Playwright or its Chromium does not start (run without --check to install)"; return 1; }
	log "ready: $VENV"
}

case "${1:-}" in
--check) check; exit $? ;;
"") ;;
*) die "usage: $0 [--check]" ;;
esac

command -v python3 >/dev/null 2>&1 || die "python3 is required"
command -v ffmpeg >/dev/null 2>&1 || die "ffmpeg is required on PATH (the clips are encoded with it)"
mkdir -p "$RECORDER_HOME"
[ -x "$VENV/bin/python" ] || python3 -m venv "$VENV"
"$VENV/bin/python" -m pip install --quiet --requirement "$HERE/console-recorder-requirements.txt"
"$VENV/bin/python" -m playwright install chromium
check
