#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# acceptance.sh -- the Towerpulse acceptance, from Git Bash on Windows, against a kind cluster that is already up.
#
#   acceptance.sh                         the control run, then the rule-disabled run (each on a FRESH sim)
#   acceptance.sh control                 only the control run
#   acceptance.sh rule-disabled           only the rule-disabled run
#   acceptance.sh hot                     only segment A (the hot room) on a fresh sim; a take whose first run missed a platform fact is re-recorded with this
#   acceptance.sh chiller                 only segment B (the annex chiller beat) on a fresh sim, with the calculated-metric check
#   acceptance.sh --build editor          build the Windows player first through the open Editor (needs $UNITY_EVAL, see below)
#
# What a run does. It provisions a brand-new sim (`dcctl sim create --manifest towerpulse`, so a fresh tenant: stale alarms and commands from an earlier
# take cannot carry over), starts its simulator with --external-device-plane (provisioning and converging only: the simulator publishes nothing, the
# player is the ONE writer of the 31 devices), then runs the player for the scripted day on the WALL clock and records it:
#
#   segment A  13:54 on the model clock: the 14:00 meeting heats F4-East; the platform's rule R1 raises the alarm and sends boost-cooling; the room cools;
#              the command is SUCCESSFUL only when the zone is at setpoint; the alarm clears.
#   segment B  16:25: the annex chiller trips; rule R4 raises the absence alarm; the operator's `restart` goes through the API; it is SUCCESSFUL; it reports again.
#
# The rule-disabled run is the SAME seed and the same day with R1 disabled (draft + publish) before the player starts. It must give NO alarm and NO
# boost while the zone still overheats: the platform, not the script, closed the loop in the control run. Only segment A runs (R4 is untouched).
#
# The chiller COP and its one honest gap. When a restart is accepted the chiller is online again but has not started: its first reading carries power_kw 0
# and cooling_kw 0, which is what the plant really draws then. The platform's cop (cooling_kw / power_kw) is 0/0 for that reading, which is not a number, so
# it is SKIPPED as non_finite and no cop is stored for it: nothing is faked, and the plane keeps sending the true zeros. The check therefore requires every
# non_finite skip to be explained by a power 0 / cooling 0 reading the plane really sent (count_power_zero over the recording), and missing_input to be 0.
# The chart plots one-minute averages of the stored cop, so the neighbouring readings keep that minute's point; a minute with NO stored cop would be a gap
# (the chart does not connect nulls), never an invented value.
#
# acceptance_check.py judges the platform's side from the platform's own records (alarms, commands, stored telemetry, the broker's connections, the
# calculated-metric counters) and the annex owner's sign-in. The sim is destroyed afterwards. Evidence goes to Build/acceptance/<stamp>-<mode>/ (ignored by git).
#
# Environment:
#   KUBECONFIG      the cluster's kubeconfig             (required; the current context is never changed)
#   TP_INSTANCE     instance id            (default tp)         TP_API  http host:port of the ingress (default localhost:18080)
#   TP_MQTT         the broker devices dial (default ssl://localhost:11883)
#   TP_INGRESS      the device HTTP ingress (default http://localhost:18081; a port-forward to svc/event-sources:8081 is made if nothing answers)
#   TP_TIER         an EXISTING tenant tier to use instead (default: unset; `sim create` makes and raises its own tp-showcase tier,
#                   the one place calculated metrics are on, and the platform's tiers stay as they are)
#   DCCTL, SIMULATOR   the binaries. Unset (the default), both are BUILT from this tree into Build/bin/ at the start of every run. A binary you name is
#                      used only if binary_fresh.py finds it newer than every source it is built from; a stale one is refused, because a take
#                      recorded by a simulator older than the tree shows a platform the tree no longer is.
#   TP_START_A, TP_START_B   model start hours (default 13.9 and 16.43)
#   TP_LOOP         1: start the player with -tp-loop, so the 90-second storyboard repeats and the alarm and command cards show whenever the platform raises them
#                   (a live run only reaches them minutes in, after one pass has ended on a card-less pull-back)
#   TP_HOOK         optional: an executable called as `$TP_HOOK <sim> <evidence-dir> <since>` in the control run, after the owner check, while the player is still up
#   UNITY_EVAL      for --build editor: a command called as `$UNITY_EVAL tp_build.cs <seconds>` that evaluates EditorScratch/tp_build.cs in the open Editor
#
# Credentials are read by dcctl, the simulator, the player and acceptance_check.py themselves; this script never reads, prints or stores one.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="$(cd "$HERE/.." && pwd)"
REPO="$(git -C "$PROJECT" rev-parse --show-toplevel)"
CHECK="$HERE/acceptance_check.py"

