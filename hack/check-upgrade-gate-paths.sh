#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Owns, and guards, the rule that decides whether a pull request runs the upgrade drill.
#
# `.github/workflows/upgrade-gate.yml` runs the drill on a pull request only when that
# PR touches something which can change what an existing instance holds or how it is
# read. The dominant entry in that list is migrations: they are the direct cause of the
# class the drill exists to find.
#
# 🔴 THE LIST LIVES HERE, NOT IN THE WORKFLOW'S `on: paths:`, AND THAT IS THE POINT.
# The drill has to be a REQUIRED check to be worth anything — a red gate nobody is
# obliged to look at is discipline wearing a gate's clothes — and a required check that
# never reports leaves every unrelated pull request pending forever. So the workflow
# fires on every PR and this script decides, inside the run, whether the drill is
# warranted. One list, read by both the completeness check and the decision, because
# the alternative is two lists and a promise that they agree.
#
# 🔴 A LIST THAT STOPS MATCHING IS SILENT, AND SILENT IN THE WORST DIRECTION. Nothing
# fails. The PR goes green, the drill simply does not appear, and "the upgrade drill did
# not object" reads exactly like "the upgrade drill passed" to everyone including the
# person who merges it. That is the same shape of defect the drill was built to catch —
# an unchecked claim that reads as a checked one — so the list does not get to be an
# unchecked claim itself.
#
# Three claims are checked, and they are deliberately of different kinds:
#
#   1. COMPLETENESS. Every migration and baseline file in the tree is matched by at
#      least one pattern. This is the one set that can be enumerated without judgement;
#      the rest of the list (served schemas, models, the chart) is a coverage judgement
#      and is not second-guessed here.
#   2. SHAPE. The workflow still has a `pull_request` trigger, and that trigger declares
#      NO `paths:`/`paths-ignore:`. A filter reintroduced up there would AND with the
#      decision below, invisibly, and the decision would never see the files it filtered
#      out — the two-lists failure this file exists to avoid, wearing a disguise.
#   3. THE DECISION ITSELF (`--match-changed`), which is what the workflow calls.
#
# The most likely way claim 1 fires: a NEW functional area lands, with its schema under
# a directory shape the patterns do not reach. That area's migrations would then never
# trigger the drill, and nobody would learn it from a green PR.
#
# Usage:
#   hack/check-upgrade-gate-paths.sh                  # claims 1 and 2
#   hack/check-upgrade-gate-paths.sh --match-changed  # paths on stdin -> run=true|false
#   hack/check-upgrade-gate-paths.sh --self-test      # prove all three can fail

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

WORKFLOW=".github/workflows/upgrade-gate.yml"

