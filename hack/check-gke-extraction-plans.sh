#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The no-op plan gate for the deploy/gke -> modules/gke-cluster extraction.
#
# WHAT IT ASSERTS. For each fixture (a sanitized state of a cluster built from a past or
# present shape of the root): `tofu plan -refresh=false -out`, then `tofu show -json`, then
#
#   1. EVERY resource_changes entry, of EVERY type, has actions exactly ["no-op"]. A pure
#      move is a no-op with previous_address set. "Zero deletes" would not be enough: an
#      in-place node_config update recreates every node, and terraform_data.cluster_settled is
#      silently REPLACED when something its triggers_replace reads changes.
#   2. replace_paths is empty everywhere.
#   3. Each resource ends at the address the fixture's EXPECTED-ADDRESS MAP names (old address
#      -> new address), and nothing else is planned. The map is a per-fixture input, so the
#      same gate serves a tree with no module and a tree with one.
#
# 🔴 IT FAILS, NEVER PASSES, WHEN IT HAS NOTHING TO LOOK AT: no fixtures, a fixture with no
# state, an unparsable plan, an action it does not know. A gate that can pass over an empty
# directory is the failure shape this repo has paid for before.
#
# FIXTURES. A fixture is a directory holding `fixture.conf` (shell variables, all paths relative
# to the fixture directory except ROOT):
#     ROOT=deploy/gke           the OpenTofu root, relative to the base tree (default: the repo)
#     STATE=state.json          a version-4 state; NAMED *.state.json, never *.tfstate (gitignored,
#                               so a fixture with that name would never be committed and the gate
#                               would pass over nothing in CI)
#     VARS=vars.vars.json       NOT *.tfvars or *.env: both are gitignored, like *.tfstate
#     MAP=map.json              { "<old address>": "<expected new address>", ... }
# The plan is run against a temporary COPY of the state, so a fixture is never modified.
# Everything runs offline: a dummy access token and -refresh=false, no cloud call is made.
#
# Usage:
#   hack/check-gke-extraction-plans.sh [--base DIR] FIXTURE_DIR...
#   hack/check-gke-extraction-plans.sh --self-test     # shows the gate refuses what it must
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export GOOGLE_OAUTH_ACCESS_TOKEN="${GOOGLE_OAUTH_ACCESS_TOKEN:-dummy}"
export GOOGLE_CLOUD_PROJECT="${GOOGLE_CLOUD_PROJECT:-example-project}"
export TF_IN_AUTOMATION=1

fail() {
	echo "FAIL: $*" >&2
	return 1
}

