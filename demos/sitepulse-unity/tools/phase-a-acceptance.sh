#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# phase-a-acceptance.sh -- the Sitepulse Phase A acceptance, from WSL.
#
# It launches the Windows IL2CPP player in Live mode (-sitepulse-acceptance phaseA) against the running
# environment (tools/live-env.sh up), and while the player's own probe measures what only the player can
# see, phase_a_check.py judges the PLATFORM's side: every emitted sample stored (matched by device and
# occurredTime), the commands (console-style goto-area, a superseding pair, the platform rule's own
# goto-refuel on SP-HL-0006, a rejected area, the plant), the device log against the platform's outcome,
# and that SP-HL-0006's stored fuel rises only while the refuel command was in flight.
#
#   phase-a-acceptance.sh                       the acceptance run
#   phase-a-acceptance.sh --controls            ... and then every negative control, each its own relaunch
#   phase-a-acceptance.sh --controls-only       only the controls
#   phase-a-acceptance.sh --build editor|batch  build the player first (see below)
#
# Options:
#   --build none|editor|batch   none (default): use Build/Sitepulse.exe as it is (its build-info.json must name
#                               the current commit, or the run fails). editor: build through an Editor that is
#                               open, by the eval runner named in $UNITY_EVAL (called as: $UNITY_EVAL <file> <s>).
#                               batch: Unity.exe -batchmode (the Editor must be closed; $UNITY_EXE).
#   --out DIR                   evidence root (default: Build/acceptance, ignored by git)
#   --screenshot-after SECONDS  when the player saves its picture (default 55)
#   --allow-stale-build         do not fail on a build older than HEAD (it is still recorded)
#
# Environment: SP_HOME (default ~/sitepulse-env) for the broker CA; DC_VERSION (the platform version the
# environment was installed at, recorded in the bundle); RUNNER (default http://127.0.0.1:8090).
#
# The runner's operator token is read by phase_a_check.py into a variable and nowhere else: it is never
# printed, logged or stored. Nothing in the evidence bundle holds a device credential: the player's log is
# reduced to its [sitepulse] lines and passed through the player's own redaction rules, and the bundle is
# scanned for credential and token shapes before it is called done.
#
# Exit status: 0 only if every item passed (and, with --controls, every control failed the right way).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="$(cd "$HERE/.." && pwd)"
REPO="$(git -C "$PROJECT" rev-parse --show-toplevel)"
LIVE_ENV="$HERE/live-env.sh"
CHECK="$HERE/phase_a_check.py"

SP_HOME="${SP_HOME:-$HOME/sitepulse-env}"
DC_VERSION="${DC_VERSION:-0.19.0}"
RUNNER="${RUNNER:-http://127.0.0.1:8090}"
PLAYER="$PROJECT/Build/Sitepulse.exe"
BUILD=none
OUT_ROOT="$PROJECT/Build/acceptance"
SHOT_AFTER=55
ALLOW_STALE=0
DO_RUN=1
DO_CONTROLS=0
UNITY_EXE="${UNITY_EXE:-/mnt/c/Program Files/Unity/Hub/Editor/6000.5.3f1/Editor/Unity.exe}"

# the controls, in the order they run: the faulted device is named where the control needs one
CONTROLS=("bogus-binding:SP-HL-0004" "wrong-ca" "bad-credential:SP-DZ-0002" "unknown-command" "runner-stop")

log() { printf '[phase-a] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--controls) DO_CONTROLS=1 ;;
	--controls-only) DO_CONTROLS=1 DO_RUN=0 ;;
	--build) BUILD="${2:?--build needs none, editor or batch}"; shift ;;
	--out) OUT_ROOT="${2:?--out needs a directory}"; shift ;;
	--screenshot-after) SHOT_AFTER="${2:?needs seconds}"; shift ;;
	--allow-stale-build) ALLOW_STALE=1 ;;
	-h | --help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done