# ---------------------------------------------------------------------------
# GATE_PATHS — the single source of truth for "can this change what an instance holds?"
# ---------------------------------------------------------------------------
# Priced rather than guessed: over the sixty merged commits before 2026-08-27 these
# patterns fire on twenty-five of them, and the drill costs about eight minutes on a
# runner. What the list has to cover is anything that can change what an existing
# instance holds or how it is read:
#
#   - migrations and baselines, the direct cause;
#   - a served schema, because the drill's read-backs select named fields;
#   - `model_*.go`, because a stored shape can move with NO migration at all — that is
#     3 of the 25, and it is the class the geofence-archive defect belonged to;
#   - apiprobe and the rig, so a change to the instrument is measured by the instrument;
#   - the chart, because it renders what an instance runs, and a chart change is what
#     broke the upgrade procedure in v0.11.0;
#   - core messaging and stream declarations, because the first new pod reconciles
#     them against the live broker's streams and consumers, and a refusal there is a
#     crash-loop at upgrade with no migration anywhere in the diff;
#   - 🔴 THE UPGRADE VERB ITSELF, which this list did not cover for one whole release.
#     The documented upgrade used to be a `helm upgrade`, so watching the chart watched
#     the procedure. It is now `dcctl upgrade`, which composes the release's values,
#     reads back every credential the instance runs on, writes the instance
#     configuration document, re-issues the broker's certificate and reconciles the
#     root-key escrow — and NONE of that lives in the chart. Measured, not assumed: the
#     release that built all of it changed only files under `backend/cli/bootstrap/`,
#     and this decider answered `run=false` for that changeset. The drill that exists
#     to validate upgrades did not watch the code that performs one.
#     🔑 The general shape, and it is this project's most expensive: a gate keeps
#     watching where the risk USED to be. When a procedure moves, move its gate.
#   - THIS FILE, because it now decides whether the drill runs at all. A change to the
#     decider that quietly narrowed the decision would otherwise be the one change the
#     drill never sees.
#
# 🔴 THREE ANSWERS, NOT TWO, AND THE THIRD IS WHY THE LIST CAN BE SHORT. Every changed
# file is classified as one of:
#   GATE        it matches GATE_PATHS and not GATE_EXCLUDE  -> the drill runs;
#   KNOWN       it is inside a workspace module (go.work), or matches NOT_UPGRADE
#                                                            -> no reason to run;
#   UNKNOWN     anything else: outside every module and on no list -> the drill RUNS.
# The list used to be the only answer, so a path nobody had thought about (a new top-level
# tree, VERSION, .ko.yaml) answered `run=false` by default. Now only code the workspace
# declares, or a tree written down as not reaching the drill, can be skipped; everything
# unrecognised is assumed to matter. The module list is read from go.work, and an unreadable
# go.work makes every unlisted path UNKNOWN, so that failure runs the drill too.
#
# Re-derived 2026-11-16 from what the drill builds and runs (deploy/local/build-images.sh
# builds every service, the operator, the broker and the console; dcctl and apiprobe are
# built from the tree; the rig sources hack/), adding dependency versions, the operator,
# configuration, KV and broker-auth declarations, secrets, schema packages, the install verb
# and all of hack/ — and dropping test files and fixtures, which no drill compiles. Replayed
# over the 80 merged commits before 2026-11-17, see the pull request that made the change.
#
# Syntax is GitHub's filter language, kept rather than switched to globs, so that the
# list stays copy-pasteable back into an `on: paths:` block if this design is ever
# reversed — and so `**` keeps meaning "crosses directories" while `*` does not.
GATE_PATHS=(
  # --- What an instance HOLDS -------------------------------------------------------
  # Migrations and baselines, the direct cause. Wherever an area keeps them (most in a
  # schema package, some in model).
  'backend/**/migration*.go'
  'backend/**/baseline*.go'
  # The rest of a schema package: seeds and snapshot structs whose file names are not
  # migration*/baseline*. A seed edit moves what a fresh row holds and passes
  # `migration-diff.sh verify`, which captures no rows; only a read-back sees it.
  'backend/services/*/schema/**'
  # A stored shape can move with NO migration at all — the geofence-archive class.
  'backend/services/*/model/model_*.go'
  # A service's wire contract (device-management/contract): the fact and request shapes
  # other services decode, moved out of model/model_*.go with their tags intact. A tag edit
  # here moves bytes old pods still read mid-upgrade.
  'backend/services/*/contract/**'
  # The migration runner and the tenant-scope callback every read goes through.
  'backend/core/rdb/**'
  # Envelope-encrypted secrets are stored rows sealed under the instance root key, and
  # dcctl upgrade reconciles the root-key escrow (core/secrets/escrow): a change to
  # either can leave a stored secret that no longer opens after the upgrade.
  'backend/core/secrets/**'
  # --- How it is READ back ------------------------------------------------------------
  # A served schema, because the drill's read-backs select named fields.
  'backend/services/*/graphql/**'
  # apiprobe seeds and reads; a change to the instrument is measured by the instrument.
  'backend/tools/apiprobe/**'
  # --- What the first NEW pod does to a LIVE instance ----------------------------------
  # Core messaging and stream/KV declarations: the first new pod reconciles them against
  # the live broker's streams, consumers and buckets, and a declaration the running
  # broker refuses — or a consumer config it will not update in place — crash-loops that
  # pod at upgrade with no migration anywhere in the diff.
  'backend/core/streams/**'
  'backend/core/messaging/**'
  'backend/core/kv/**'
  # The broker accounts and credentials. The drill checks that the shared broker login
  # is kept by the first upgrade and retired by the second, which is natsauth's output.
  'backend/core/natsauth/**'
  # Configuration. An old pod and a new one read the SAME instance configuration
  # document during a rolling upgrade, so a new key, a changed default or a stricter
  # parse here is what crash-loops the pods the upgrade has not reached yet — the
  # failure the upgrade drill caught in a release-candidate drill (a defaulted new
  # config key plus an unconditional stream subject).
  'backend/core/config/**'
  'backend/services/*/config/**'
  # The message wire between old and new pods while both run.
  'backend/proto/**'
  'backend/services/*/proto/**'
  # Dependency versions: one go.work resolves one version of each dependency for every
  # image the drill builds (the database driver, the NATS client, the ORM), so a bump in
  # any go.mod can change how every service reads and writes an existing instance.
  'go.work'
  'go.work.sum'
  '**/go.mod'
  '**/go.sum'
  # The base image every ko-built image the drill runs is built on.
  '.ko.yaml'
  # --- The upgrade PROCEDURE --------------------------------------------------------
  # 🔴 THE UPGRADE VERB ITSELF, which this list did not cover for one whole release
  # (see the header). `dcctl upgrade` composes the release's values, reads back every
  # credential, writes the instance configuration document, re-issues the broker's
  # certificate and reconciles the root-key escrow. These are the packages it compiles
  # outside core: bootstrap and the two it imports, plus the three verbs.
  'backend/cli/bootstrap/**'
  'backend/cli/dcdir/**'
  'backend/cli/operator/**'
  'backend/cli/cmd/upgrade.go'
  'backend/cli/cmd/install.go'
  'backend/cli/cmd/bootstrap.go'
  # The operator: its image is built from this tree and the drill requires it to be
  # running the version the services were upgraded to; dcctl also compiles its API
  # types, manifests and functional-area table (k8s/api, apply, config, functionalarea).
  'backend/k8s/**'
  # The broker the drill restarts during the upgrade (dc-nats-server, built from this tree
  # with its own main) and the broker administrator the chart runs as its pre-upgrade hook
  # Job (dc-broker-admin). Either failing to start, or reconciling accounts differently, is
  # an upgrade failure with no service code in the diff.
  'backend/broker/**'
  'backend/admin/**'
  # The deploy module: the chart (it renders what an instance runs, and a chart change is
  # what broke the upgrade procedure in v0.11.0), the OpenTofu roots the chart is applied
  # on top of, the image build the drill runs (deploy/local/build-images.sh) and the
  # operand image it loads (deploy/images/).
  'deploy/**'
  # --- The instrument -------------------------------------------------------------------
  # Every script the drill or its workflow runs, sources or reads lives under hack/:
  # the rig, the baseline policy that decides WHICH drill runs, the stub identity
  # provider, the tofu-root discovery, the image-pin and pull helpers, and THIS FILE,
  # which decides whether the drill runs at all. A textual reference walk from the
  # workflow reaches forty of them, mostly through comments, so a hand list of the
  # "real" ones would be a judgement nobody can check. All of hack/ runs it instead.
  'hack/**'
  '.github/workflows/upgrade-gate.yml'
)

