#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# live-env.sh -- the local environment the Sitepulse Live demo runs against, isolated from any
# other DeviceChain state on this machine.
#
#   live-env.sh up                bring it up (idempotent: re-running converges)
#   live-env.sh status            read-only: what is running, what is not (never changes anything)
#   live-env.sh down [--stop]     --stop pauses it (runner + kind node stopped, everything kept);
#                                 no flag tears it down and removes $SP_HOME
#   live-env.sh sample <label> <seconds> <interval>
#                                 footprint: docker stats of the node, the runner, the VM
#   live-env.sh verify            confirms the scenario's objects through the platform GraphQL
#   live-env.sh replicas [<deploy>]
#                                 read-only: the instance namespace's deployments as "name desired ready", or
#                                 one of them (an acceptance control reads the count it must put back)
#   live-env.sh scale <deploy> <n>
#                                 set one deployment's replica count in the instance namespace (the observer-outage
#                                 acceptance control scales event-management to 0 and back). Only ever the
#                                 isolated cluster: kubectl is pinned to its context and kubeconfig.
#
# What `up` builds:
#
#   * a kind cluster named "sitepulse" on the standard host ports 80/443/1883,
#     installed --compact by a RELEASED dcctl (checksum + build attestation verified);
#   * one instance "sitepulse" on http://localhost (no TLS on the ingress; the MQTT
#     gateway on ssl://localhost:1883 is always TLS, under a CA dcctl mints);
#   * the "sitepulse" sim (tenant sim-sitepulse) and its runner on 127.0.0.1:8090;
#   * the broker CA exported as a PEM for the player's pinned trust.
#
# Isolation: EVERY dcctl / kind / kubectl / helm / runner call runs with
# HOME=$SP_HOME and KUBECONFIG=$SP_HOME/.kube/config, so ~/.devicechain and
# ~/.kube/config are never read or written. The only shared things are the docker
# daemon and the host ports; the preflight refuses to start if any port is held by
# something other than this environment, and it never stops anything else.
#
# Idempotent: re-running `up` converges. A node container stopped by `down --stop`
# is started again; install/bootstrap are skipped once they have succeeded for the
# current cluster (SP_RECONVERGE=1 re-runs them); `sim create` reconciles; a live
# runner is left alone.
#
# Inputs (environment):
#   SP_HOME        isolated home                  (default: $HOME/sitepulse-env)
#   DC_VERSION     released platform version      (default: 0.19.0)
#   DCCTL          a dcctl binary to use; it must report DC_VERSION. Unset: the
#                  release asset is downloaded and verified with `gh` into $SP_HOME/bin.
#   DC_SIMULATOR   a dc-simulator binary. Unset: built from DC_REPO.
#   DC_REPO        a platform source tree to build dc-simulator from (default: the
#                  repository this script lives in, if any).
#   SP_CA_OUT      extra path to copy the broker CA PEM to (e.g. a Windows folder
#                  under /mnt/c). It is always written to $SP_HOME/nats-ca.pem.
#   SP_RECONVERGE  1 = re-run dcctl install + bootstrap even if already done.
#
# Nothing secret is printed: the runner's /config.json (it serves a tenant-admin
# token) is only probed for its status code or read in-process by verify, and the CA
# is a public certificate.

set -euo pipefail

SP_HOME="${SP_HOME:-$HOME/sitepulse-env}"
DC_VERSION="${DC_VERSION:-0.19.0}"
DC_GH_REPO="${DC_GH_REPO:-devicechain-io/devicechain}"
CLUSTER=sitepulse
INSTANCE=sitepulse
SIM=sitepulse
NODE="${CLUSTER}-control-plane"
NS="dci-${INSTANCE}"
RUN_DIR="$SP_HOME/run"
STATE_DIR="$SP_HOME/state"
RUNNER_PID="$RUN_DIR/dc-simulator.pid"
RUNNER_LOG="$RUN_DIR/dc-simulator.log"
RUNNER_ADDR="127.0.0.1:8090"
HANDSHAKE="$SP_HOME/.devicechain/sims/${SIM}.json"
CA_PEM="$SP_HOME/nats-ca.pem"
PORTS=(80 443 1883 8090)

