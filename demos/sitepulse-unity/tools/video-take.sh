#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# video-take.sh -- ONE live take for the Sitepulse feature video, from WSL.
#
# It launches the Windows IL2CPP player in Live mode with -sitepulse-video-run against the running environment (tools/live-env.sh up).
# The player does the take's presenter part itself, once every device is observed: the fleet works for 150 s (the establishing
# shots), the low-fuel cycle on SP-HL-0006 (the platform's rule sends the refuel command), a puncture on SP-HL-0003 (the tyre rule
# raises its alarm and sends nothing), then it waits for the operator's goto-area sp-zone-yard to SP-HL-0003 and closes after a short tail.
# Roughly 12 to 20 minutes. The take is RECORDED (on by default): render it with tools/shots/*.json afterwards (see below).
#
#   video-take.sh                        the player does everything but the operator's command, which YOU send from the console
#                                        (device page, Commands panel: Go To Area, areaToken sp-zone-yard, to SP-HL-0003) when the
#                                        player's log says "operator" -- that is the console part you record
#   video-take.sh --unattended           ... and this script sends that command through the operator plane when the player says it is time
#   video-take.sh --out DIR              evidence and recordings under DIR (default Build/video-take/<stamp>)
#   video-take.sh --timeout-min N        stop the player if it is still running after N minutes (default 40)
#
# Afterwards:  <out>/recordings/<run id>/   the recording (run.json, sim.bin, observed/device/presenter.ndjson, video-run.json)
#              <out>/player.filtered.log    the player's [sitepulse] lines, redacted
# Render:      Build/Sitepulse.exe -force-d3d11 -sitepulse-render <shots.json> -sitepulse-replay <run id> -sitepulse-record <out>/recordings
#                  -sitepulse-out <frames dir> -screen-fullscreen 0 -screen-width 1920 -screen-height 1080 -no-intro   (Windows paths)
#              tools/shots/sitepulse-video.json (16:9), sitepulse-video-9x16.json (use -screen-width 1080 -screen-height 1920),
#              sitepulse-website-loop.json; then tools/frames-to-mp4.sh <frames dir>
#
# Environment: SP_HOME (default ~/sitepulse-env) for the broker CA; DC_VERSION (the platform version the environment was installed at,
# recorded in the recording's header); RUNNER (default http://127.0.0.1:8090).
#
# Exit status: the player's own (0 only when every event of the take was seen), 1 for a refused start.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="$(cd "$HERE/.." && pwd)"
LIVE_ENV="$HERE/live-env.sh"
PLAYER="$PROJECT/Build/Sitepulse.exe"

SP_HOME="${SP_HOME:-$HOME/sitepulse-env}"
DC_VERSION="${DC_VERSION:-0.19.0}"
RUNNER="${RUNNER:-http://127.0.0.1:8090}"
OUT=""
UNATTENDED=0
TIMEOUT_MIN=40

log() { printf '[video-take] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--unattended) UNATTENDED=1 ;;
	--out) OUT="${2:?--out needs a directory}"; shift ;;
	--timeout-min) TIMEOUT_MIN="${2:?--timeout-min needs minutes}"; shift ;;
	-h | --help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done
case "$TIMEOUT_MIN" in '' | *[!0-9]*) die "--timeout-min is a whole number of minutes" ;; esac