win() { wslpath -w "$1"; }
now_iso() { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required on PATH"; }

EVID=""
item() { # item PASS|FAIL|info id description [detail]
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "${4:-}" >>"$EVID/script-items.tsv"
	log "$1  $2: ${4:-$3}"
}

# ---------------------------------------------------------------------------
# preflight: refuse to start on anything that would make the run mean nothing
# ---------------------------------------------------------------------------

win_pids() { tasklist.exe /FI "IMAGENAME eq Sitepulse.exe" /FO CSV /NH 2>/dev/null | tr -d '\r' | awk -F'","' '/Sitepulse.exe/ {gsub(/"/,"",$2); print $2}' | sort -n; }

preflight() {
	need python3
	need openssl
	need curl
	command -v tasklist.exe >/dev/null 2>&1 || die "tasklist.exe is not reachable: this script drives a Windows player from WSL"
	command -v wslpath >/dev/null 2>&1 || die "wslpath is required"
	"$LIVE_ENV" status >/dev/null || die "the environment is not fully up (run: $LIVE_ENV status)"
	[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$RUNNER/status")" = 200 ] || die "the runner does not answer $RUNNER/status"
	CA_SRC="${SP_CA_PEM:-$SP_HOME/nats-ca.pem}"
	[ -f "$CA_SRC" ] || die "the broker CA is missing: $CA_SRC (live-env.sh up writes it)"
	openssl x509 -in "$CA_SRC" -noout >/dev/null 2>&1 || die "$CA_SRC is not a certificate"
	[ -z "$(win_pids)" ] || die "a Sitepulse.exe is already running (pid $(win_pids | tr '\n' ' ')): two players with the same device client IDs evict each other. Close it first"
}

# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------

build_player() {
	case "$BUILD" in
	none) return ;;
	editor)
		[ -n "${UNITY_EVAL:-}" ] || {
			cat >&2 <<EOF
--build editor needs \$UNITY_EVAL: a command that runs a C# file from $PROJECT/EditorScratch in the open Editor,
called as  \$UNITY_EVAL phaseA_build.cs <timeout-seconds>.  The file is already there
(EditorScratch/phaseA_build.cs); it calls DeviceChain.Sitepulse.EditorTools.BuildPlayer.Run() and writes
EditorScratch/phaseA_build.result.txt ("OK ..." or "FAILED ...").
EOF
			exit 2
		}
		mkdir -p "$PROJECT/EditorScratch"
		cp "$HERE/phase-a-build.eval.cs" "$PROJECT/EditorScratch/phaseA_build.cs"
		rm -f "$PROJECT/EditorScratch/phaseA_build.result.txt"
		log "building through the open Editor (an IL2CPP build takes several minutes)"
		"$UNITY_EVAL" phaseA_build.cs 60 >/dev/null || true
		local _
		for _ in $(seq 1 480); do
			sleep 5
			grep -q '^RUNNING' "$PROJECT/EditorScratch/phaseA_build.result.txt" 2>/dev/null || break
		done
		grep -q '^OK' "$PROJECT/EditorScratch/phaseA_build.result.txt" 2>/dev/null ||
			die "the Editor build did not succeed: $(head -c 400 "$PROJECT/EditorScratch/phaseA_build.result.txt" 2>/dev/null)"
		;;
	batch)
		[ -f "$UNITY_EXE" ] || die "UNITY_EXE=$UNITY_EXE does not exist"
		[ ! -e "$PROJECT/Temp/UnityLockfile" ] || die "an Editor holds this project (Temp/UnityLockfile): close it, or use --build editor"
		log "building with Unity -batchmode"
		"$UNITY_EXE" -batchmode -quit -nographics -projectPath "$(win "$PROJECT")" \
			-executeMethod DeviceChain.Sitepulse.EditorTools.BuildPlayer.Windows64Il2Cpp \
			-logFile "$(win "$PROJECT/Build/build.log")" || die "the batch build failed; see Build/build.log"
		;;
	*) die "--build is none, editor or batch" ;;
	esac
	stamp_build_info
}

# The Editor runs on Windows and cannot resolve this worktree's git metadata (its .git is a file pointing into WSL),
# so the build is stamped from here, where git works: the commit, whether the TRACKED tree is clean (untracked files
# do not count) and the C# SDK's last commit, each commit as 12 characters. Unity's own fields stay as written.
stamp_build_info() {
	local info="$PROJECT/Build/build-info.json" clean=true
	[ -f "$info" ] || die "the build left no $info"
	[ -z "$(git -C "$PROJECT" status --porcelain --untracked-files=no)" ] || clean=false
	python3 "$CHECK" stamp-build --info "$info" \
		--commit "$(git -C "$PROJECT" rev-parse HEAD)" --clean "$clean" \
		--sdk "$(git -C "$PROJECT" log -1 --format=%H -- "$REPO/sdks/csharp")" || die "could not stamp $info"
	log "stamped the build: commit $(git -C "$PROJECT" rev-parse --short=12 HEAD), tracked tree clean: $clean"
}