INSTANCE="${TP_INSTANCE:-tp}"
API="${TP_API:-localhost:18080}"
MQTT="${TP_MQTT:-ssl://localhost:11883}"
INGRESS="${TP_INGRESS:-http://localhost:18081}"
TIER="${TP_TIER:-}"
START_A="${TP_START_A:-13.9}"
START_B="${TP_START_B:-16.43}"
NS="dci-${INSTANCE}"
RUNNER="http://127.0.0.1:8090"
PLAYER="$PROJECT/Build/Towerpulse.exe"
DCCTL="${DCCTL:-}"
SIMULATOR="${SIMULATOR:-}"
BIN_DIR="$PROJECT/Build/bin"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_ROOT="$PROJECT/Build/acceptance"
BUILD=none
MODES=()

log() { printf '[acceptance] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }
win() { cygpath -m "$1"; }
now_iso() { date -u +%Y-%m-%dT%H:%M:%S.000Z; }

while [ $# -gt 0 ]; do
	case "$1" in
	control | rule-disabled | chiller | hot) MODES+=("$1") ;;
	--build) BUILD="${2:?--build needs none or editor}"; shift ;;
	--out) OUT_ROOT="${2:?--out needs a directory}"; shift ;;
	-h | --help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown argument $1 (see --help)" ;;
	esac
	shift