# Inside the paths above, files that can never reach a drill: the drill compiles no test
# and reads no fixture (it builds images, dcctl and apiprobe, and none of their non-test
# code embeds a testdata file — checked when this list was written).
GATE_EXCLUDE=(
  'backend/**/*_test.go'
  'backend/**/testdata/**'
)

# Outside every workspace module, the trees that are KNOWN not to reach the drill. Any
# other path outside the modules RUNS it: an unrecognised file is assumed to matter
# (VERSION, a new top-level directory) rather than assumed not to.
#   docs, docs-drafts, branding, *.md, LICENSE...: prose and assets nothing builds into
#     an image the drill runs.
#   frontend: the drill does build the console image, but it serves static files; its
#     build is the frontend CI job's to fail, and nothing it holds survives an upgrade.
#   sdks, demos, examples, _legacy: not built by the drill.
#   backend/testdata: test fixtures (see GATE_EXCLUDE).
#   .github/** other than this gate's workflow: other workflows, templates and
#     dependabot config; the drill runs none of them.
NOT_UPGRADE=(
  'docs/**'
  'docs-drafts/**'
  'branding/**'
  'frontend/**'
  'sdks/**'
  'demos/**'
  'examples/**'
  '_legacy/**'
  'backend/testdata/**'
  '.github/**'
  '*.md'
  'LICENSE'
  'NOTICE'
  '.gitignore'
  '.gitattributes'
  'global.json'
  'widget-translations-added.txt'
)