# ---------------------------------------------------------------------------
# launching a player
# ---------------------------------------------------------------------------

RAW=""          # where the current player writes its raw log
PLAYER_PID=""   # the Windows pid, for the one kill this script ever does
PLAYER_JOB=""   # the WSL-side job, for its exit code

# launch_player <dir> <ca-file> [extra player flags...]
launch_player() {
	local dir="$1" ca="$2"
	shift 2
	local before after pid _
	before="$(win_pids | tr '\n' ' ')"
	"$PLAYER" -sitepulse-mode live -sitepulse-acceptance phaseA \
		-sitepulse-acceptance-dir "$(win "$dir")" \
		-dc-runner "$RUNNER" -dc-ca "$(win "$ca")" \
		-logFile "$(win "$RAW")" \
		-screen-fullscreen 0 \
		-sitepulse-screenshot "$(win "$dir/screenshot.png")" -sitepulse-screenshot-after "$SHOT_AFTER" \
		-no-intro "$@" >/dev/null 2>&1 &
	PLAYER_JOB=$!
	PLAYER_PID=""
	for _ in $(seq 1 20); do
		sleep 1
		after="$(win_pids | tr '\n' ' ')"
		for pid in $after; do
			case " $before " in *" $pid "*) ;; *) PLAYER_PID="$pid" ;; esac
		done
		[ -z "$PLAYER_PID" ] || break
	done
	[ -n "$PLAYER_PID" ] || die "the player did not start"
	log "player started (windows pid $PLAYER_PID) -> $dir"
}

player_alive() { [ -n "$PLAYER_PID" ] && grep -qx "$PLAYER_PID" <<<"$(win_pids)"; }

# wait_player <seconds>: waits for the player to exit; kills ONLY the pid this script started if it will not.
# Sets PLAYER_EXIT (the player's own exit code, or 'killed').
wait_player() {
	local secs="$1" _
	PLAYER_EXIT=killed
	for _ in $(seq 1 "$secs"); do
		player_alive || {
			wait "$PLAYER_JOB" 2>/dev/null && PLAYER_EXIT=0 || PLAYER_EXIT=$?
			return 0
		}
		sleep 1
	done
	log "the player did not exit within ${secs}s: closing the process this script started (pid $PLAYER_PID)"
	taskkill.exe /F /PID "$PLAYER_PID" >/dev/null 2>&1 || true
	wait "$PLAYER_JOB" 2>/dev/null || true
	return 1
}

# stage_log <dir>: reduce the raw player log to its [sitepulse] lines, redacted; delete the raw one
stage_log() {
	local dir="$1"
	if [ -f "$RAW" ]; then
		python3 -c '
import sys
sys.path.insert(0, sys.argv[1])
import phase_a_check as c
text = open(sys.argv[2], encoding="utf-8", errors="replace").read()
open(sys.argv[3], "w", encoding="utf-8").write(c.filter_log(text))
' "$HERE" "$RAW" "$dir/player.filtered.log"
		rm -f "$RAW"
	fi
}

# run_with_checker <dir> <since> <checker args...>: the checker in the background, the player watched meanwhile
run_with_checker() {
	local dir="$1" since="$2"
	shift 2
	python3 "$CHECK" "$@" --dir "$dir" --runner "$RUNNER" --since "$since" --live-env "$LIVE_ENV" --out "$dir" &
	local checker=$! quiet=0
	while kill -0 "$checker" 2>/dev/null; do
		sleep 3
		# a player that ended before it was told to, and left no final verdict, crashed: stop waiting for it
		if ! player_alive && [ ! -e "$dir/phaseA-finish" ] && ! grep -q '"final": true' "$dir/phaseA-result.json" 2>/dev/null; then
			quiet=$((quiet + 1))
			if [ "$quiet" -ge 5 ]; then
				log "the player is gone and left no verdict: it crashed"
				kill "$checker" 2>/dev/null || true
				wait "$checker" 2>/dev/null || true
				CHECKER_RC=1
				PLAYER_CRASHED=1
				return
			fi
		fi
	done
	wait "$checker" && CHECKER_RC=0 || CHECKER_RC=$?
}