done
[ ${#MODES[@]} -gt 0 ] || MODES=(control rule-disabled)

# ---------------------------------------------------------------------------
# the binaries: built from this tree, or proven newer than it
# ---------------------------------------------------------------------------
# dcctl embeds the chart and the operator manifests (deploy/) and links core; dc-simulator links core. Either's sources changing makes a binary stale.
prepare_binaries() {
	command -v go >/dev/null || die "go is required to build dcctl and dc-simulator from this tree"
	mkdir -p "$BIN_DIR"
	if [ -z "$DCCTL" ]; then
		make -C "$REPO/backend/cli" build BUILDDIR="$BIN_DIR" >/dev/null || die "could not build dcctl from this tree"
		DCCTL="$BIN_DIR/dcctl.exe"
	fi
	if [ -z "$SIMULATOR" ]; then
		# not `make build`: its output has no .exe suffix, which dcctl cannot launch on Windows
		(cd "$REPO/backend/sims/dc-simulator" && go build -o "$BIN_DIR/dc-simulator.exe" .) || die "could not build dc-simulator from this tree"
		SIMULATOR="$BIN_DIR/dc-simulator.exe"
	fi
	[ -f "$DCCTL" ] || [ -f "$DCCTL.exe" ] || die "dcctl not found at $DCCTL"
	local d s
	d="$DCCTL"; [ -f "$d" ] || d="$DCCTL.exe"
	s="$SIMULATOR"; [ -f "$s" ] || s="$SIMULATOR.exe"
	[ -f "$s" ] || die "dc-simulator not found at $SIMULATOR"
	"$PY" -I "$HERE/binary_fresh.py" "$d" "$REPO/backend/cli" "$REPO/backend/core" "$REPO/deploy" || die "dcctl is stale: unset DCCTL to build it from this tree"
	"$PY" -I "$HERE/binary_fresh.py" "$s" "$REPO/backend/sims/dc-simulator" "$REPO/backend/core" || die "dc-simulator is stale: unset SIMULATOR to build it from this tree"
	log "binaries current with the tree: $(basename "$d"), $(basename "$s")"
}

# ---------------------------------------------------------------------------
# preflight
# ---------------------------------------------------------------------------
preflight() {
	[ -n "${KUBECONFIG:-}" ] || die "set KUBECONFIG to the Towerpulse cluster's kubeconfig (the current context is never used)"
	command -v kubectl >/dev/null || die "kubectl is required"
	command -v python >/dev/null || command -v python3 >/dev/null || die "python is required"
	PY="$(command -v python3 || command -v python)"
	prepare_binaries
	TP_KUBECONFIG="$KUBECONFIG" "$HERE/live-env.sh" status >/dev/null || die "the cluster is not fully up (run tools/live-env.sh status)"
	[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$RUNNER/status" || true)" != 200 ] || die "something already answers $RUNNER: stop that sim's runner first"
	[ -z "$(player_pids)" ] || die "a Towerpulse player is already running (pid $(player_pids | tr '\n' ' ')): two players with the same client ids evict each other"
}

player_pids() { tasklist.exe /FI "IMAGENAME eq Towerpulse.exe" /FO CSV /NH 2>/dev/null | tr -d '\r' | awk -F'","' '/Towerpulse.exe/ {gsub(/"/,"",$2); print $2}' | sort -n; }

# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------
stamp_build() {
	local info="$PROJECT/Build/build-info.json"
	[ -f "$info" ] || die "the build left no $info"
	local clean=true
	[ -z "$(git -C "$PROJECT" status --porcelain --untracked-files=no)" ] || clean=false
	"$PY" -I - "$info" "$(git -C "$PROJECT" rev-parse HEAD)" "$clean" "$(git -C "$PROJECT" log -1 --format=%H -- "$REPO/sdks/csharp")" <<'EOF'
import json, sys
p, commit, clean, sdk = sys.argv[1:5]
d = json.load(open(p))
d["gitSha"], d["trackedTreeClean"], d["sdkCommit"] = commit[:12], clean == "true", sdk[:12]
json.dump(d, open(p, "w"), indent=2)
EOF
	log "build stamped: commit $(git -C "$PROJECT" rev-parse --short=12 HEAD), tracked tree clean: $clean"
}

build_player() {
	case "$BUILD" in
	none) ;;
	editor)
		[ -n "${UNITY_EVAL:-}" ] || die "--build editor needs \$UNITY_EVAL"
		rm -f "$PROJECT/EditorScratch/tp_build.result.txt"
		"$UNITY_EVAL" tp_build.cs 60 >/dev/null || true
		local _
		for _ in $(seq 1 360); do
			sleep 5
			grep -q '^RUNNING' "$PROJECT/EditorScratch/tp_build.result.txt" 2>/dev/null || break
		done
		grep -q '^OK' "$PROJECT/EditorScratch/tp_build.result.txt" 2>/dev/null || die "the Editor build did not succeed"
		;;
	*) die "--build is none or editor" ;;
	esac
	[ -f "$PLAYER" ] || die "$PLAYER does not exist: build it (--build editor)"
	stamp_build
}

# ---------------------------------------------------------------------------
# one run
# ---------------------------------------------------------------------------
PF_PID="" SIM_NAME="" PLAYER_PID=""

kill_pid() { [ -z "${1:-}" ] || taskkill.exe //PID "$1" //T //F >/dev/null 2>&1 || true; }

# `$!` is the MSYS pid of a background job, which taskkill does not know: the WINDOWS pid of what this script started is found from its command line.
# Only an exact pid is ever ended, never a process by name.
winpid_of() { powershell.exe -NoProfile -Command "(Get-CimInstance Win32_Process | Where-Object { \$_.Name -eq '$1' -and \$_.CommandLine -like '*$2*' } | Select-Object -First 1).ProcessId" | tr -d '\r'; }

teardown() {
	log "teardown: sim ${SIM_NAME:-none}"
	[ -z "$PLAYER_PID" ] || kill_pid "$PLAYER_PID"
	[ -z "$SIM_NAME" ] || kill_pid "$(winpid_of dcctl.exe "sim run $SIM_NAME")"
	[ -z "$SIM_NAME" ] || "$DCCTL" sim destroy "$SIM_NAME" -i "$INSTANCE" --server "$API" >/dev/null 2>&1 || log "WARNING: sim destroy $SIM_NAME failed: destroy it by hand"
	[ -z "$PF_PID" ] || kill_pid "$(winpid_of kubectl.exe 'port-forward svc/event-sources')"
	SIM_NAME="" PF_PID="" PLAYER_PID=""
}
trap teardown EXIT

launch_player() { # launch_player <hour> <label> <record-dir> <quit-file> <log-file> <ca>
	local hour="$1" label="$2" rec="$3" quit="$4" logf="$5" ca="$6"
	rm -f "$quit"
	PLAYER_PID="$(powershell.exe -NoProfile -Command "(Start-Process -FilePath '$(win "$PLAYER")' -PassThru -ArgumentList '-screen-width','960','-screen-height','540','-screen-fullscreen','0','-dc-runner','$RUNNER','-dc-ca','$(win "$ca")','-tp-start-hour','$hour','-tp-seed','1','-tp-record','$(win "$rec")','-tp-label','$label','-tp-quit-file','$(win "$quit")','-logFile','$(win "$logf")'${TP_LOOP:+,'-tp-loop'}).Id" | tr -d '\r')"
	[[ "$PLAYER_PID" =~ ^[0-9]+$ ]] || die "could not start the player"
	log "player pid $PLAYER_PID ($label from hour $hour)"
}

end_player() { # end_player <quit-file>
	echo "done" >"$1"
	local _ alive
	for _ in $(seq 1 40); do
		sleep 3
		alive="$(tasklist.exe /FI "PID eq $PLAYER_PID" 2>/dev/null || true)"
		case "$alive" in *"$PLAYER_PID"*) ;; *) PLAYER_PID=""; return 0 ;; esac
	done
	log "WARNING: the player did not close itself within two minutes; ending pid $PLAYER_PID"
	kill_pid "$PLAYER_PID"
	PLAYER_PID=""
}