usage() {
  echo "usage: $0 [--match-changed | --self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# matches: does any pattern in $2.. match the path in $1?
# ---------------------------------------------------------------------------
# GitHub's filter language is not fnmatch: `**` crosses directory separators and `*`
# does not. Translating to a regex is the only way to say that, and it is why this is
# python rather than a `case` statement.
matches() {
  python3 - "$@" <<'PY'
import re, sys
path, pats = sys.argv[1], sys.argv[2:]
def to_re(p):
    out, i = "", 0
    while i < len(p):
        if p[i:i+3] == "**/": out += "(?:.*/)?"; i += 3
        elif p[i:i+2] == "**": out += ".*"; i += 2
        elif p[i] == "*":    out += "[^/]*"; i += 1
        else:                out += re.escape(p[i]); i += 1
    return re.compile("^" + out + "$")
sys.exit(0 if any(to_re(p).match(path) for p in pats) else 1)
PY
}

# ---------------------------------------------------------------------------
# CLAIM 1 — completeness: every migration/baseline file must be matched.
# ---------------------------------------------------------------------------
# `_legacy/` is excluded because it is the archived pre-migration tree: not in the
# workspace, not built, and explicitly not maintained. Including it would make this
# check fail forever on files nobody is allowed to touch.
check_completeness() {
  local pats=("$@") f rc=0 checked=0

  if [ "${#pats[@]}" -eq 0 ]; then
    echo "::error::the pattern list is empty, so nothing would ever trigger the upgrade drill"
    return 1
  fi

  while read -r f; do
    checked=$((checked + 1))
    if ! matches "$f" "${pats[@]}"; then
      echo "::error file=${f}::not matched by any pattern in GATE_PATHS, so a PR touching it would NOT run the upgrade drill"
      rc=1
    fi
  done < <(git ls-files | grep -E '(^|/)(migration[^/]*|baseline[^/]*)\.go$' | grep -v '_test\.go$' | grep -v '^_legacy/')

  # 🔴 ZERO FILES CHECKED IS A FAILURE, NOT A PASS. If the enumeration above ever
  # returns nothing — a rename, a moved tree, a grep that stopped matching — every
  # file trivially passes and this script reports success having examined nothing.
  # That is precisely the vacuous green it exists to prevent elsewhere.
  if [ "$checked" -eq 0 ]; then
    echo "::error::found no migration or baseline files to check; this script examined nothing and its pass would mean nothing"
    return 1
  fi

  [ "$rc" -eq 0 ] && echo "GATE_PATHS covers all ${checked} migration/baseline files (${#pats[@]} patterns)"
  return "$rc"
}

# ---------------------------------------------------------------------------
# CLAIM 2 — shape: the workflow fires on every PR, and filters none of them away.
# ---------------------------------------------------------------------------
# Read with python's YAML parser rather than by grepping: the trigger sits in a block
# that also carries `branches:`, and a text scrape cannot tell those two lists apart.
check_workflow_shape() {
  local workflow="$1" verdict
  verdict="$(python3 - "$workflow" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
# `on:` parses as the boolean True in YAML 1.1, which is a trap worth naming: the key
# is not the string "on" and a doc["on"] lookup returns nothing at all.
triggers = doc.get(True, doc.get("on", {})) or {}
if "pull_request" not in triggers:
    print("no-trigger"); raise SystemExit
pr = triggers.get("pull_request") or {}
bad = [k for k in ("paths", "paths-ignore") if k in pr]
print("filtered:" + ",".join(bad) if bad else "ok")
PY
)"

  case "$verdict" in
    ok)
      echo "${workflow} fires on every pull request and filters none of them away"
      return 0 ;;
    no-trigger)
      echo "::error::${workflow} has no pull_request trigger, so the upgrade drill never runs on a pull request at all"
      return 1 ;;
    filtered:*)
      echo "::error::${workflow}'s pull_request trigger declares ${verdict#filtered:}."
      echo "    That filter ANDs with the decision in this script, invisibly: a file it"
      echo "    excludes never reaches --match-changed, so the drill can be skipped for a"
      echo "    change GATE_PATHS says needs it. The workflow must fire on every PR and"
      echo "    let this script decide. Delete the filter, or move its patterns into"
      echo "    GATE_PATHS if they belong there."
      return 1 ;;
    *)
      echo "::error::could not read ${workflow}'s triggers (got '${verdict}')"
      return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# CLAIM 3 — the decision. Paths on stdin, `run=true|false` on stdout.