# ---------------------------------------------------------------------------
# one launch: the acceptance run, or a control
# ---------------------------------------------------------------------------

# execute <dir> <label> <ca-file> <checker-mode> [extra player flags...]
execute() {
	local dir="$1" label="$2" ca="$3" mode="$4"
	shift 4
	mkdir -p "$dir" "$OUT_ROOT/.raw"
	# the raw log is kept OUTSIDE the bundle and deleted once its redacted [sitepulse] lines are staged
	RAW="$OUT_ROOT/.raw/$(basename "$EVID")-${label//[:\/]/_}.log"
	local since
	since="$(date -u -d '-5 seconds' +%Y-%m-%dT%H:%M:%S.%3NZ)"
	CHECKER_RC=0
	PLAYER_CRASHED=0
	launch_player "$dir" "$ca" "$@"
	if [ "$mode" = run ]; then run_with_checker "$dir" "$since" run
	else run_with_checker "$dir" "$since" control --control "$label"; fi
	wait_player 120 || true
	stage_log "$dir"
	local verdict="the player's exit code is $PLAYER_EXIT"
	if [ "$PLAYER_CRASHED" = 1 ]; then item FAIL "player-$label" "the player ran to its verdict" "it crashed before leaving one; see $(basename "$dir")/player.filtered.log"; fi
	# the probe exits 0 when its checks (or, for a control, the fault failing the right way) passed
	if [ "$PLAYER_EXIT" = 0 ]; then item PASS "player-exit-$label" "the player exits 0 (its own probe passed)" "$verdict"
	else item FAIL "player-exit-$label" "the player exits 0 (its own probe passed)" "$verdict"; fi
	if [ -s "$dir/screenshot.png" ]; then item PASS "screenshot-$label" "the player saved a screenshot" "$(basename "$dir")/screenshot.png, $(stat -c %s "$dir/screenshot.png") bytes"
	else item FAIL "screenshot-$label" "the player saved a screenshot" "no screenshot.png"; fi
}

# ---------------------------------------------------------------------------
# the wrong CA: a self-signed certificate that is not the broker's
# ---------------------------------------------------------------------------