latest_run() { find "$1" -maxdepth 1 -type d -name 'run-*' | sort | tail -1; }

run_mode() {
	local mode="$1" ev short
	ev="$OUT_ROOT/$STAMP-$mode"
	case "$mode" in control) short=c ;; hot) short=h ;; *) short=r ;; esac
	SIM_NAME="tp-acc-$short-$(date -u +%m%d%H%M)"
	[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$RUNNER/status" || true)" != 200 ] || die "something still answers $RUNNER: the previous sim's runner was not stopped"
	mkdir -p "$ev/recordings" "$ev/logs"
	log "=== $mode run on a fresh sim ($SIM_NAME); evidence in $ev"

	if ! curl -s -o /dev/null --max-time 3 "$INGRESS/"; then
		kubectl -n "$NS" port-forward svc/event-sources "${INGRESS##*:}:8081" >"$ev/logs/port-forward.log" 2>&1 &
		PF_PID=$!
		sleep 3
	fi
	"$DCCTL" sim create "$SIM_NAME" --manifest towerpulse ${TIER:+--tier "$TIER"} --seed 1 -i "$INSTANCE" --server "$API" --ingress "$INGRESS" \
		--mqtt-broker "$MQTT" --mqtt-insecure=false >"$ev/logs/sim-create.log" 2>&1 || { tail -5 "$ev/logs/sim-create.log" >&2; die "sim create failed"; }
	DC_SIM_EXTERNAL_DEVICE_PLANE=1 "$DCCTL" sim run "$SIM_NAME" -i "$INSTANCE" --server "$API" --simulator "$SIMULATOR" >"$ev/logs/simulator.log" 2>&1 &
	"$PY" -I "$CHECK" ready --runner "$RUNNER" >"$ev/logs/ready.json" || { tail -5 "$ev/logs/simulator.log" >&2; die "the sim did not converge"; }
	TP_KUBECONFIG="$KUBECONFIG" "$HERE/live-env.sh" ca "$ev/broker-ca.pem" >/dev/null
	local ca="$ev/broker-ca.pem"
	"$PY" -I "$CHECK" counters --out "$ev" --name before --namespace "$NS"

	if [ "$mode" = rule-disabled ]; then
		"$PY" -I "$CHECK" rules --runner "$RUNNER" --out "$ev" disable || log "the rule did not go off (see items)"
	fi

	# ---- segment A
	local since_a rec_a q="$ev/quit-a"
	since_a="$(now_iso)"
	if [ "$mode" != chiller ]; then
		launch_player "$START_A" "$mode-A" "$ev/recordings" "$q" "$ev/logs/player-a.log" "$ca"
		if [ "$mode" = control ] || [ "$mode" = hot ]; then
			sleep 240
			rec_a="$(latest_run "$ev/recordings")"
			"$PY" -I "$CHECK" common --runner "$RUNNER" --out "$ev" --since "$since_a" --recording "$rec_a" --namespace "$NS" || true
		fi
		"$PY" -I "$CHECK" segment-a --runner "$RUNNER" --out "$ev" --mode "${mode/hot/control}" --since "$since_a" || true
		end_player "$q"
		rec_a="$(latest_run "$ev/recordings")"
		"$PY" -I "$CHECK" annotate --runner "$RUNNER" --recording "$rec_a" --namespace "$NS" || true
	fi

	# ---- segment B and the owner (control), or segment B alone with its calculated-metric check (chiller)
	if [ "$mode" != rule-disabled ] && [ "$mode" != hot ]; then
		local since_b rec_b q2="$ev/quit-b"
		since_b="$(now_iso)"
		launch_player "$START_B" "$mode-B" "$ev/recordings" "$q2" "$ev/logs/player-b.log" "$ca"
		"$PY" -I "$CHECK" segment-b --runner "$RUNNER" --out "$ev" --since "$since_b" || true
		[ "$mode" != control ] || "$PY" -I "$CHECK" owner --runner "$RUNNER" --sim "$SIM_NAME" --out "$ev" --since "$since_a" || true
		# an optional extra check run while the player is still up and the sim is live: TP_HOOK <sim> <evidence-dir> <since>. It never decides the verdict.
		[ "$mode" != control ] || [ -z "${TP_HOOK:-}" ] || "$TP_HOOK" "$SIM_NAME" "$ev" "$since_a" || log "WARNING: TP_HOOK failed"
		end_player "$q2"
		rec_b="$(latest_run "$ev/recordings")"
		"$PY" -I "$CHECK" annotate --runner "$RUNNER" --recording "$rec_b" --namespace "$NS" || true
		[ "$mode" != chiller ] || "$PY" -I "$CHECK" calc --runner "$RUNNER" --out "$ev" --since "$since_b" --recording "$rec_b" --namespace "$NS" || true
	fi

	"$PY" -I - "$ev" "$mode" "$SIM_NAME" "$INSTANCE" "$(git -C "$PROJECT" rev-parse --short=12 HEAD)" "$STAMP" <<'EOF'