# ---------------------------------------------------------------------------
# Output goes to stdout in $GITHUB_OUTPUT's own key=value form so the caller can append
# it directly; everything a human reads goes to stderr, so the two cannot be confused.
#
# 🔴 EVERY UNCERTAINTY RESOLVES TO `run=true`. Running the drill when it was not needed
# costs eight minutes. NOT running it when it was needed produces a green pull request
# that was never drilled — which is the exact outcome this whole file exists to prevent,
# and it is indistinguishable from a drill that passed.
match_changed() {
  local verdict
  # One python pass over the whole list, not one interpreter per file: a pull request can
  # touch hundreds of files and this runs on every one of them.
  verdict="$(GATE="$(printf '%s\n' "${GATE_PATHS[@]}")" \
             EXCL="$(printf '%s\n' "${GATE_EXCLUDE[@]}")" \
             INERT="$(printf '%s\n' "${NOT_UPGRADE[@]}")" \
             python3 -c '
import os, re, sys

def to_re(p):
    out, i = "", 0
    while i < len(p):
        if p[i:i+3] == "**/": out += "(?:.*/)?"; i += 3
        elif p[i:i+2] == "**": out += ".*"; i += 2
        elif p[i] == "*":    out += "[^/]*"; i += 1
        else:                out += re.escape(p[i]); i += 1
    return re.compile("^" + out + "$")

def pats(name):
    return [to_re(x) for x in os.environ[name].splitlines() if x]

gate, excl, inert = pats("GATE"), pats("EXCL"), pats("INERT")
hit = lambda f, ps: any(r.match(f) for r in ps)

# The workspace modules, from go.work: `use ./dir` lines and `use ( ... )` blocks. A go.work
# that cannot be read leaves this EMPTY, which makes every unlisted path "outside every
# module" and therefore unknown, so the failure runs the drill rather than skipping it.
mods = []
try:
    text = re.sub(r"//[^\n]*", "", open("go.work").read())
    for block in re.findall(r"^use\s*\((.*?)\)", text, re.M | re.S):
        mods += block.split()
    mods += re.findall(r"^use\s+([^\s(]+)", text, re.M)
except OSError as e:
    print("::warning::could not read go.work (%s); every unlisted path counts as unknown" % e, file=sys.stderr)
mods = [m[2:] if m.startswith("./") else m for m in mods]
if not mods:
    print("::warning::go.work names no modules; every unlisted path counts as unknown", file=sys.stderr)
inside = lambda f: any(f == m or f.startswith(m + "/") for m in mods)

total, gated, unknown = 0, [], []
for line in sys.stdin:
    f = line.strip()
    if not f:
        continue
    total += 1
    if hit(f, gate) and not hit(f, excl):
        gated.append(f)
    elif inside(f) or hit(f, inert):
        pass
    else:
        unknown.append(f)

err = lambda *a: print(*a, file=sys.stderr)
if total == 0:
    err("::warning::no changed files were supplied, which cannot be true of a pull request; running the drill rather than assuming there is nothing to drill")
    print("run=true"); sys.exit(0)
if gated:
    err("%d of %d changed file(s) can change what an existing instance holds, how it is read, or how it is upgraded:" % (len(gated), total))
    for f in gated: err("    " + f)
if unknown:
    err("%d of %d changed file(s) are outside every workspace module and on no known list; running the drill rather than assuming they cannot matter:" % (len(unknown), total))
    for f in unknown: err("    " + f)
if gated or unknown:
    print("run=true")
else:
    err("None of the %d changed file(s) can change what an existing instance holds or how it is read. Skipping the drill." % total)
    print("run=false")
')" || {
    # The classifier itself failed: "we do not know", which must not read as "nothing to drill".
    echo "::warning::the path classifier failed; running the drill rather than guessing" >&2
    verdict="run=true"
  }
  case "$verdict" in
    run=true|run=false) echo "$verdict" ;;
    *) echo "::warning::the path classifier produced '$verdict'; running the drill rather than guessing" >&2
       echo "run=true" ;;
  esac
}