log() { printf '[sitepulse-env] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# spenv runs a command inside the isolated environment.
spenv() { HOME="$SP_HOME" KUBECONFIG="$SP_HOME/.kube/config" "$@"; }
kc() { spenv kubectl --context "kind-${CLUSTER}" "$@"; }

# redact drops the one-time superuser password line a fresh bootstrap prints: this
# environment never needs it typed (dcctl reads it from the instance Secret).
redact() { grep --line-buffered -v -i 'password' || true; }

need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required on PATH"; }

container_state() {
	local s
	s="$(docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null)" || s=absent
	echo "${s:-absent}"
}

runner_alive() {
	[ -f "$RUNNER_PID" ] && kill -0 "$(cat "$RUNNER_PID")" 2>/dev/null &&
		curl -sf -o /dev/null --max-time 3 "http://$RUNNER_ADDR/status"
}

# ---------------------------------------------------------------------------
# binaries
# ---------------------------------------------------------------------------

resolve_dcctl() {
	if [ -z "${DCCTL:-}" ]; then
		need gh
		local dl="$SP_HOME/dl/dcctl-$DC_VERSION" asset="dcctl_${DC_VERSION}_linux_amd64.tar.gz"
		DCCTL="$SP_HOME/bin/dcctl-$DC_VERSION"
		if [ ! -x "$DCCTL" ]; then
			log "downloading dcctl v$DC_VERSION from $DC_GH_REPO"
			mkdir -p "$dl" "$SP_HOME/bin"
			gh release download "v$DC_VERSION" -R "$DC_GH_REPO" -D "$dl" --clobber \
				-p "$asset" -p checksums.txt
			(cd "$dl" && sha256sum -c --ignore-missing --strict checksums.txt) ||
				die "dcctl checksum mismatch"
			gh attestation verify "$dl/$asset" --repo "$DC_GH_REPO" \
				--cert-identity "https://github.com/$DC_GH_REPO/.github/workflows/release.yml@refs/tags/v$DC_VERSION" \
				>/dev/null || die "dcctl build attestation did not verify"
			tar -xzf "$dl/$asset" -C "$dl" dcctl
			install -m 0755 "$dl/dcctl" "$DCCTL"
		fi
	fi
	[ -x "$DCCTL" ] || die "DCCTL=$DCCTL is not executable"
	local out v
	out="$("$DCCTL" version 2>/dev/null)"
	v="$(awk 'NR==1{print $2}' <<<"$out")"
	[ "$v" = "$DC_VERSION" ] || die "dcctl reports version '$v', expected $DC_VERSION"
	grep -Eq "images: +v$DC_VERSION\$" <<<"$out" || die "dcctl does not default to v$DC_VERSION images"
	log "dcctl $v ($DCCTL)"
}

resolve_simulator() {
	if [ -z "${DC_SIMULATOR:-}" ]; then
		local repo="${DC_REPO:-$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || true)}"
		[ -n "$repo" ] && [ -d "$repo/backend/sims/dc-simulator" ] ||
			die "set DC_SIMULATOR to a dc-simulator binary, or DC_REPO to a platform source tree"
		need go
		DC_SIMULATOR="$SP_HOME/bin/dc-simulator"
		log "building dc-simulator from $repo"
		mkdir -p "$SP_HOME/bin"
		(cd "$repo/backend/sims/dc-simulator" && go build -o "$DC_SIMULATOR" .)
	fi
	[ -x "$DC_SIMULATOR" ] || die "DC_SIMULATOR=$DC_SIMULATOR is not executable"
}

# ---------------------------------------------------------------------------
# preflight
# ---------------------------------------------------------------------------

# port_holders prints the listeners on our ports, from WSL and (if reachable) Windows.
port_holders() {
	ss -Hltn "( $(printf 'sport = :%s or ' "${PORTS[@]}" | sed 's/ or $//') )" || true
	if command -v powershell.exe >/dev/null 2>&1; then
		powershell.exe -NoProfile -Command \
			"Get-NetTCPConnection -State Listen -LocalPort $(IFS=,; echo "${PORTS[*]}") -EA SilentlyContinue | ForEach-Object { \"win \$(\$_.LocalAddress):\$(\$_.LocalPort) pid=\$(\$_.OwningProcess)\" }" \
			2>/dev/null | tr -d '\r' || true
	fi
}

preflight() {
	need docker
	need kind
	need kubectl
	need helm
	need curl
	need openssl
	command -v tofu >/dev/null 2>&1 || command -v terraform >/dev/null 2>&1 ||
		die "OpenTofu (tofu) or Terraform is required on PATH"

	# Any other kind node publishing our ports must be stopped by its owner, not by us.
	local other
	other="$(docker ps --format '{{.Names}}\t{{.Ports}}' |
		awk -F'\t' -v me="$NODE" '$1!=me && ($2 ~ /:(80|443|1883)->/) {print $1}')"
	[ -z "$other" ] || die "container(s) already publish 80/443/1883: $other — stop them yourself, then re-run"

	# If this environment is not up yet, nothing may hold our ports on either side.
	if [ "$(container_state "$NODE")" != running ] && ! runner_alive; then
		local held
		held="$(port_holders)"
		[ -z "$held" ] || die "host ports ${PORTS[*]} are not free:
$held"
	fi
}