win() { wslpath -w "$1"; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required on PATH"; }
win_pids() { tasklist.exe /FI "IMAGENAME eq Sitepulse.exe" /FO CSV /NH 2>/dev/null | tr -d '\r' | awk -F'","' '/Sitepulse.exe/ {gsub(/"/,"",$2); print $2}' | sort -n; }

preflight() {
	need python3
	need curl
	command -v tasklist.exe >/dev/null 2>&1 || die "tasklist.exe is not reachable: this script drives a Windows player from WSL"
	command -v wslpath >/dev/null 2>&1 || die "wslpath is required"
	[ -f "$PLAYER" ] || die "no player at $PLAYER: build it first (phase-a-acceptance.sh --build editor|batch builds it)"
	"$LIVE_ENV" status >/dev/null || die "the environment is not fully up (run: $LIVE_ENV status)"
	[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$RUNNER/status")" = 200 ] || die "the runner does not answer $RUNNER/status"
	CA_SRC="${SP_CA_PEM:-$SP_HOME/nats-ca.pem}"
	[ -f "$CA_SRC" ] || die "the broker CA is missing: $CA_SRC (live-env.sh up writes it)"
	local running
	running="$(win_pids | tr '\n' ' ')"
	[ -z "$running" ] || die "a Sitepulse.exe is already running (pid $running): two players with the same device client IDs evict each other. Close it first"
}

PLAYER_PID=""
PLAYER_JOB=""

launch_player() {
	local before after pid _
	before="$(win_pids | tr '\n' ' ')"
	"$PLAYER" -sitepulse-mode live -sitepulse-video-run \
		-sitepulse-record "$(win "$OUT/recordings")" \
		-dc-runner "$RUNNER" -dc-ca "$(win "$OUT/ca.pem")" \
		-sitepulse-platform-version "$DC_VERSION" \
		-logFile "$(win "$RAW")" \
		-screen-fullscreen 0 -screen-width 1920 -screen-height 1080 \
		-no-intro >/dev/null 2>&1 &
	PLAYER_JOB=$!
	for _ in $(seq 1 20); do
		sleep 1
		after="$(win_pids | tr '\n' ' ')"
		for pid in $after; do
			case " $before " in *" $pid "*) ;; *) PLAYER_PID="$pid" ;; esac
		done
		[ -z "$PLAYER_PID" ] || break
	done
	[ -n "$PLAYER_PID" ] || die "the player did not start"
	log "player started (windows pid $PLAYER_PID); the recording goes to $OUT/recordings"
}

player_alive() { [ -n "$PLAYER_PID" ] && grep -qx "$PLAYER_PID" <<<"$(win_pids)"; }

main() {
	preflight
	[ -n "$OUT" ] || OUT="$PROJECT/Build/video-take/$(date -u +%Y%m%dT%H%M%SZ)"
	mkdir -p "$OUT/recordings" "$OUT/.raw"
	cp "$CA_SRC" "$OUT/ca.pem"   # a public certificate
	RAW="$OUT/.raw/player.log"
	log "out: $OUT"
	if [ "$UNATTENDED" = 1 ]; then
		log "unattended: this script sends the operator's goto-area when the player says it is time"
	else
		log "attended: when the player's log says 'operator', send goto-area sp-zone-yard to SP-HL-0003 from the console and record that screen"
	fi

	launch_player

	local operator_job=""
	if [ "$UNATTENDED" = 1 ]; then
		python3 "$HERE/video_take.py" operator --runner "$RUNNER" --log "$RAW" >"$OUT/operator.txt" 2>&1 &
		operator_job=$!
	fi

	local waited=0 limit=$((TIMEOUT_MIN * 60))
	while player_alive; do
		if [ "$waited" -ge "$limit" ]; then
			log "the player is still running after ${TIMEOUT_MIN} min: closing the process this script started (pid $PLAYER_PID)"
			taskkill.exe /F /PID "$PLAYER_PID" >/dev/null 2>&1 || true
			break
		fi
		sleep 5
		waited=$((waited + 5))
	done
	local rc=0
	wait "$PLAYER_JOB" 2>/dev/null || rc=$?
	if [ -n "$operator_job" ]; then
		kill "$operator_job" 2>/dev/null || true
		wait "$operator_job" 2>/dev/null || true
		log "the operator's part: $(tr '\n' ' ' <"$OUT/operator.txt")"
	fi

	# the raw log is reduced to its [sitepulse] lines, redacted, and deleted
	if [ -f "$RAW" ]; then
		python3 -c '
import sys
sys.path.insert(0, sys.argv[1])
import phase_a_check as c
text = open(sys.argv[2], encoding="utf-8", errors="replace").read()
open(sys.argv[3], "w", encoding="utf-8").write(c.filter_log(text))
' "$HERE" "$RAW" "$OUT/player.filtered.log"
		rm -f "$RAW"
	fi

	local run
	run="$(find "$OUT/recordings" -mindepth 1 -maxdepth 1 -type d -name 'run-*' | sort | tail -n 1)"
	[ -n "$run" ] || die "the player left no recording under $OUT/recordings (see $OUT/player.filtered.log)"
	log "recording: $run"
	if [ -f "$run/video-run.json" ]; then log "the take: $(tr -s ' \n' ' ' <"$run/video-run.json")"; fi
	log "player exit code $rc (0: every event of the take was seen)"
	exit "$rc"
}

main