check() {
  local rc=0
  check_completeness "${GATE_PATHS[@]}" || rc=1
  check_workflow_shape "$WORKFLOW" || rc=1
  return "$rc"
}

# ---------------------------------------------------------------------------
# self-test: each claim must be shown to FAIL, and each must be shown to PASS.
# ---------------------------------------------------------------------------
# The house rule, and it earns its keep here more than most: this script's whole job is
# to notice an absence, and a check for an absence that has never been seen to fire is
# indistinguishable from one that always passes. Each defect is planted ALONE — a
# self-test that plants all of them at once is satisfied by a checker that finds any one.
self_test() {
  local tmp rc out
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # --- claim 1: completeness -------------------------------------------------
  local without_migrations=()
  for p in "${GATE_PATHS[@]}"; do
    [ "$p" = 'backend/**/migration*.go' ] || without_migrations+=("$p")
  done
  rc=0; check_completeness "${without_migrations[@]}" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a list with no migration pattern was accepted" >&2; return 1; }
  echo "    a list that lost its migration pattern is REFUSED"

  rc=0; check_completeness >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: an empty pattern list was accepted" >&2; return 1; }
  echo "    an empty pattern list is REFUSED"

  # THE COUNTERWEIGHT. Refusing everything would pass both cases above while being
  # completely broken, so the real list must still be accepted.
  rc=0; check_completeness "${GATE_PATHS[@]}" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "SELF-TEST FAILED: the real pattern list was refused (exit $rc)" >&2; return 1; }
  echo "    the real pattern list is ACCEPTED"

  # --- claim 2: workflow shape ----------------------------------------------
  python3 - "$WORKFLOW" "$tmp/notrigger.yml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
key = True if True in doc else "on"
doc[key].pop("pull_request", None)
yaml.safe_dump(doc, open(sys.argv[2], "w"))
PY
  rc=0; check_workflow_shape "$tmp/notrigger.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a workflow with no pull_request trigger was accepted" >&2; return 1; }
  echo "    a workflow with no pull_request trigger is REFUSED"

  # The regression this design introduces the possibility of, so it is planted here
  # before anyone can introduce it for real.
  python3 - "$WORKFLOW" "$tmp/refiltered.yml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