# check_plan_json PLAN_JSON MAP_JSON: prints one line per violation (nothing when clean).
check_plan_json() {
	local plan="$1" map="$2"
	jq -r --slurpfile map "$map" '
		(.resource_changes // []) as $rc
		| ($map[0]) as $m
		| ([ $m | to_entries[] | .value ]) as $want
		| (
			(if ($rc | length) == 0 then ["the plan contains no resource_changes: nothing was checked"] else [] end)
			+ [ $rc[] | select(.change.actions != ["no-op"])
				| "\(.address): actions \(.change.actions | tostring) (want no-op)"
				+ (if .action_reason then " reason=\(.action_reason)" else "" end) ]
			+ [ $rc[] | select((.change.replace_paths // []) | length > 0)
				| "\(.address): replace_paths \(.change.replace_paths | tostring)" ]
			+ [ $m | to_entries[] as $e
				| select(([ $rc[] | select(.address == $e.value
						and (($e.key == $e.value) or (.previous_address == $e.key))) ] | length) == 0)
				| "expected \($e.key) at \($e.value), not found there" ]
			+ [ $rc[] | select(.address as $a | ($want | index($a)) == null)
				| "\(.address): planned but not in the expected-address map" ]
		  ) | .[]
	' "$plan"
}

run_fixture() {
	local dir="$1" base="$2"
	local ROOT STATE VARS MAP
	[ -f "$dir/fixture.conf" ] || { fail "$dir has no fixture.conf"; return 1; }
	ROOT="" STATE="" VARS="" MAP=""
	# shellcheck disable=SC1091
	. "$dir/fixture.conf"
	if [ -z "$ROOT" ] || [ -z "$STATE" ] || [ -z "$VARS" ] || [ -z "$MAP" ]; then
		fail "$dir/fixture.conf must set ROOT, STATE, VARS and MAP"
		return 1
	fi
	case "$STATE" in
	*.state.json) ;;
	*) fail "$dir: STATE must be named *.state.json, got $STATE"; return 1 ;;
	esac
	[ -f "$dir/$STATE" ] || { fail "$dir: state $STATE is missing"; return 1; }
	[ -f "$dir/$VARS" ] || { fail "$dir: vars $VARS is missing"; return 1; }
	[ -f "$dir/$MAP" ] || { fail "$dir: map $MAP is missing"; return 1; }
	[ -d "$base/$ROOT" ] || { fail "$dir: root $base/$ROOT is missing"; return 1; }
	jq -e 'type == "object" and length > 0' "$dir/$MAP" >/dev/null || { fail "$dir/$MAP is not a non-empty object"; return 1; }

	local work absdir rc=0
	work="$(mktemp -d)"
	absdir="$(cd "$dir" && pwd)"
	cp "$absdir/$STATE" "$work/in.state.json"
	(
		cd "$base/$ROOT"
		tofu init -backend=false -input=false -no-color >"$work/init.log" 2>&1 || { cat "$work/init.log" >&2; exit 3; }
		tofu plan -refresh=false -input=false -no-color -var-file="$absdir/$VARS" \
			-state="$work/in.state.json" -out="$work/p.tfplan" >"$work/plan.log" 2>&1 || { tail -30 "$work/plan.log" >&2; exit 4; }
		tofu show -json "$work/p.tfplan" >"$work/plan.json"
	) || rc=$?
	if [ "$rc" -ne 0 ]; then
		echo "FAIL: $dir: tofu exited $rc (init=3, plan=4): a plan that errors is not a no-op plan" >&2
		rm -rf "$work"
		return 1
	fi
	if ! jq -e . "$work/plan.json" >/dev/null 2>&1; then
		fail "$dir: the plan is not parsable JSON"
		rm -rf "$work"
		return 1
	fi
	local bad
	if ! bad="$(check_plan_json "$work/plan.json" "$absdir/$MAP")"; then
		fail "$dir: the plan could not be checked"
		rm -rf "$work"
		return 1
	fi
	rm -rf "$work"
	if [ -n "$bad" ]; then
		echo "FAIL: $dir" >&2
		printf '  %s\n' "$bad" >&2
		return 1
	fi
	echo "ok: $dir"
}

run_gate() {
	local base="$1"
	shift
	local rc=0 n=0 d f
	for d in "$@"; do
		[ -d "$d" ] || { fail "fixture directory $d does not exist"; rc=1; continue; }
		while IFS= read -r f; do
			n=$((n + 1))
			run_fixture "$(dirname "$f")" "$base" || rc=1
		done < <(find "$d" -type f -name fixture.conf | sort)
	done
	if [ "$n" -eq 0 ]; then
		echo "FAIL: no fixture.conf found under: $*  (a gate that checks nothing must not pass)" >&2
		return 1
	fi
	return "$rc"
}

# ---------------------------------------------------------------------------------------------
# Self-test: the gate is proven on a SYNTHETIC PAIR (a miniature flat root and the same resources
# behind a module call) before the real root is extracted. Every control must make it FAIL, and
# the legitimate pair must make it pass: a gate that only refuses is an outage.
# ---------------------------------------------------------------------------------------------
synthetic="$repo_root/hack/fixtures/gke-extraction/synthetic"

# fresh_tree DEST: a private copy of the synthetic pair, so a control never touches the real one.
fresh_tree() {
	local dest="$1"
	mkdir -p "$dest/synthetic"
	cp -R "$synthetic/." "$dest/synthetic/"
	rm -rf "$dest"/synthetic/*/.terraform "$dest"/synthetic/*/.terraform.lock.hcl "$dest"/synthetic/*/modules/*/.terraform
}

# expect_fail NAME TREE FIXTURE...: the gate over those fixtures must exit non-zero.
expect_fail() {
	local name="$1" tree="$2"
	shift 2
	local out
	if out="$(run_gate "$tree" "$@" 2>&1)"; then
		echo "SELF-TEST FAIL: control $name was NOT refused by the gate" >&2
		return 1
	fi
	# Say WHY it was refused, so a control refused for the wrong reason (an environment error
	# rather than the planted defect) is visible in the log.
	echo "  control $name refused, as it must be: $(printf '%s
' "$out" | grep -v '^FAIL' | grep -m1 -E 'actions|expected|planned|nothing|tofu exited|Error' | sed 's/^ *//' || true)"
}

# mutate TREE FILE FROM TO: plant one defect, failing if the text to replace is not there (a
# control that did not apply would "pass" by refusing nothing).
mutate() {
	python3 -X utf8 - "$1/$2" "$3" "$4" <<'PY'
import sys
p, a, b = sys.argv[1:4]
s = open(p, encoding="utf-8").read()
if a not in s:
    sys.exit("control did not apply: %r not in %s" % (a, p))
open(p, "w", encoding="utf-8", newline="\n").write(s.replace(a, b, 1))
PY
}

self_test() {
	command -v jq >/dev/null || { echo "SELF-TEST FAIL: jq is required" >&2; return 1; }
	command -v tofu >/dev/null || { echo "SELF-TEST FAIL: tofu is required" >&2; return 1; }
	command -v python3 >/dev/null || { echo "SELF-TEST FAIL: python3 is required" >&2; return 1; }
	local tmp rc=0
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" EXIT

	# COUNTERWEIGHT: the legitimate pair passes on every shape.
	local ok="$tmp/ok"
	fresh_tree "$ok"
	if ! run_gate "$ok" "$ok/synthetic/fixtures" >"$tmp/ok.log" 2>&1; then
		cat "$tmp/ok.log" >&2
		echo "SELF-TEST FAIL: the legitimate synthetic pair did not pass; a gate that only refuses is an outage" >&2
		return 1
	fi
	echo "  counterweight: the legitimate pair passes on $(grep -c '^ok:' "$tmp/ok.log") fixtures"
	[ "$(grep -c '^ok:' "$tmp/ok.log")" -eq 4 ] || { echo "SELF-TEST FAIL: expected 4 synthetic fixtures to pass" >&2; return 1; }

	local mf=synthetic/modular/main.tf mm=synthetic/modular/modules/gke-cluster/main.tf t
	local fx=synthetic/fixtures

	# N1: remove the cluster's moved block -> cluster delete and create.
	t="$tmp/N1"; fresh_tree "$t"
	mutate "$t" $mf 'moved {
  from = google_container_cluster.this
  to   = module.gke_cluster.google_container_cluster.this
}
' '' || rc=1
	expect_fail N1 "$t" "$t/$fx/modular-current" || rc=1

	# N2: remove the pools' moved block -> pool deletes.
	t="$tmp/N2"; fresh_tree "$t"
	mutate "$t" $mf 'moved {
  from = google_container_node_pool.this
  to   = module.gke_cluster.google_container_node_pool.this
}
' '' || rc=1
	expect_fail N2 "$t" "$t/$fx/modular-current" || rc=1

	# N3: remove the legacy loadgen[0] moved block -> the old-shape fixture fails.
	t="$tmp/N3"; fresh_tree "$t"
	mutate "$t" $mf 'moved {
  from = google_container_node_pool.loadgen[0]
  to   = google_container_node_pool.this["loadgen"]
}
' '' || rc=1
	expect_fail N3 "$t" "$t/$fx/modular-legacy" || rc=1

	# N4: rename the module call -> every modular fixture fails.
	t="$tmp/N4"; fresh_tree "$t"
	mutate "$t" $mf 'module "gke_cluster" {' 'module "renamed_cluster" {' || rc=1
	expect_fail N4 "$t" "$t/$fx/modular-current" || rc=1
	expect_fail N4b "$t" "$t/$fx/modular-legacy" || rc=1

	# N5: an empty fixture directory -> the gate fails rather than passing over nothing.
	mkdir -p "$tmp/empty"
	expect_fail N5 "$ok" "$tmp/empty" || rc=1

	# N6: a module input wired to a value that differs from the root's former literal. The value
	# is one the settle resource's trigger does NOT read (a pool's node label), so the ONLY thing
	# that can refuse it is the in-place `update` check: a defect that also replaced the settle
	# resource would be caught twice and would not show that the update check works.
	t="$tmp/N6"; fresh_tree "$t"
	mutate "$t" $mm 'labels       = var.labels' 'labels       = merge(var.labels, { drift = "yes" })' || rc=1
	expect_fail N6 "$t" "$t/$fx/modular-current" || rc=1

	# N7: ownership labels merged into the value the settle resource's trigger reads -> replaced.
	t="$tmp/N7"; fresh_tree "$t"
	mutate "$t" $mm 'labels        = jsonencode(var.labels)' 'labels        = jsonencode(merge(var.labels, { "dc-fleet-owner" = "x" }))' || rc=1
	expect_fail N7 "$t" "$t/$fx/modular-current" || rc=1

	# A map that names the wrong address must be refused too, or the gate checks half of what it claims.
	t="$tmp/wrongmap"; fresh_tree "$t"
	mutate "$t" $fx/modular-current/map.json 'module.gke_cluster.google_container_cluster.this' 'module.elsewhere.google_container_cluster.this' || rc=1
	expect_fail wrong-address-map "$t" "$t/$fx/modular-current" || rc=1

	if [ "$rc" -eq 0 ]; then
		echo "SELF-TEST PASS: the gate passes the legitimate pair and refuses N1-N7 and a wrong address map"
	fi
	return "$rc"
}

case "${1:-}" in
--self-test) self_test ;;
--base)
	[ $# -ge 3 ] || { echo "FAIL: --base needs a directory and at least one fixture directory" >&2; exit 1; }
	base="$2"
	shift 2
	run_gate "$base" "$@"
	;;
"")
	echo "usage: $0 [--base DIR] FIXTURE_DIR... | --self-test" >&2
	exit 1
	;;
*) run_gate "$repo_root" "$@" ;;
esac