make_wrong_ca() {
	local out="$1" tmp
	tmp="$(mktemp -d)"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout "$tmp/key.pem" -out "$out" \
		-subj "/CN=sitepulse-acceptance-wrong-ca" -days 1 >/dev/null 2>&1
	rm -rf "$tmp"   # the key is not kept: nothing is ever signed with it
	openssl x509 -in "$out" -noout >/dev/null 2>&1 || die "could not generate the wrong CA"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

main() {
	preflight
	build_player

	local stamp
	stamp="$(date -u +%Y%m%dT%H%M%SZ)"
	EVID="$OUT_ROOT/phaseA-$stamp"
	mkdir -p "$EVID"
	: >"$EVID/script-items.tsv"
	log "evidence: $EVID"

	[ -f "$PLAYER" ] || die "no player at $PLAYER: build it first (--build editor|batch)"
	cp "$CA_SRC" "$EVID/ca.pem"   # a public certificate

	# ---- what was built, and is it this tree? --------------------------------------------------
	local head head12 verdict detail
	head="$(git -C "$PROJECT" rev-parse HEAD)"
	head12="$(git -C "$PROJECT" rev-parse --short=12 HEAD)"
	if [ -f "$PROJECT/Build/build-info.json" ]; then
		cp "$PROJECT/Build/build-info.json" "$EVID/build-info.json"
		local allow=()
		[ "$ALLOW_STALE" = 1 ] && allow=(--allow-stale)
		IFS=$'\t' read -r verdict detail < <(python3 "$CHECK" build-current --info "$EVID/build-info.json" --head "$head" "${allow[@]}")
		item "$verdict" build-current "the player was built from this commit with a clean tracked tree" "$detail"
	else
		item FAIL build-current "the player was built from this commit with a clean tracked tree" "Build/build-info.json is missing: the player was not built by BuildPlayer.Windows64Il2Cpp"
	fi

	# ---- the environment --------------------------------------------------------------------------
	local fp wsl_now win_now offset
	fp="$(openssl x509 -in "$CA_SRC" -noout -fingerprint -sha256 | sed 's/.*=//')"
	wsl_now="$(date -u +%s)"
	win_now="$(powershell.exe -NoProfile -Command '[DateTimeOffset]::UtcNow.ToUnixTimeSeconds()' 2>/dev/null | tr -d '\r' || true)"
	offset="n/a"
	[ -z "${win_now:-}" ] || offset=$((win_now - wsl_now))
	python3 - "$EVID/environment.json" "$DC_VERSION" "$fp" "$offset" "$RUNNER" "$head12" <<'PY'
import json, sys
path, version, fp, offset, runner, commit = sys.argv[1:7]
json.dump({"commit": commit, "platformVersion": version, "brokerCaSha256": fp, "windowsMinusWslClockSeconds": offset, "runner": runner}, open(path, "w"), indent=2)
PY
	item info environment "platform, CA and clocks recorded (environment.json)" "platform v$DC_VERSION, CA sha256 $fp, Windows clock minus WSL clock: ${offset}s"

	local failed=0

	if [ "$DO_RUN" = 1 ]; then
		execute "$EVID" run "$EVID/ca.pem" run || failed=1
		[ "$CHECKER_RC" = 0 ] || failed=1
	fi

	if [ "$DO_CONTROLS" = 1 ]; then
		local c label dir
		for c in "${CONTROLS[@]}"; do
			label="${c//:/-}"
			dir="$EVID/controls/$label"
			mkdir -p "$dir"
			log "---- control $c"
			case "$c" in
			unknown-command)
				python3 "$CHECK" unknown-command --dir "$dir" --runner "$RUNNER" --out "$dir" || failed=1
				;;
			wrong-ca)
				make_wrong_ca "$dir/wrong-ca.pem"
				execute "$dir" "$c" "$dir/wrong-ca.pem" control -sitepulse-control "$c" || failed=1
				[ "$CHECKER_RC" = 0 ] || failed=1
				;;
			*)
				execute "$dir" "$c" "$EVID/ca.pem" control -sitepulse-control "$c" || failed=1
				[ "$CHECKER_RC" = 0 ] || failed=1
				;;
			esac
		done
	fi

	# ---- the bundle ---------------------------------------------------------------------------------
	local scan=()
	while IFS= read -r f; do scan+=("$f"); done < <(find "$EVID" -type f \( -name 'phaseA-*' -o -name 'player.filtered.log' -o -name 'checker-*.json' -o -name 'SUMMARY-*.md' -o -name 'environment.json' \))
	python3 "$CHECK" leakscan --dir "$EVID" --out "$EVID" --files "${scan[@]}" || failed=1

	{
		echo "# Sitepulse Phase A acceptance"
		echo
		echo "- **Run**: $stamp"
		echo "- **Commit**: $head12"
		echo "- **Platform**: v$DC_VERSION; broker CA sha256 $fp"
		if [ -f "$EVID/build-info.json" ]; then
			python3 - "$EVID/build-info.json" <<'PY'
import json, sys
b = json.load(open(sys.argv[1]))
print("- **Player**: built from %s (tracked tree clean: %s); SDK commit %s; Unity %s; %s, stripping %s; built %s" % (
    b.get("gitSha"), b.get("trackedTreeClean"), b.get("sdkCommit"), b.get("unityVersion"),
    b.get("scriptingBackend"), b.get("strippingLevel"), b.get("buildFinishedAt")))
PY
		fi
		echo "- **Controls**: $([ "$DO_CONTROLS" = 1 ] && echo run || echo "not run (--controls)")"
	} >"$EVID/header.md"
	python3 "$CHECK" bundle --dir "$EVID" --header "$EVID/header.md" || failed=1
	rm -f "$EVID"/SUMMARY-*.md "$EVID"/controls/*/SUMMARY-*.md "$EVID/header.md"
	log "evidence bundle: $EVID (SUMMARY.md)"
	exit "$failed"
}

# sourced (by a test), it only defines its functions
if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