key = True if True in doc else "on"
doc[key]["pull_request"]["paths"] = ["backend/**/migration*.go"]
yaml.safe_dump(doc, open(sys.argv[2], "w"))
PY
  rc=0; check_workflow_shape "$tmp/refiltered.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a reintroduced paths: filter was accepted" >&2; return 1; }
  echo "    a reintroduced paths: filter is REFUSED"

  rc=0; check_workflow_shape "$WORKFLOW" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "SELF-TEST FAILED: the real workflow was refused (exit $rc)" >&2; return 1; }
  echo "    the real workflow is ACCEPTED"

  # --- claim 3: the decision -------------------------------------------------
  out="$(printf 'backend/services/device-management/schema/baseline.go\ndocs/README.md\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a baseline change did not run the drill (got '$out')" >&2; return 1; }
  echo "    a changed baseline RUNS the drill"

  # How streams are declared and reconciled, and how consumers are configured, is
  # core code with no migration, schema or chart file in the diff — and a live
  # instance can crash-loop at upgrade on either. Both trees must run the drill.
  out="$(printf 'backend/core/messaging/nats.go\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a core messaging change did not run the drill (got '$out')" >&2; return 1; }
  echo "    a changed core messaging file RUNS the drill"

  out="$(printf 'backend/core/streams/streams.go\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a core stream declaration change did not run the drill (got '$out')" >&2; return 1; }
  echo "    a changed core stream declaration RUNS the drill"

  # THE COUNTERWEIGHT for claim 3, and the only case that proves the decision is a
  # decision. A --match-changed that answered run=true unconditionally would satisfy
  # every other case here while reinstating a drill on all sixty commits in sixty.
  out="$(printf 'docs/README.md\nfrontend/apps/console/src/main.tsx\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=false" ] || { echo "SELF-TEST FAILED: an unrelated change still ran the drill (got '$out')" >&2; return 1; }
  echo "    a change that cannot touch stored data SKIPS the drill"

  out="$(printf '' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: an empty file list did not fail closed (got '$out')" >&2; return 1; }
  echo "    an empty file list FAILS CLOSED and runs the drill"

  # `*` must not cross a directory separator, or `backend/services/*/model/model_*.go`
  # would quietly widen to every model file at any depth. This is the property that
  # makes the regex translation necessary; a plain fnmatch would get it wrong.
  # (A real module, so the path is module code rather than an unknown path outside it.)
  out="$(printf 'backend/services/device-management/x/model/model_x.go\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=false" ] || { echo "SELF-TEST FAILED: '*' crossed a directory separator (got '$out')" >&2; return 1; }
  echo "    a single '*' does not cross a directory separator"

  # A shape that moved out of model keeps the drill (the class model_*.go stood for).
  out="$(printf 'backend/services/device-management/contract/alarm_event.go
' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a changed wire-contract file did not run the drill (got '$out')" >&2; return 1; }
  echo "    a changed wire-contract file RUNS the drill"

  # Each added class must RUN as itself (one file per call, so nothing else can carry it).
  local f
  for f in backend/services/mcp/go.mod go.work.sum backend/k8s/controllers/instance_controller.go \
           backend/core/config/instance.go backend/services/device-state/config/config.go \
           backend/core/kv/buckets.go backend/services/user-management/schema/seed.go \
           backend/core/secrets/escrow/escrow.go backend/core/natsauth/x.go \
           backend/broker/dc-nats-server/main.go backend/admin/dc-broker-admin/main.go \
           deploy/local/build-images.sh hack/lib/sso-stub-idp.sh backend/cli/cmd/install.go; do
    out="$(printf '%s\n' "$f" | match_changed 2>/dev/null)"
    [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: $f did not run the drill (got '$out')" >&2; return 1; }
  done
  echo "    dependency versions, the operator, config, KV, secrets, seeds, the image build, hack/ and the install verb RUN the drill"

  # FAIL SAFE: a path outside every module that no list accounts for runs the drill.
  out="$(printf 'mystery/new-tree/file.txt\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: an unknown path outside the modules was skipped (got '$out')" >&2; return 1; }
  echo "    an unknown path outside every module RUNS the drill"

  # THE COUNTERWEIGHT: module code off the list, a test in a gated tree, a fixture and
  # another workflow skip. Without it, "always run" would pass every case above.
  out="$(printf 'backend/services/mcp/server/server.go\nbackend/cli/bootstrap/install_test.go\nbackend/core/rdb/testdata/x.sql\n.github/workflows/ci.yml\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=false" ] || { echo "SELF-TEST FAILED: module code off the list, a test file or another workflow ran the drill (got '$out')" >&2; return 1; }
  echo "    module code off the list, a test file, a fixture and another workflow SKIP the drill"

  # An unreadable go.work must not make module code look known: with no module list the
  # same file is outside every module, so it runs.
  out="$(cd "$tmp" && printf 'backend/services/mcp/server/server.go\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a missing go.work did not fail safe (got '$out')" >&2; return 1; }
  echo "    a missing go.work FAILS SAFE and runs the drill"

  # A classifier that cannot run means "we do not know", never "nothing to drill".
  out="$(python3() { return 1; }; printf 'docs/README.md\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a failed classifier did not fail safe (got '$out')" >&2; return 1; }
  echo "    a classifier that fails FAILS SAFE and runs the drill"

  echo "SELF-TEST PASSED"
}

case "${1:-}" in
  "")               check ;;
  --match-changed)  match_changed ;;
  --self-test)      self_test ;;
  *)                usage ;;
esac