import json, sys
ev, mode, sim, inst, sha, stamp = sys.argv[1:7]
json.dump({"mode": mode, "sim": sim, "instance": inst, "commit": sha, "stamp": stamp, "clock": "wall (model speed 1; readings stamped with the wall clock)", "seed": 1}, open(ev + "/context.json", "w"))
EOF
	local verdict=0 need
	if [ "$mode" = control ]; then
		need="sessions,telemetry,calculated,a-alarm,a-boost-sent,a-boost-successful-at-setpoint,a-alarm-cleared,b-silent,b-r4-alarm,b-restart-successful,b-reporting-again,b-alarm-cleared,o-signin,o-annex-only"
	elif [ "$mode" = hot ]; then
		need="sessions,telemetry,calculated,a-alarm,a-boost-sent,a-boost-successful-at-setpoint,a-alarm-cleared"
	elif [ "$mode" = chiller ]; then
		need="b-silent,b-r4-alarm,b-restart-successful,b-reporting-again,b-alarm-cleared,calculated"
	else
		need="rule-disabled-effective,c-no-alarm,c-no-boost,c-zone-overheated,c-rule-was-off"
	fi
	"$PY" -I "$CHECK" summary --out "$ev" --title "Towerpulse acceptance: $mode" --require "$need" || verdict=$?
	leak_scan "$ev" || verdict=2
	teardown
	return "$verdict"
}

# nothing in the bundle may look like a credential
leak_scan() {
	local f hits=0
	while IFS= read -r f; do
		if grep -qE 'eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.' "$f" 2>/dev/null; then log "LEAK: a JWT-shaped string in $f"; hits=1; fi
	done < <(find "$1" -type f \( -name '*.md' -o -name '*.json' -o -name '*.jsonl' -o -name '*.ndjson' -o -name '*.log' \))
	return "$hits"
}

preflight
build_player
overall=0
for m in "${MODES[@]}"; do
	run_mode "$m" || overall=1
done
log "evidence under $OUT_ROOT/$STAMP-*"
exit "$overall"