# ---------------------------------------------------------------------------
# cluster, instance, sim, runner, CA
# ---------------------------------------------------------------------------

ensure_cluster() {
	mkdir -p "$SP_HOME/.kube" "$RUN_DIR" "$STATE_DIR"
	chmod 700 "$SP_HOME"
	if ! grep -qx "$CLUSTER" <<<"$(spenv kind get clusters 2>/dev/null)"; then
		# A fresh cluster: whatever was recorded about an earlier one is void.
		rm -f "$STATE_DIR"/*.done
	fi
	case "$(container_state "$NODE")" in
	running) ;;
	exited | created)
		log "starting stopped node $NODE"
		docker start "$NODE" >/dev/null
		spenv kind export kubeconfig --name "$CLUSTER" >/dev/null
		kc wait --for=condition=Ready node --all --timeout=180s >/dev/null
		;;
	absent) ;; # dcctl install creates it
	*) die "node $NODE is in state $(container_state "$NODE")" ;;
	esac

	if [ ! -f "$STATE_DIR/install.done" ] || [ "${SP_RECONVERGE:-0}" = 1 ]; then
		log "dcctl install local --cluster $CLUSTER --compact"
		spenv "$DCCTL" install local --cluster "$CLUSTER" --compact --yes
		touch "$STATE_DIR/install.done"
	fi
	[ "$(spenv kubectl config current-context)" = "kind-$CLUSTER" ] ||
		die "isolated kubeconfig does not point at kind-$CLUSTER"
}

ensure_instance() {
	if [ ! -f "$STATE_DIR/bootstrap.done" ] || [ "${SP_RECONVERGE:-0}" = 1 ]; then
		log "dcctl bootstrap local $INSTANCE (http://localhost, no escrow — throwaway root key)"
		spenv "$DCCTL" bootstrap local "$INSTANCE" --cluster "$CLUSTER" \
			--host localhost --no-tls --no-escrow --yes 2>&1 | redact
		touch "$STATE_DIR/bootstrap.done"
	fi
	log "waiting for $NS workloads"
	kc -n "$NS" wait --for=condition=Available deployment --all --timeout=600s >/dev/null
	# Right after a node restart the Deployments still report their pre-stop status, so
	# Available proves nothing; wait for the areas the sim uses to answer through the
	# ingress, and for the broker to accept connections.
	local area deadline=$((SECONDS + 600))
	for area in user-management device-management dashboard-management event-processing \
		event-management device-state command-delivery; do
		until [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X POST \
			-H 'Content-Type: application/json' -d '{"query":"{__typename}"}' \
			"http://localhost/api/$area/graphql")" = 200 ]; do
			[ $SECONDS -lt $deadline ] || die "$area did not answer on http://localhost within 600s"
			sleep 3
		done
	done
	until (exec 3<>/dev/tcp/127.0.0.1/1883) 2>/dev/null; do
		[ $SECONDS -lt $deadline ] || die "MQTT gateway did not accept on :1883 within 600s"
		sleep 3
	done
}

ensure_sim() {
	log "dcctl sim create $SIM (manifest sitepulse, pinned broker trust)"
	spenv "$DCCTL" sim create "$SIM" --manifest sitepulse --instance "$INSTANCE" \
		--server localhost --seed 1 --mqtt-insecure=false >/dev/null
	[ -f "$HANDSHAKE" ] || die "sim create wrote no handshake at $HANDSHAKE"
	chmod 600 "$HANDSHAKE"
}

ensure_runner() {
	if runner_alive; then
		log "runner already up (pid $(cat "$RUNNER_PID"))"
		return
	fi
	log "starting dc-simulator on $RUNNER_ADDR (log: $RUNNER_LOG)"
	HOME="$SP_HOME" nohup setsid "$DC_SIMULATOR" --handshake "$HANDSHAKE" \
		>"$RUNNER_LOG" 2>&1 </dev/null &
	echo $! >"$RUNNER_PID"
	# /status answers only after the scenario's bootstrap (devices, rules, dashboard) succeeded.
	for _ in $(seq 1 120); do
		runner_alive && return
		kill -0 "$(cat "$RUNNER_PID")" 2>/dev/null ||
			die "dc-simulator exited during bootstrap; see $RUNNER_LOG"
		sleep 2
	done
	die "dc-simulator did not become ready within 240s; see $RUNNER_LOG"
}

export_ca() {
	kc -n "$NS" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d >"$CA_PEM.tmp"
	openssl x509 -in "$CA_PEM.tmp" -noout >/dev/null 2>&1 || die "exported CA is not a certificate"
	mv "$CA_PEM.tmp" "$CA_PEM"
	if [ -n "${SP_CA_OUT:-}" ]; then
		mkdir -p "$(dirname "$SP_CA_OUT")"
		cp "$CA_PEM" "$SP_CA_OUT"
	fi
	log "broker CA: $CA_PEM${SP_CA_OUT:+ (and $SP_CA_OUT)} — $(openssl x509 -in "$CA_PEM" -noout -subject -enddate | tr '\n' ' ')"
}

summary() {
	cat >&2 <<EOF
[sitepulse-env] up.
  console / GraphQL   http://localhost/            http://localhost/api/{area}/graphql
  MQTT (TLS only)     ssl://localhost:1883          CA: $CA_PEM
  runner              http://$RUNNER_ADDR/status    /config.json (serves a tenant-admin token; loopback only)
  isolated env        HOME=$SP_HOME KUBECONFIG=$SP_HOME/.kube/config
EOF
}


cmd_up() {
	preflight
	resolve_dcctl
	resolve_simulator
	ensure_cluster
	ensure_instance
	ensure_sim
	ensure_runner
	export_ca
	summary
}

# ---------------------------------------------------------------------------
# status: read-only. It starts nothing, stops nothing and prints no secret.
# ---------------------------------------------------------------------------

cmd_status() {
	local node runner_code rc=0
	node="$(container_state "$NODE")"
	echo "node container   $NODE: $node"
	if [ -f "$RUNNER_PID" ] && kill -0 "$(cat "$RUNNER_PID")" 2>/dev/null; then
		runner_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://$RUNNER_ADDR/status" || true)"
		echo "runner           pid $(cat "$RUNNER_PID"), /status -> ${runner_code:-none}"
		[ "$runner_code" = 200 ] || rc=1
	else
		echo "runner           not running"
		rc=1
	fi
	if [ -f "$CA_PEM" ]; then
		echo "broker CA        $CA_PEM — $(openssl x509 -in "$CA_PEM" -noout -enddate 2>/dev/null || echo 'not a certificate')"
	else
		echo "broker CA        missing ($CA_PEM)"
		rc=1
	fi
	if [ "$node" = running ]; then
		local area code
		for area in device-management event-management device-state command-delivery; do
			code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X POST -H 'Content-Type: application/json' \
				-d '{"query":"{__typename}"}' "http://localhost/api/$area/graphql" || true)"
			echo "graphql $area -> ${code:-none}"
			[ "$code" = 200 ] || rc=1
		done
		if (exec 3<>/dev/tcp/127.0.0.1/1883) 2>/dev/null; then echo "mqtt gateway     accepting on :1883"; else echo "mqtt gateway     NOT accepting on :1883"; rc=1; fi
	else
		rc=1
	fi
	[ "$rc" = 0 ] && echo "status: up" || echo "status: not fully up"
	return "$rc"
}

# ---------------------------------------------------------------------------
# down
# ---------------------------------------------------------------------------

stop_runner() {
	if [ -f "$RUNNER_PID" ] && kill -0 "$(cat "$RUNNER_PID")" 2>/dev/null; then
		log "stopping dc-simulator (pid $(cat "$RUNNER_PID"))"
		kill -TERM "$(cat "$RUNNER_PID")"
		for _ in $(seq 1 20); do kill -0 "$(cat "$RUNNER_PID")" 2>/dev/null || break; sleep 0.5; done
	fi
	rm -f "$RUNNER_PID"
}

cmd_down() {
	case "${1:-}" in
	--stop)
		stop_runner
		if [ "$(container_state "$NODE")" = running ]; then
			log "stopping node container $NODE"
			# The kind node does not exit on its stop signal (measured: still Exited (137),
			# i.e. SIGKILLed, after a 60 s grace), so a longer timeout only adds waiting. The
			# databases recover on the next start; this is a pause, not a clean shutdown.
			docker stop "$NODE" >/dev/null
		fi
		log "stopped; \`live-env.sh up\` resumes it"
		;;
	"")
		[ -d "$SP_HOME" ] || {
			log "nothing to do: $SP_HOME does not exist"
			return 0
		}
		DCCTL="${DCCTL:-$SP_HOME/bin/dcctl-$DC_VERSION}"
		[ -x "$DCCTL" ] || DCCTL="$(command -v dcctl)"
		stop_runner
		if [ "$(container_state "$NODE")" = exited ]; then
			docker start "$NODE" >/dev/null
		fi
		if grep -qx "$CLUSTER" <<<"$(spenv kind get clusters 2>/dev/null)"; then
			spenv "$DCCTL" sim destroy "$SIM" --instance "$INSTANCE" || log "sim destroy failed; continuing"
			spenv "$DCCTL" destroy "$INSTANCE" --yes || log "instance destroy failed; continuing"
			log "deleting kind cluster $CLUSTER"
			spenv kind delete cluster --name "$CLUSTER"
		fi
		log "removing $SP_HOME"
		rm -rf -- "$SP_HOME"
		;;
	*)
		echo "usage: $0 down [--stop]" >&2
		return 2
		;;
	esac
}

# ---------------------------------------------------------------------------
# runner: stop or start just the runner, for the acceptance's "runner stopped
# mid-run" control. The cluster is left alone.
# ---------------------------------------------------------------------------

cmd_runner() {
	case "${1:-}" in
	stop) stop_runner ;;
	start)
		resolve_simulator
		ensure_runner
		;;
	*)
		echo "usage: $0 runner stop|start" >&2
		return 2
		;;
	esac
}

# ---------------------------------------------------------------------------
# sample: the footprint of the environment over a window. Writes under
# $SP_HOME/footprint/<label> and prints a one-paragraph summary.
# ---------------------------------------------------------------------------

cmd_sample() {
	local label="${1:?usage: $0 sample <label> [seconds] [interval]}" secs="${2:-120}" iv="${3:-5}"
	local out="$SP_HOME/footprint/$label" end pid
	mkdir -p "$out"
	: >"$out/docker.tsv"
	end=$((SECONDS + secs))
	while [ $SECONDS -lt $end ]; do
		docker stats --no-stream --format '{{.CPUPerc}}\t{{.MemUsage}}' "$NODE" | sed "s/^/$(date +%s)\t/" >>"$out/docker.tsv"
		sleep "$iv"
	done
	pid="$(cat "$RUNNER_PID" 2>/dev/null || true)"
	[ -n "$pid" ] && ps -o pid,rss,etimes,times -p "$pid" >"$out/runner.txt" 2>/dev/null || : >"$out/runner.txt"
	free -m >"$out/free.txt"
	docker exec "$NODE" crictl stats -o json >"$out/crictl.json" 2>/dev/null || true
	docker exec "$NODE" crictl ps -o json >"$out/crictl-ps.json" 2>/dev/null || true
	if command -v powershell.exe >/dev/null 2>&1; then
		powershell.exe -NoProfile -Command "Get-Process vmmemWSL,vmmem -EA SilentlyContinue | % { \$_.Name + ' WS_MiB=' + [int](\$_.WorkingSet64/1MB) }" 2>/dev/null | tr -d '\r' >"$out/vmmem.txt" || true
	fi
	python3 - "$out" <<'PY'
import sys, json, re, statistics as st
o = sys.argv[1]
rows = [l.rstrip('\n').split('\t') for l in open(o + '/docker.tsv') if l.strip()]
cpu = [float(r[1].rstrip('%')) for r in rows]
def mib(s):
    v, u = re.match(r'([\d.]+)\s*([KMGT]i?B)', s).groups()
    return float(v) * {'KiB': 1/1024, 'MiB': 1, 'GiB': 1024, 'TiB': 1024**2, 'kB': 1/1024, 'MB': 1, 'GB': 1024}[u]
mem = [mib(r[2].split('/')[0]) for r in rows]
print(f"{o.split('/')[-1]}: n={len(cpu)} node CPU% mean={st.mean(cpu):.1f} median={st.median(cpu):.1f} max={max(cpu):.1f} (100%=1 core) | node mem MiB mean={st.mean(mem):.0f} max={max(mem):.0f}")
try:
    ps = {c['id']: c for c in json.load(open(o + '/crictl-ps.json'))['containers']}
    agg = {}
    for s in json.load(open(o + '/crictl.json'))['stats']:
        cid = s['attributes']['id']
        c = ps.get(cid)
        name = (c['labels'].get('io.kubernetes.pod.namespace', '?') + '/' + c['labels'].get('io.kubernetes.container.name', '?')) if c else cid[:12]
        ws = int(s.get('memory', {}).get('workingSetBytes', {}).get('value', 0)) / 2**20
        agg[name] = agg.get(name, 0) + ws
    print("  per-container working set MiB (top):", ', '.join(f"{k}={v:.0f}" for k, v in sorted(agg.items(), key=lambda x: -x[1])[:12]), f"| sum={sum(agg.values()):.0f}")
except Exception as e:
    print("  crictl parse failed:", e)
lines = open(o + '/runner.txt').read().split('\n')
print("  runner:", lines[1].strip() if len(lines) > 1 and lines[1].strip() else 'n/a')
try:
    print("  vmmem:", open(o + '/vmmem.txt').read().strip())
except OSError:
    pass
PY
}

# replicas: read-only. No argument lists every deployment of the instance namespace; one names a deployment.
cmd_replicas() {
	local deploy="${1:-}"
	[ "$(container_state "$NODE")" = running ] || die "the node $NODE is not running"
	if [ -z "$deploy" ]; then
		kc -n "$NS" get deploy -o 'jsonpath={range .items[*]}{.metadata.name}{" "}{.spec.replicas}{" "}{.status.readyReplicas}{"\n"}{end}'
		return
	fi
	valid_deploy "$deploy"
	kc -n "$NS" get deploy "$deploy" -o 'jsonpath={.metadata.name}{" "}{.spec.replicas}{" "}{.status.readyReplicas}{"\n"}'
}

valid_deploy() {
	[[ "$1" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "'$1' is not a deployment name"
}

# scale: the one mutation of the cluster this script offers beyond up/down. It names its deployment and its
# count, and refuses anything that is not a small non-negative integer.
cmd_scale() {
	local deploy="${1:-}" n="${2:-}"
	[ -n "$deploy" ] && [ -n "$n" ] || die "usage: $0 scale <deploy> <n>"
	valid_deploy "$deploy"
	[[ "$n" =~ ^[0-9]{1,2}$ ]] || die "'$n' is not a replica count (0-99)"
	[ "$(container_state "$NODE")" = running ] || die "the node $NODE is not running"
	[ "$(spenv kubectl config current-context)" = "kind-$CLUSTER" ] || die "isolated kubeconfig does not point at kind-$CLUSTER"
	kc -n "$NS" get deploy "$deploy" >/dev/null || die "no deployment $deploy in $NS"
	log "scaling $NS/$deploy to $n"
	kc -n "$NS" scale deploy "$deploy" --replicas="$n" >/dev/null
}

cmd_verify() {
	need python3
	exec python3 "$(dirname "$0")/live-env-verify.py"
}

case "${1:-}" in
up) cmd_up ;;
status) cmd_status ;;
down)
	shift
	cmd_down "$@"
	;;
runner)
	shift
	cmd_runner "$@"
	;;
sample)
	shift
	cmd_sample "$@"
	;;
verify) cmd_verify ;;
replicas)
	shift
	cmd_replicas "$@"
	;;
scale)
	shift
	cmd_scale "$@"
	;;
*)
	echo "usage: $0 up | status | down [--stop] | runner stop|start | sample <label> [seconds] [interval] | verify | replicas [<deploy>] | scale <deploy> <n>" >&2
	exit 2
	;;
esac
