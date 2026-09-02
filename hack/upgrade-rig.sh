#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# THE UPGRADE DRILL — does an existing instance's DATA survive a release?
#
# WHAT THIS GATES
#
# Not the merge. Everything in a release is landable and CI-tested without a
# cluster, and it is. What needs a real upgrade is the CLAIM the release notes
# make: that an operator running the previous version can move to this one and
# still have what they had.
#
# Nothing else measures that. `hack/migration-diff.sh verify` is the closest, and
# it compares `pg_dump --schema-only` — so it captures NO ROWS. A migration that
# creates every table and column perfectly and then rewrites, truncates or drops
# the data inside them passes it with every area green. The load harness drives
# ingest, detection and a command round trip very hard, but it seeds its own world
# on a fresh install and touches almost none of the CRUD API. And a fresh
# `dcctl destroy && bootstrap` is the INSTALL path, which structurally cannot see
# an upgrade defect at all.
#
# So the only way to see it is to write rows through the real API on the OLD
# version, upgrade the way the documentation tells an operator to, and read them
# back through the real API on the NEW one.
#
#   hack/upgrade-rig.sh up        cluster + registry, build the baseline release's
#                                 own dcctl from its tag, install THAT version, and
#                                 seed one of every entity it can express
#   hack/upgrade-rig.sh upgrade   build the working tree's images, carry the
#                                 release's values forward, and run the upgrade
#                                 exactly as docs/deployment/releases-and-upgrades
#                                 says to run it
#   hack/upgrade-rig.sh verify    every seeded row must read back UNCHANGED
#   hack/upgrade-rig.sh tablesweep  every tenant-scoped table must hold a seeded row,
#                                   or say why it is legitimately empty
#   hack/upgrade-rig.sh readsweep every door the served schemas expose must still
#                                 ANSWER — the wider, shallower question, and the
#                                 one a round trip of one's own writes cannot ask
#   hack/upgrade-rig.sh control   THE NEGATIVE CONTROLS: break the instance three
#                                 different ways and require the right check to
#                                 notice each, with the exact exit code for it
#   hack/upgrade-rig.sh all       up + upgrade + verify + readsweep + tablesweep + control
#   hack/upgrade-rig.sh down      delete the cluster and the rig's state
#
# `all` is the one worth running. `verify` on its own reports a pass from a check
# whose ability to FAIL has not been demonstrated in this session — which is the
# thing every rig in this directory exists to argue against.
#
# WHY THE BASELINE BUILDS ITS OWN dcctl
#
# 🔴 The working tree's dcctl embeds the working tree's CHART. Pointing it at the
# previous release's IMAGES would deploy a new chart around old binaries: the
# chart renders instance config the old services have never seen, and their typed
# configuration is fail-closed, so they reject it and crash-loop. That is not an
# upgrade — it is a combination no operator has ever run, failing for a reason
# that has nothing to do with the release.
#
# So the baseline half is built from the baseline TAG: its chart, its images, its
# CLI. `git archive` rather than a worktree, because the stash and worktree state
# of this repository is shared with other checkouts and a rig should not leave
# entries in it.
#
# 🔑 --version is passed EXPLICITLY and must be. A dcctl built from a tag by the
# makefile stamps its default image version from the repo-root VERSION file, which
# reads `0.0.1` and has for the project's whole history — a plausible-looking tag
# that was never published, so every workload would ImagePullBackOff several
# minutes into a bootstrap that looked healthy. Only a goreleaser build stamps the
# real tag, and this is not one.
#
# WHY THE UPGRADE IS RUN THE DOCUMENTED WAY
#
# The v0.11.0 drill found that the upgrade command the documentation gave could
# not work: Helm reuses a release's stored values only when an upgrade passes NONE
# of its own, so the single `--set image.tag=…` that IS the upgrade silently threw
# away everything bootstrap had generated — the root key, the cross-service auth
# secret, the NATS credential. The procedure was rewritten around
# `helm get values`, and this rig runs THAT, so the documented procedure and the
# tested one cannot drift apart.
#
# WHAT A GREEN RUN DOES NOT MEAN, and these matter:
#
#   - The operator is NOT upgraded. `helm upgrade` deploys the chart, and the
#     controller (backend/k8s) is installed by dcctl outside it — so this drill
#     leaves the baseline release's operator reconciling the new release's
#     services, which is exactly what an operator following the documented
#     procedure gets. If that is wrong, it is wrong in the docs too.
#   - Nothing here is upgraded UNDER LOAD. No device is connected, no telemetry
#     flows, nothing is in flight when the rollout happens. Zero-downtime is a
#     separate claim and this is not evidence for it.
#   - Entities the baseline release could not express are SKIPPED, by name, and
#     both the seed and the verify say which. A green run says nothing about them,
#     because there was never an old row to carry forward.
#   - Event history is not covered. This drill is about the control plane; the
#     event store's survival across a restore is the DR drill's subject.
#   - The read sweep asks whether a door ANSWERS, not whether it answers the same.
#     Values are verify's subject, and verify has a receipt from before the upgrade
#     to compare against; the sweep has nothing to compare against and does not
#     pretend otherwise. A door that returns a plausible WRONG answer is outside
#     both, and that residue is real.
#
# Requires: kind, kubectl, docker, helm, ko, git, curl, `tofu` OR `terraform` on
# PATH, and a Go toolchain (the rig builds dcctl, apiprobe and every service image
# itself). Two full image builds and a bring-up — budget an hour, and run `down`
# afterwards.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The release this drill upgrades FROM.
#
# 🔴 DERIVED, NOT WRITTEN DOWN, AND THE LITERAL IT REPLACES HAD ALREADY GONE STALE.
# This read `v0.11.0` — three releases back — because a default that has to be bumped
# by hand once per release is a default nobody bumps. The consequence is not loud: the
# seed measures the coverage table against the baseline's OWN served schemas and skips
# what that release could not express, so a local run drilled v0.11.0 → HEAD and
# silently left out every entity introduced since. v0.11.0 serves no `createGeoFence`
# at all, so a developer running this by hand covered no geofence — which is the exact
# subsystem the drill most recently failed to protect.
#
# It only ever misled LOCAL runs: upgrade-gate.yml derives its own baseline and never
# reads this. That is worse rather than better. It means the wrong value was in front
# of the person iterating on the rig and correct in the pipeline nobody watches, so
# nothing about a green CI run could ever contradict it.
#
# The rule is the one the workflow already uses, and stating it in both places is
# deliberate: they answer for different targets (the workflow knows the tag being
# released; this knows only the working tree) and a shared helper would have to be
# handed the difference anyway.
#
# `[-]` rather than `-- '-'`: the second spelling is a portability trap — GNU grep
# reads it as a pattern, at least one drop-in replacement reads it as no pattern and
# exits usage-error, which pipefail then turns into a failure nobody attributes to a
# grep argument. Same note as the workflow's.
newest_stable_tag() {
  git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -v '[-]' | sort -V | tail -1
}

# ⚠️ NOT resolved at assignment. `need_all` has not run yet, and a shallow or
# tag-less clone would otherwise make this empty at load time — turning every
# later use into a confusing blank rather than a refusal that says what is wrong.
baseline_tag="${DC_BASELINE_TAG:-}"

# resolve_baseline_tag fills in the derived default the first time a phase needs it,
# and REFUSES rather than guessing when the repository cannot answer.
resolve_baseline_tag() {
  [[ -z "$baseline_tag" ]] || return 0
  baseline_tag="$(newest_stable_tag)"
  [[ -n "$baseline_tag" ]] || fail "no stable vX.Y.Z tag is visible, so there is no release to
upgrade FROM. A shallow clone is the usual cause; pass DC_BASELINE_TAG explicitly to
drill a specific hop."
}

# NOT configurable: kind takes the cluster name from the top-level `name:` in the
# config file in preference to --name, so a variable here would name the context
# one thing and the cluster another. Change both or neither.
# The application database name, discovered from the server by rdb_database and used
# by psql_rdb. Declared here rather than made local so the control can resolve it
# once; it is empty until a phase that reaches Postgres sets it.
rdb_db=""

# The CNPG Cluster holding the relational store, and the tenant the coverage sweep
# writes under. A SECOND tenant, because the drill's own seed is baseline-constrained
# and this measurement must not be — see cmd_tablesweep.
rdb_cluster="${DC_RDB_CLUSTER:-dc-rdb}"
sweep_tenant="${DC_SWEEP_TENANT:-apiprobe-coverage}"

# Port-forward state. pf_pid is checked by stop_forward, which is called on every exit
# path out of cmd_tablesweep rather than from a trap: this script already installs an
# EXIT trap for the values file, and a second one would silently replace the first.
pf_pid=""

cluster="devicechain-upgrade"
kube_context="kind-$cluster"
kind_config="$repo_root/deploy/local/kind-cluster-upgrade.yaml"

# The instance name is NOT "default", for the reason the HA rig gives: dcctl keeps
# OpenTofu state per INSTANCE (~/.devicechain/<instance>/infra), not per cluster,
# so a shared name would reconcile this rig's recorded infrastructure against a
# developer's own cluster.
instance="${DC_INSTANCE:-upgrig}"

# The ingress the drill reaches the platform on. `--compact` implies plain HTTP,
# and the kind config maps the ingress onto 18081 — high enough that a local
# cluster's own 80/443 and the DR rig's 18080/18443 are both left alone.
api_server="localhost:18081"
api_scheme="http"

# The local registry. The name is fixed by the containerd mirror in the kind
# config, and it is the SAME container dcctl and deploy/local use — shared on
# purpose, so a developer box runs one registry rather than three.
registry="localhost:5000"
registry_container="kind-registry"
kind_network="kind"

# The registry the release pipeline publishes to. Read as a constant here rather
# than pulled from the chart: this is the registry an OPERATOR pulls from, and the
# rig asserting against the chart's current default would happily follow the chart
# somewhere else and still call the result a published-image drill.
published_registry="ghcr.io/devicechain-io"

# WHICH IMAGES THE UPGRADE HALF MOVES TO. Not a preference — the two modes answer
# different questions and only one of them is the release's claim.
#
#   build (default) — ko/docker-build the working tree into the local registry.
#                     Skew-free: chart, images and this script are one commit.
#                     The right mode for a developer testing an unreleased change.
#   pull            — upgrade to an ALREADY-PUBLISHED tag in ghcr. This is the
#                     release path, and it is the only mode that exercises the
#                     artifact an operator actually installs. A source build of
#                     the same commit is not the same bytes, was not produced by
#                     the release pipeline, and has never been pushed anywhere —
#                     so a green build-path drill says nothing about whether the
#                     images on ghcr can be upgraded onto.
#
# 🔴 THE BUILD PATH'S PER-RUN TAG IS LOAD-BEARING, AND THE PULL PATH IS EXEMPT FOR
# A REASON RATHER THAN BY OVERSIGHT. A reused tag lets the kubelet satisfy the new
# image reference from a layer it already holds: the workloads roll, report Ready,
# and run the PREVIOUS build — an upgrade drill that silently upgrades to the same
# code and passes every check it has. A tag that has never existed cannot be served
# from cache. The pull path needs no such defence: the node pulled $baseline_tag
# and is being asked for a DIFFERENT published tag, which no cached layer answers.
upgrade_images="${DC_UPGRADE_IMAGES:-build}"
upgrade_tag="${DC_UPGRADE_TAG:-}"

work="${DC_UPGRADE_WORK:-$HOME/.devicechain-upgrade-rig}"
baseline_src="$work/src"
baseline_dcctl="$baseline_src/backend/cli/build/dcctl"
# The WORKING TREE's dcctl — the one being released, and the only one that has the
# `upgrade` verb. Built into the repo's own gitignored build dir, which is where
# `make -C backend/cli build` puts it and where a developer already looks for it.
target_dcctl="$repo_root/backend/cli/build/dcctl"
apiprobe="$work/bin/apiprobe"
receipt="$work/receipt.json"
sweep_receipt="$work/receipt-coverage.json"
pf_log="$work/port-forward.log"
values_file="$work/values.yaml"
# What the upgrade moved TO — registry on line 1, tag on line 2. Written by
# `upgrade` and read by `operator`, so the operator check measures against the
# version this run actually deployed rather than against one it assumes.
target_file="$work/upgrade-target"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
# The exit code that means THE OPERATOR-SKEW FINDING and nothing else.
#
# 🔴 A drill whose every failure is `exit 1` makes a KNOWN, NAMED finding
# indistinguishable from a rig that could not run — and a workflow reading it then
# has to parse prose to tell "the release has a defect" from "the runner was out
# of disk". This drill has already been bitten once by the same shape in the other
# direction: an INCONCLUSIVE result was announced as data loss because every
# non-zero code shared one headline.
#
# 🔴 AND IT MUST NOT COLLIDE WITH apiprobe'"'"'S TAXONOMY, which this rig imports,
# prints and reasons about in the same log. The first spelling of this constant was
# 3 — which is apiprobe'"'"'s MISMATCH — so one run printed "verify exited 3, exactly
# as required" (a negative control HOLDING) five lines above the rig itself exiting
# 3 to mean something entirely different. Both were correct and the pair was
# unreadable. 20 sits clear of apiprobe'"'"'s range and below the shell'"'"'s own reserved
# codes (126+), and `selftest` asserts the separation against apiprobe'"'"'s source
# rather than trusting this comment to stay true.
exit_operator_skew=20

fail_code() {
  local code="$1"
  shift
  printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2
  exit "$code"
}

fail() { fail_code 1 "$@"; }

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
# ko_required — will this run shell out to a builder?
#
# 🔴 ko IS A BUILD-PATH TOOL, and demanding it unconditionally is not merely strict,
# it is WRONG. The pull path builds no image at all: the baseline installs published
# images and the upgrade moves to another published tag. A gate that skips installing
# ko because it does not need it, and is then refused by the tool list for not having
# it, fails on the one path a release actually runs. Measured, not imagined — that is
# exactly how the first pull-path run died, three seconds in.
#
# A predicate rather than a condition inlined in need_all, so `selftest` can ask the
# question without a cluster, a builder, or a doctored PATH. ⚠️ It is the RULE that
# is covered, not need_all's wiring to it — a mutation that stops need_all calling
# this at all survives the self-test, and only a run on a machine without ko would
# see it.
ko_required() { [[ "$upgrade_images" == build ]]; }

need_all() {
  # The baseline is resolved here rather than at load time, and `down` is the reason:
  # it is the one command that needs no baseline and the one you most want to work on
  # a repository that cannot answer. Resolving in the dispatch would make a shallow
  # clone unable to clean up after itself.
  resolve_baseline_tag

  # Every tool, checked BEFORE anything is provisioned. A missing binary
  # discovered twenty minutes into a bring-up is the posture this script rejects
  # everywhere else.
  for t in kind kubectl docker helm git curl go jq; do need "$t"; done
  ko_required && need ko
  command -v tofu >/dev/null 2>&1 || command -v terraform >/dev/null 2>&1 ||
    fail "one of tofu or terraform is required but neither is on PATH"
}

# validate_upgrade_mode checks the image-source choice BEFORE anything is
# provisioned, for the same reason need_all does: `all` spends forty minutes in
# `up` before it ever reaches `upgrade`, and discovering there that the run was
# told to pull a tag nobody named is forty minutes spent proving nothing.
validate_upgrade_mode() {
  case "$upgrade_images" in
  build) ;;
  pull)
    [[ -n "$upgrade_tag" ]] || fail "DC_UPGRADE_IMAGES=pull needs DC_UPGRADE_TAG — the published
tag to upgrade TO. Without it there is nothing to pull and no way to guess: the
working tree's VERSION file names a tag that was never published."

    # 🔴 Upgrading a release to ITSELF is not a drill, and it is the shape a
    # release pipeline reaches by accident: derive the baseline wrong by one step
    # and every check passes, having deployed the images that were already
    # running. helm would report a successful no-op upgrade and the probe would
    # read back rows nothing had touched.
    [[ "$upgrade_tag" != "$baseline_tag" ]] || fail "the baseline and the upgrade target are both
$baseline_tag. Upgrading a release to itself deploys the images already running,
so every check here would pass without an upgrade having happened. Set
DC_BASELINE_TAG to the release being upgraded FROM."

    # 🔴 THE CHART AND THE IMAGES HAVE TO BE THE SAME RELEASE. This is the target
    # half of the rule the baseline half already follows — the baseline is installed
    # by its OWN dcctl, carrying its OWN chart, for exactly this reason.
    #
    # `helm upgrade` here deploys the WORKING TREE's chart. Point that at some other
    # release's images and the new chart renders instance config those older binaries
    # have never seen; typed configuration is fail-closed, so they reject it and
    # crash-loop. That is not an upgrade — it is a combination no operator has ever
    # run, failing for a reason that has nothing to do with the release.
    #
    # MEASURED, not theorised: a run pairing HEAD's chart with v0.11.0's images spent
    # the full 20-minute `helm --wait` before dying on a crash-looping
    # device-management. Twenty minutes to learn what a tree hash answers instantly.
    #
    # 🔴 An unreachable tag is REFUSED, not skipped. Waving the comparison through
    # when the tag is unknown fails OPEN in exactly the case the guard can reason
    # about least, and makes "checked and agreed" indistinguishable from "could not
    # look". The release pipeline always has the tag — it checks the tag out.
    git -C "$repo_root" rev-parse --verify "$upgrade_tag^{commit}" >/dev/null 2>&1 ||
      fail "$upgrade_tag is not a tag in this repository, so this rig cannot check that the
chart it is about to deploy belongs to the same release as the images. Fetch the tag
(the release pipeline checks it out), or drill the build path instead."

    # Compared as the CHART's tree, not as the commit. A commit test would refuse a
    # branch that has not touched the chart at all, whose deployment of it is
    # byte-for-byte what the release ships — a legitimate drill, and the only way
    # this path gets exercised outside a tag checkout.
    # 🔴 --verify, and it is not decoration. Bare `git rev-parse <rev>:<path>` on a
    # path the tree does not contain does NOT fail — it ECHOES ITS ARGUMENT BACK, so
    # `there` comes out as the literal string "sometag:deploy/helm/devicechain",
    # non-empty, and the emptiness check below can never fire. A tag carrying no
    # chart at all was then reported as "the chart and the images are different
    # releases", which sends a reader looking for a version mismatch that does not
    # exist. Found by the self-test case written to cover exactly this.
    local here there
    here="$(git -C "$repo_root" rev-parse --verify -q "HEAD:deploy/helm/devicechain" 2>/dev/null || true)"
    there="$(git -C "$repo_root" rev-parse --verify -q "$upgrade_tag:deploy/helm/devicechain" 2>/dev/null || true)"
    [[ -n "$here" && -n "$there" ]] ||
      fail "could not read the chart tree for HEAD and/or $upgrade_tag, so this rig cannot
check that they agree. It refuses rather than guesses: the combination it guards against
fails twenty minutes later, as a rollout timeout that reads like a platform defect."
    [[ "$here" == "$there" ]] || fail "the chart and the images are different releases.
  chart at HEAD:          $here
  chart at $upgrade_tag:  $there
This upgrade would deploy the working tree's chart around $upgrade_tag's binaries. The new
chart renders instance config those binaries have never seen, their typed config is
fail-closed, and they crash-loop — a combination no operator has ever run, failing for a
reason that has nothing to do with the release. Check out $upgrade_tag (which is what the
release pipeline does), or drill the build path instead."
    ;;
  *) fail "DC_UPGRADE_IMAGES must be 'build' or 'pull'; got '$upgrade_images'" ;;
  esac
}

# The values file carries the instance root key and every generated credential.
# It exists only for the seconds between `helm get values` and `helm upgrade`, and
# it is removed however the run ends — including on a failed upgrade, where the
# temptation to leave it "for debugging" is exactly how a root key ends up in a
# home directory for a year.
trap 'rm -f "$values_file"' EXIT

# ---------------------------------------------------------------------------
# building
# ---------------------------------------------------------------------------

# load_exit_codes imports apiprobe's exit-code taxonomy rather than repeating it.
#
# The codes are the interface between the tool and this script, and an interface
# written out as bare literals in shell is not one: renumbering exitMissing in Go
# would leave every Go test green, every build clean, and this script's negative
# controls reporting INCONCLUSIVE forever — which reads as an environment problem
# rather than as a rig that has stopped checking anything.
load_exit_codes() {
  local defs
  defs="$("$apiprobe" codes)" || fail "apiprobe could not report its exit codes"
  eval "$defs"
  # 🔴 Every code this script NAMES, checked. The first two are compared against
  # in cmd_control, and an unset one expands to the empty string — which no exit
  # code ever equals, so every branch would fall through to INCONCLUSIVE, silently
  # and forever. The other two are only printed, but a diagnosis that reads
  # "exit  means the schema moved" is a rig quietly telling an operator nothing at
  # the exact moment they most need it to be precise.
  local code var
  for code in MISSING MISMATCH SHAPE SETUP UNREADABLE COVERAGE; do
    var="APIPROBE_EXIT_$code"
    [[ -n "${!var:-}" ]] ||
      fail "apiprobe reported no $code exit code; this rig names it and cannot run without it"
  done
}

build_apiprobe() {
  say "building apiprobe from the working tree"
  mkdir -p "$work/bin"
  (cd "$repo_root/backend/tools/apiprobe" && go build -o "$apiprobe" .)
  [[ -x "$apiprobe" ]] || fail "apiprobe was not built at $apiprobe"
}

# extract_baseline lays the baseline release's source tree out under $work.
#
# `git archive` and not `git worktree add`: a worktree writes into the shared .git
# of a repository this checkout may not be the only user of, and a rig killed
# halfway would leave an entry behind for someone else to find. An archive is a
# plain directory that `down` can delete.
#
# It is not a git repository, so the CLI makefile's `git describe` and
# `git status` calls print a "not a git repository" line and fall back to their
# unknown values. That is cosmetic — the only stamped value this rig depends on is
# the image version, and that is passed explicitly for the reason in the header.
extract_baseline() {
  if [[ -f "$baseline_src/VERSION" ]]; then
    say "reusing the extracted $baseline_tag tree at $baseline_src"
    return
  fi
  git -C "$repo_root" rev-parse --verify "$baseline_tag^{commit}" >/dev/null 2>&1 ||
    fail "$baseline_tag is not a tag in this repository; nothing to upgrade FROM"
  say "extracting $baseline_tag to $baseline_src"
  mkdir -p "$baseline_src"
  git -C "$repo_root" archive --format=tar "$baseline_tag" | tar -x -C "$baseline_src"
  [[ -f "$baseline_src/backend/cli/Makefile" ]] ||
    fail "the extracted $baseline_tag tree has no backend/cli; this rig cannot build its dcctl"
  repoint_baseline_chart_source
}

# repoint_baseline_chart_source rewrites the CloudNativePG chart source in the
# EXTRACTED BASELINE to the OCI registry, because every release up to and including
# v0.13.0 fetches it over http from a domain that no longer resolves.
#
# 🔴 WHY THIS IS NOT CHEATING, AND WHERE THE LINE IS. The drill exists to ask "does
# an existing instance's DATA survive this release?". It cannot ask that if the
# baseline will not install at all -- and on 2026-09-02 it would not: upstream
# pointed cloudnative-pg.github.io/charts at a 301 to cloudnative-pg.io, then that
# domain began answering SERVFAIL, resolver-dependent and intermittent. The release
# run for v0.14.0-rc.1 died here, in `tofu apply` on the BASELINE, with a DNS
# timeout -- nothing to do with the release under test. Patching the chart SOURCE
# leaves every byte of the baseline's schema, models, API and chart untouched, so
# the question the drill asks is unchanged; refusing to patch it just means the
# drill reports an upstream outage as a failure of this release.
#
# 🔑 It is honest here for a reason particular to this rig: the baseline is already
# BUILT FROM SOURCE by build_baseline_dcctl, not downloaded as the released binary.
# Byte-identity with the shipped artifact was never a property of this drill, so
# this changes the kind of edit, not the kind of thing being tested. (Contrast
# `build: false` on the RELEASE path, which is load-bearing precisely because there
# the images must be the published ones.)
#
# Self-limiting by construction: it rewrites only the dead URL, so a baseline of
# v0.14.0 or later -- which already uses OCI -- matches nothing and is left alone.
# That is why finding no occurrences is reported and NOT a failure.
repoint_baseline_chart_source() {
  local dead="https://cloudnative-pg.github.io/charts"
  local live="oci://ghcr.io/cloudnative-pg/charts"

  # 🔴 SCOPED TO deploy/, WHICH IS THE ONLY PART THAT RUNS. dcctl embeds the
  # OpenTofu root from there; the baseline's hack/ scripts are never executed by
  # this drill, so rewriting them would change nothing and claim something.
  #
  # It also stops the function editing ITSELF. A whole-tree scan matches
  # hack/upgrade-rig.sh, because the retired URL appears in this very function as a
  # string literal -- so on a v0.14.0+ baseline, where there is genuinely nothing to
  # do, it reported "1 file repointed" and rewrote `dead` to equal `live` in the
  # extracted copy. Harmless (that copy never runs) but it is a false report, and a
  # false report is what this rig exists not to produce.
  local scope="$baseline_src/deploy"
  [[ -d "$scope" ]] ||
    fail "the extracted $baseline_tag tree has no deploy/; dcctl embeds its OpenTofu from there"

  local -a hits
  mapfile -t hits < <(grep -rl -- "$dead" "$scope" 2>/dev/null || true)

  if [[ ${#hits[@]} -eq 0 ]]; then
    note "$baseline_tag does not reference the retired chart host; nothing to repoint"
    return
  fi

  say "repointing $baseline_tag's CNPG chart source to $live (${#hits[@]} file(s))"
  local f
  for f in "${hits[@]}"; do
    sed -i "s|$dead|$live|g" "$f"
    note "  ${f#"$baseline_src"/}"
  done

  # 🔴 A sed that matched nothing exits 0. Re-read the tree and prove the string is
  # gone, or the drill proceeds to fail exactly as it did before with a rig that
  # claims it fixed something.
  #
  # 🔴🔴 THE CHECK ITSELF MUST NOT BE THE THING THAT KILLS THE RIG. `grep -rq` exits
  # 1 when it finds NOTHING, which here is the SUCCESS case -- so under
  # `set -euo pipefail` a bare `grep` (or `grep | wc -l`, where pipefail propagates
  # the same 1) aborts the run at the exact moment the repointing worked. That is
  # not hypothetical: it is what this function did on its first CI run, exiting 1 one
  # second after correctly rewriting both files. The status is consumed by the `if`
  # so a "not found" result reaches the code that wants it rather than `set -e`.
  if grep -rq -- "$dead" "$scope" 2>/dev/null; then
    local -a left
    mapfile -t left < <(grep -rl -- "$dead" "$scope" 2>/dev/null || true)
    fail "repointing left ${#left[@]} file(s) still naming $dead (${left[*]}); the drill would fail on an upstream outage rather than on this release"
  fi
}

build_baseline_dcctl() {
  say "building $baseline_tag's own dcctl (its chart, not the working tree's)"
  make -C "$baseline_src/backend/cli" build >/dev/null
  [[ -x "$baseline_dcctl" ]] || fail "the $baseline_tag dcctl was not built at $baseline_dcctl"
  note "$("$baseline_dcctl" version | head -1)"
}

# build_target_dcctl builds the dcctl the upgrade is moving TO, which is the one
# that carries `dcctl upgrade`.
#
# 🔴 IT MUST BE THE TARGET'S dcctl AND NOT THE BASELINE'S, for a reason that has
# nothing to do with the verb existing. `dcctl upgrade` renders the operator
# overlay EMBEDDED IN THE BINARY — CRDs, RBAC and the controller — so the binary
# is the source of those manifests, not just the thing that applies them.
# Upgrading with the baseline's dcctl would move the image tag while re-applying
# the OLD overlay, which is a subtler version of the defect this whole check
# exists to catch: an operator that looks upgraded and a CRD set that is not.
#
# On the pull path the working tree IS the tag being released (the release
# workflow checks out that commit), so this is the released dcctl in both modes.
build_target_dcctl() {
  say "building the working tree's dcctl (it carries \`dcctl upgrade\`)"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$target_dcctl" ]] || fail "the working tree's dcctl was not built at $target_dcctl"
  note "$("$target_dcctl" version | head -1)"
}

# pin_baseline_charts gives the BASELINE install the third-party chart versions the
# working tree pins, for any chart the baseline itself leaves unpinned.
#
# 🔴 WITHOUT THIS THE DRILL IS NOT REPRODUCIBLE, AND IT FAILS IN A WAY THAT NAMES
# NOTHING. v0.11.0 declares nats, ingress-nginx and cert-manager as `default = ""`
# — "empty installs latest" — and its module renders that as
# `version = var.chart_version != "" ? var.chart_version : null`. A null version is
# UNKNOWN at plan time and resolved from the chart repository during apply, so a
# repo hiccup surfaces as the helm provider's:
#
#   Provider produced inconsistent final plan ... .version: was known, but now unknown
#
# which mentions neither the chart nor the network. It cost this rig one full run:
# the first attempt installed cleanly and the second died there, same commit, same
# cluster config, twenty minutes apart. hack/check-chart-pins.sh is the gate that
# stops this at HEAD, and the pins it enforces are what this function reads — but a
# gate on the working tree cannot reach backwards into a release that already
# shipped, and the rig installs that release's own OpenTofu on purpose.
#
# Only the UNPINNED charts are overridden. A chart the baseline pins deliberately
# keeps its own value, because changing it would silently alter what the drill
# installs; the ones changed here had no value to alter. The side effect is
# desirable in its own right: the upgrade then moves the platform's images without
# moving any third-party chart underneath them, which is the variable this drill
# exists to isolate.
#
# TF_VAR_* is honoured because dcctl passes no `-var` for these (see infraVars) —
# an explicit -var would outrank the environment.
pin_baseline_charts() {
  local var pin baseline_default pinned=()
  for var in nats_chart_version ingress_nginx_chart_version cert_manager_chart_version; do
    # The value the WORKING TREE pins, read out of the tree rather than repeated
    # here, so a deliberate bump at HEAD carries into the rig instead of drifting
    # away from it behind a second copy nobody remembers to edit.
    pin="$(tofu_default "$repo_root/deploy/opentofu/variables.tf" "$var")"
    [[ -n "$pin" ]] || fail "the working tree declares no default for $var, so this rig
cannot pin the baseline's copy of that chart. Either the variable was renamed or the
pin was removed — hack/check-chart-pins.sh is the authority on the second."

    baseline_default="$(tofu_default "$baseline_src/deploy/opentofu/variables.tf" "$var")"
    if [[ -n "$baseline_default" ]]; then
      note "$baseline_tag pins $var itself ($baseline_default); left alone"
      continue
    fi
    export "TF_VAR_$var=$pin"
    pinned+=("$var=$pin")
  done

  if [[ ${#pinned[@]} -gt 0 ]]; then
    say "pinning charts $baseline_tag left unpinned: ${pinned[*]}"
  else
    note "$baseline_tag pins every chart itself; nothing to override"
  fi
}

# tofu_default reads a variable's `default` out of an OpenTofu variables file. A
# quoted empty string reads as empty, which is exactly the case this rig treats as
# "unpinned" — so the caller cannot tell an absent variable from an empty one, and
# checks for the absent case itself.
tofu_default() {
  local file="$1" name="$2"
  [[ -f "$file" ]] || return 0
  awk -v name="$name" '
    $0 ~ "^variable \"" name "\" \\{" { inside = 1; next }
    inside && /^}/                    { exit }
    inside && $1 == "default"         { sub(/^[^=]*=[[:space:]]*/, ""); gsub(/"/, ""); print; exit }
  ' "$file"
}

# ensure_registry starts the local registry the NEW images are published to.
#
# dcctl creates this itself on its --build path, but this rig's baseline install
# uses PUBLISHED images and so never takes that path — the registry has to exist
# before the upgrade regardless, and creating it here means the failure to create
# it lands before an hour of cluster time rather than after.
ensure_registry() {
  local running port
  # Derived, never written twice. The port appears in three places that must
  # agree — this publish, the image references the chart is given, and the
  # containerd mirror in the kind config — and two of them are out of reach from
  # here, so at least the two that are in reach come from one value.
  port="${registry##*:}"
  running="$(docker inspect -f '{{.State.Running}}' "$registry_container" 2>/dev/null || true)"
  if [[ "$running" != "true" ]]; then
    say "starting the local registry container $registry_container"
    docker rm -f "$registry_container" >/dev/null 2>&1 || true
    docker run -d --restart=always -p "127.0.0.1:$port:5000" \
      --name "$registry_container" registry:2 >/dev/null
  fi
  # Idempotent: ignore "already exists in network".
  docker network connect "$kind_network" "$registry_container" >/dev/null 2>&1 || true
}

create_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
    say "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster"
  kind create cluster --name "$cluster" --config "$kind_config" --wait 120s
}

# wait_for_api blocks until the ingress actually routes to a live backend.
#
# Borrowed verbatim in spirit from the DR rig, including the two traps it paid
# for: `|| true` rather than `|| echo 000` (curl already prints 000 on a dead
# connection, and appending a second one yields "000\n000", which matches no case
# and reads as an answer), and treating 404 as a RETRY, because ingress-nginx
# serves its default backend until the route is admitted.
#
# It matters twice as much here as in a bring-up rig: after `helm upgrade` the old
# pods are still terminating, and a verify that raced the rollout would read the
# OLD version and report a pass that means nothing.
wait_for_api() {
  local area="$1" url code waited=0
  url="$api_scheme://$api_server/api/$area/graphql"
  while true; do
    code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 \
      -X POST "$url" -H 'Content-Type: application/json' \
      -d '{"query":"{__typename}"}' 2>/dev/null || true)"
    case "${code:-000}" in
      000 | 404 | 502 | 503 | 504) ;;
      *) return 0 ;;
    esac
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the $area API never became reachable at $url (last status $code)"
    sleep 5
  done
}

wait_for_every_api() {
  local area waited=0
  say "waiting for the instance API to route"
  wait_for_api user-management
  while read -r area; do
    [[ -n "$area" ]] || continue
    wait_for_api "$area"
    waited=$((waited + 1))
  done < <("$apiprobe" areas)
  # An empty list would make this wait for user-management alone and return — a
  # wait that looks like it covered the instance and covered one service. The
  # areas come from a subprocess, and a subprocess that dies produces no lines
  # rather than an error the loop can see.
  [[ $waited -gt 0 ]] || fail "apiprobe named no functional areas, so nothing beyond
user-management was waited for; this rig cannot tell a ready instance from an
unreachable one."
}

# ---------------------------------------------------------------------------
# up — the baseline release, and the rows it can hold
# ---------------------------------------------------------------------------

cmd_up() {
  need_all
  mkdir -p "$work"
  chmod 700 "$work"
  build_apiprobe
  extract_baseline
  build_baseline_dcctl
  create_cluster
  ensure_registry

  # Every area the coverage table writes to, ASKED FOR rather than assumed.
  #
  # 🔴 outbound-connectors is held out of the `default` profile, so a stock
  # install serves no route for it — and the connector row would be refused by an
  # ingress that has nothing behind it, reported as though the API had declined
  # the request. Reading the list from apiprobe means the day the table gains
  # another opt-in area, this keeps working.
  local area area_args=()
  while read -r area; do
    [[ -n "$area" ]] || continue
    area_args+=(--enable-area "$area")
  done < <("$apiprobe" areas)
  [[ ${#area_args[@]} -gt 0 ]] || fail "apiprobe named no functional areas; the seed would have nothing to write to"

  pin_baseline_charts

  say "installing $baseline_tag with its own dcctl"
  note "areas beyond the default profile are requested explicitly: ${area_args[*]}"
  # --no-escrow: a throwaway instance destroyed by `down`, and the flag's own
  # documentation names exactly this case. It also keeps the run non-interactive,
  # which a passphrase prompt would not.
  "$baseline_dcctl" bootstrap local "$instance" --yes --compact \
    --kube-context "$kube_context" --host localhost --no-escrow \
    --version "$baseline_tag" "${area_args[@]}"

  wait_for_every_api

  say "seeding one of every entity $baseline_tag can express"
  # --baseline-schemas is what makes this possible at all: apiprobe's table is
  # built from the working tree and knows entities this release never had, so it
  # measures the table against the release's OWN served schemas and skips what
  # cannot be written, by name. Without it the seed dies part-way through with a
  # refusal that looks like a platform defect.
  "$apiprobe" seed --instance "$instance" --receipt "$receipt" \
    --server "$api_server" --scheme "$api_scheme" \
    --baseline-schemas "$baseline_src/backend/services" ||
    fail "seeding $baseline_tag failed (apiprobe exit $?). Nothing was upgraded, so this
says nothing about the release under test — it says the baseline install could not
be written to."

  [[ -s "$receipt" ]] || fail "the seed reported success but wrote no receipt at $receipt"
  say "BASELINE SEEDED — $baseline_tag is installed and holds the rows the upgrade must carry"
}

# ---------------------------------------------------------------------------
# upgrade — the documented procedure, verbatim in shape
# ---------------------------------------------------------------------------

cmd_upgrade() {
  need_all
  [[ -s "$receipt" ]] || fail "$receipt is missing or empty; run 'up' first"
  build_apiprobe

  # Where the upgrade is moving TO. The two modes and why the per-run tag is
  # load-bearing on one of them and unnecessary on the other are documented at
  # `upgrade_images` above; this is only the plumbing.
  validate_upgrade_mode
  local target_registry target_tag
  if [[ "$upgrade_images" == pull ]]; then
    target_registry="$published_registry"
    target_tag="$upgrade_tag"
    say "upgrading to the PUBLISHED images $target_registry/*:$target_tag"
    note "nothing is built here — these are the bytes an operator installs"
    # 🔴 The in-cluster pull is ANONYMOUS. Every one of this tag's ghcr packages
    # has to be public or the rollout dies in ImagePullBackOff — which `helm
    # --wait` reports as a plain timeout, indistinguishable from a workload that
    # crash-looped on the new config. Named here because the release that adds a
    # NEW service module pushes it private by default, and this is where that
    # first shows up.
    note "the pull is anonymous — every package for $target_tag must be public"
  else
    target_registry="$registry"
    target_tag="head-$(date -u +%Y%m%d%H%M%S)"
    say "building the working tree's images → $target_registry (tag $target_tag)"
    REGISTRY="$target_registry" TAG="$target_tag" "$repo_root/deploy/local/build-images.sh"
  fi
  printf '%s\n%s\n' "$target_registry" "$target_tag" >"$target_file"

  # THE DOCUMENTED PROCEDURE. docs/deployment/releases-and-upgrades tells an
  # operator to write the release's values out and pass them back with -f, because
  # Helm reuses stored values ONLY when an upgrade passes none of its own — and the
  # one `--set` that changes the version is enough to throw away everything
  # bootstrap generated. Running it here is what keeps the documented procedure and
  # the tested one from drifting apart.
  say "carrying the release's values forward"
  rm -f "$values_file"
  (
    umask 077
    helm --kube-context "$kube_context" get values dc -n default -o yaml >"$values_file"
  )
  # `helm get values` prints "null" for a release with none, and an empty or null
  # file passed with -f would render the chart from its DEFAULTS — the exact
  # failure the procedure exists to prevent, arrived at by a different road. The
  # root key is checked by name because it is the value whose loss the chart
  # refuses to render without, and therefore the one this file exists to carry.
  [[ -s "$values_file" ]] || fail "helm get values wrote nothing; there is no release 'dc' to upgrade"
  grep -q 'rootKey' "$values_file" ||
    fail "the values carried forward hold no instance root key. Upgrading with these
would render the chart from its defaults and lose every generated credential —
which is the exact failure this procedure exists to prevent."

  say "helm upgrade → the working tree's chart and images"
  helm --kube-context "$kube_context" upgrade dc "$repo_root/deploy/helm/devicechain" \
    -n default -f "$values_file" \
    --set image.registry="$target_registry" --set image.tag="$target_tag" \
    --wait --timeout 20m ||
    fail "the upgrade itself FAILED. This is a finding: an operator on $baseline_tag
running the documented procedure would see exactly this. Read helm's output above —
a render error names the value the new chart requires and the old release does not
carry; a wait timeout means a workload never became ready, and its logs will say
whether it was the migration or the config."

  # Deleted HERE, not left to the EXIT trap. The trap is the backstop for a run
  # that dies mid-upgrade; on the happy path `all` continues into verify and
  # control, and leaving the root key on disk for the rest of a drill is not what
  # "it exists for the seconds between two commands" describes.
  rm -f "$values_file"

  # The upgrade's exit status is not evidence that the API is serving again:
  # --wait returns when the workloads report Ready, and the ingress still has to
  # pick the new pods up as healthy upstreams.
  wait_for_every_api

  # THE SECOND HALF OF THE DOCUMENTED PROCEDURE. `helm upgrade` cannot reach the
  # operator — it is not in the chart — so an upgrade that stops at the line above
  # leaves the cluster on the controller it was bootstrapped with. That was a real
  # release defect, found by this drill; `dcctl upgrade` is the fix and running it
  # here is what keeps the documented procedure and the tested one identical.
  #
  # 🔴 IT IS RUN HERE, NOT IN `cmd_operator`. The operator check must MEASURE, and
  # a check that performed the upgrade it then asserts would pass unconditionally
  # — the same instrument-reporting-on-itself shape this rig has already been bitten
  # by. Keeping the action in `upgrade` and the assertion in `operator` is what
  # makes the assertion capable of failing.
  build_target_dcctl
  say "dcctl upgrade → moving the operator (CRDs + RBAC + controller) to $target_tag"
  "$target_dcctl" upgrade local "$instance" \
    --kube-context "$kube_context" \
    --registry "$target_registry" --version "$target_tag" ||
    fail "\`dcctl upgrade\` FAILED. The services are now on $target_tag and the operator
is not, which is the exact state this drill exists to refuse. An operator running
the documented procedure would be here too. Read the output above: an image that
cannot be pulled leaves the controller crash-looping on the new tag, while an
apply error names the object the cluster refused."

  say "UPGRADED — $target_registry/*:$target_tag is serving, from the release's own values"
}

# ---------------------------------------------------------------------------
# reaching Postgres — for ONE control, and deliberately for no phase
# ---------------------------------------------------------------------------
#
# Every other thing this rig does goes through the platform's own API, on purpose:
# what an operator gets is what a client can read, and a drill that reads around
# the API measures something nobody uses. The exception is the READ-SWEEP CONTROL,
# and it has to be an exception.
#
# 🔴 THE DAMAGE THAT CONTROL NEEDS CANNOT BE DONE THROUGH THE API. It is the exact
# state a v0.12.x instance was left in by the release that shipped the geometry
# archive without its backfill: fence-set snapshots that name their geometry by a
# content address, holding NO address. No mutation produces that — the platform
# only ever writes the current shape — and no fixture reproduces it, because the
# whole point is that it is what the PREVIOUS release wrote. So the control writes
# it directly, and the sweep is then asked to notice.
#
# `-U postgres` over the pod's own socket: peer authentication inside the
# container, so this needs no credential out of the instance Secret. The DR drill
# reaches its stores the same way for its WAL switch.

# pg_pod resolves the relational Postgres pod through the `dc-postgresql` SERVICE,
# not through a pod label. Ported from dr-rig.sh, where the reasoning is recorded
# at length: the label this would otherwise select on was authored by the OpenTofu
# module for the old single-node StatefulSet, and the CloudNativePG topology
# carries `cnpg.io/*` labels instead — so a label lookup breaks on a topology
# change that is invisible from here, while the service name is what both
# topologies agree on.
#
# `|| fail` inside the command substitution is load-bearing for the same reason it
# is there: bash suppresses errexit for the dynamic extent of `$(...)`, so a jq
# failure yields an EMPTY STRING, which the refusal below would otherwise report as
# "no ready pod" — a parse failure wearing the costume of an absent object.
pg_pod() {
  local slices pod
  slices="$(kubectl --context "$kube_context" -n dc-system get endpointslices \
    -l "kubernetes.io/service-name=dc-postgresql" -o json)" ||
    fail "could not list endpointslices for the dc-postgresql service"
  # `.conditions.ready != false` rather than `== true`: the EndpointSlice API says
  # a nil `ready` must be read as ready.
  pod="$(printf '%s' "$slices" |
    jq -r 'first(.items[].endpoints[]
             | select(.conditions.ready != false)
             | .targetRef.name // empty)')" ||
    fail "could not parse the endpointslices for dc-postgresql (jq failed)"
  if [[ -z "$pod" || "$pod" == "null" ]]; then
    fail "no READY pod backs dc-postgresql in dc-system; the control cannot be armed,
which says nothing about the sweep in either direction."
  fi
  printf '%s' "$pod"
}

# rdb_database finds the database HOLDING A NAMED TABLE, and refuses anything but
# exactly one.
#
# 🔴 THE OBVIOUS VERSION IS WRONG, AND IT WAS WRITTEN FIRST: "the first non-template
# database that is not `postgres`". On the very first real instance this ran against,
# that picked `devicechain` — an empty database that merely sorts before `upgrig`,
# which is where the platform actually lives. The whole control would then have armed
# nothing, in a database with no tables at all, and reported that the sweep had failed
# to notice.
#
# So it asks the question whose answer cannot be a coincidence: which database contains
# this table. Zero and two are both refusals, because both mean the assumption behind
# the query is wrong and neither can be resolved by picking one.
#
# ⚠️ The database is NOT named after the product. It is named after the INSTANCE, so it
# moves with $instance and with whatever a future chart decides — which is the second
# reason not to write a name down here.
rdb_database() {
  local pod="$1" table="$2" all d hits=() n
  local dbs=()
  all="$(kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
    psql -U postgres -d postgres -Atqc \
    "SELECT datname FROM pg_database WHERE datistemplate = false AND datname <> 'postgres'")" ||
    fail "could not list databases on $pod"

  # 🔴 mapfile, NOT `while read … <<<"$all"`, AND THE DIFFERENCE IS A LIVE FINDING.
  # `kubectl exec -i` forwards STDIN, and inside a while-read loop the stdin it
  # forwards is the loop's own input — so the first iteration swallowed the rest of
  # the database list and the loop ended after one. On the first cluster run of this
  # control that meant only `devicechain` was ever examined, `upgrig` (where the
  # platform actually lives) was never reached, and the rig reported that NO database
  # held the table. mapfile consumes the here-string itself, so the loop body has no
  # stdin to eat.
  #
  # 🔑 It failed LOUDLY, which is the only reason it took minutes rather than a
  # release: the refusal below asks "which database holds this table" and answers
  # zero, instead of a heuristic quietly picking the one database it managed to see.
  mapfile -t dbs <<<"$all"
  for d in "${dbs[@]}"; do
    [[ -n "$d" ]] || continue
    n="$(kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
      psql -U postgres -d "$d" -Atqc \
      "SELECT count(*) FROM information_schema.tables WHERE table_name = '$table'")" || continue
    [[ "$n" == "0" ]] || hits+=("$d")
  done

  if [[ ${#hits[@]} -eq 0 ]]; then
    fail "no database on $pod holds a table named $table.
Databases seen: $(printf '%s' "$all" | tr '\n' ' '). The control cannot be armed, which
says nothing about the sweep in either direction."
  fi
  if [[ ${#hits[@]} -gt 1 ]]; then
    fail "${#hits[@]} databases on $pod hold a table named $table (${hits[*]}).
Picking one would be a guess, and a guess that arms a control in the wrong database
reports a finding about the sweep that is really a fact about this script."
  fi
  printf '%s' "${hits[0]}"
}

# psql_rdb runs one statement against the platform database and REFUSES a silent
# no-op.
#
# 🔴 `UPDATE ... WHERE <no row matches>` exits 0 and prints `UPDATE 0`. A control
# armed that way damages nothing, the sweep then passes, and the run reports that
# the control did not hold — a conclusion about the sweep drawn from a fact about
# the WHERE clause. So the caller states how many rows it expects to touch, and a
# different number stops the rig where the mistake is.
psql_rdb() {
  local pod="$1" want="$2" sql="$3" out
  out="$(kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
    psql -U postgres -d "$rdb_db" -Atqc "$sql")" ||
    fail "psql failed on $pod: $sql"
  if [[ -n "$want" && "$out" != "$want" ]]; then
    fail "the control's own SQL did not do what it claimed: expected '$want', got '$out'.
Nothing is proved in either direction — this is the rig failing to set up its own
control, not a finding about the platform."
  fi
  printf '%s' "$out"
}

# ---------------------------------------------------------------------------
# verify — the positive result
# ---------------------------------------------------------------------------

# run_verify runs the probe and RETURNS its exit code without deciding what the
# code means. The same code is a pass in one phase and a failure in another, so
# the caller decides — the DR drill learned this the same way.
run_verify() {
  local rc=0
  "$apiprobe" verify --receipt "$receipt" \
    --server "$api_server" --scheme "$api_scheme" || rc=$?
  return "$rc"
}

cmd_verify() {
  need_all
  [[ -s "$receipt" ]] || fail "$receipt is missing or empty; run 'up' then 'upgrade' first"
  build_apiprobe
  load_exit_codes

  say "THE DRILL — did every row written on $baseline_tag survive the upgrade?"
  local rc=0
  run_verify || rc=$?

  # 🔴 INCONCLUSIVE IS NOT A FINDING, and this rig used to say it was: every
  # non-zero code got the same "rows did NOT survive" headline, including the one
  # whose own legend two lines below read "says nothing either way". That was
  # measured — a receipt written by a different build of apiprobe exited SETUP and
  # was announced as data loss, complete with the failure taxonomy.
  #
  # It matters most exactly where this rig is heading. A blocking gate that
  # reports "the upgrade lost data" when it could not run gets a release stopped
  # for a reason nobody can act on, and teaches everyone to read past the headline
  # — which is the same as not having one.
  #
  # The taxonomy is imported (load_exit_codes) rather than repeated so this cannot
  # drift from apiprobe's own codes. Having imported it, BRANCH on it.
  if [[ $rc -eq $APIPROBE_EXIT_SETUP ]]; then
    fail "THE DRILL COULD NOT RUN, and this says NOTHING about the upgrade either way
(apiprobe exit $rc = SETUP). No claim is made here: no row was shown to survive, and
no row was shown to be lost. Read apiprobe's output above for what stopped it — a
receipt from another build, an unreachable API, a tenant that could not be resolved.
Fix that and run 'verify' again; the cluster is still up and still upgraded."
  fi

  if [[ $rc -ne 0 ]]; then
    fail "rows written on $baseline_tag did NOT survive the upgrade (apiprobe exit $rc).
Read apiprobe's output above, and note that the code says which KIND of defect:
  $APIPROBE_EXIT_MISSING  a row is GONE — a migration dropped data, and a schema-only diff cannot see it
  $APIPROBE_EXIT_MISMATCH a field CHANGED — a migration rewrote data, which is the failure this drill exists for
  $APIPROBE_EXIT_SHAPE  the QUERY was rejected — the schema moved; the row may be perfectly intact"
  fi
  say "DRILL PASSED — every row written by $baseline_tag reads back unchanged from the working tree"
}

# ---------------------------------------------------------------------------
# readsweep — the half a round trip cannot reach
# ---------------------------------------------------------------------------
#
# verify asks 24 questions, and every one of them is "fetch the row I just wrote,
# by its token". Against the served schemas that is 24 of about 124 query fields.
# So its guarantee is the narrow one — EVERY ROW YOU WROTE READS BACK UNCHANGED —
# and everything the platform DERIVES from a write sits outside it.
#
# 🔴 THAT IS NOT A HYPOTHETICAL GAP; THIS DRILL HAS ALREADY PASSED THROUGH IT. The
# release that shipped content-addressed fence geometry shipped no backfill for the
# snapshots already stored, so an upgraded instance stopped matching geofence rules.
# The drill seeded a fence (it has, since v0.12.1), the seed minted a fence-set
# version, the upgrade left that version pointing at nothing — and the fence ROW was
# untouched, so verify read it back perfectly and the run was green. Adding another
# entity to the table would not have changed that. A round trip of one's own writes
# cannot see a derived artifact go stale.
#
# So this phase asks the OTHER question: does everything the platform serves about
# these rows still answer at all? Not whether values are unchanged — verify owns
# that, and it owns it better, having a receipt from before the upgrade to compare
# against. Just that no door ERRORS. It is a low bar that a snapshot hydrating
# through rows nobody migrated cannot clear.
#
# The doors are derived from the SERVED SDL rather than listed, because the listed
# half is the half that was complete and still missed this.

run_readsweep() {
  local rc=0
  "$apiprobe" readsweep --receipt "$receipt" --schemas "$repo_root/backend/services" \
    --server "$api_server" --scheme "$api_scheme" || rc=$?
  return "$rc"
}

cmd_readsweep() {
  need_all
  [[ -s "$receipt" ]] || fail "$receipt is missing or empty; run 'up' then 'upgrade' first"
  build_apiprobe
  load_exit_codes

  say "THE READ SWEEP — does everything the platform SERVES about those rows still answer?"
  # --schemas points at the WORKING TREE, which after 'upgrade' is what is deployed.
  # Pointing it at the baseline tree would sweep the doors the OLD release served,
  # which is the wrong question and would report every door the release ADDED as a
  # finding.
  local rc=0
  run_readsweep || rc=$?

  if [[ $rc -eq $APIPROBE_EXIT_SETUP ]]; then
    fail "THE READ SWEEP COULD NOT RUN, and this says NOTHING either way (apiprobe exit
$rc = SETUP). No door was shown to answer and none was shown to fail."
  fi
  if [[ $rc -ne 0 ]]; then
    fail "doors the platform SERVES stopped answering after the upgrade (apiprobe exit $rc).
Note what this is NOT: verify has already passed, so the rows are present and
unchanged, and the sweep builds its documents FROM the served schema, so they are
valid. Present rows, a valid query, and an error is the signature of a STORED SHAPE
the new release can no longer make sense of — a snapshot naming rows that were never
migrated, a document decoded into a struct that moved. Start at the migrations the
release adds, and at what they do NOT rewrite."
  fi
  say "READ SWEEP PASSED — every door the deployed schemas serve still answers"
}

# ---------------------------------------------------------------------------
# tablesweep — is "one of every entity" actually TRUE?
# ---------------------------------------------------------------------------
# apiprobe's entities.go is a coverage CLAIM, written as data so a missing entity is an
# absent row somebody can count. Nobody has ever counted it. A tenant-scoped table that
# no entity writes to is invisible from the API side by construction — there is no query
# that returns "the rows you did not create" — so the claim has been asserted for as long
# as the tool has existed and checked exactly never.
#
# 🔴 IT NEEDS ITS OWN TENANT, AND THE REASON IS ALREADY WRITTEN IN THIS FILE'S OWN
# SUMMARY: "entities $baseline_tag could not express were skipped by name and nothing
# here speaks for them". The drill's seed runs against the BASELINE, so a table belonging
# to an entity that release cannot create is legitimately empty — for a reason that
# changes every time the baseline moves. A coverage result that means something different
# each release is not a coverage result.
#
# So this seeds a SECOND tenant against the UPGRADED instance, with no --baseline-schemas
# and therefore no skips, and sweeps that one. The measurement is then about the release
# under test and nothing else. It closes the gap the summary names, and it buys a second
# finding on the way: that the upgraded release can still CREATE one of every entity,
# which no other phase asks.
#
# It runs after readsweep, so a failure here can never be confused for one: verify and
# the sweep have already passed, which means the finding is about this tool's reach and
# not about the instance's data.

# pg_credentials prints "<user> <secret>" for the relational store.
#
# The Secret's NAME is read from the live Cluster rather than assumed to be CNPG's
# generated `<cluster>-app`. dcctl's own db-verify does the same, for the same reason:
# the name is a function of the chart's bootstrap block, and a constant here would work
# until the day the chart set one explicitly, then connect as nobody.
pg_credentials() {
  local secret user
  secret="$(kubectl --context "$kube_context" -n dc-system \
    get cluster.postgresql.cnpg.io "$rdb_cluster" \
    -o jsonpath='{.spec.bootstrap.initdb.secret.name}' 2>/dev/null)" || true
  [[ -n "$secret" ]] || fail "the $rdb_cluster Cluster names no bootstrap credentials Secret.
Nothing can connect, which says nothing about coverage in either direction."

  user="$(kubectl --context "$kube_context" -n dc-system get secret "$secret" \
    -o jsonpath='{.data.username}' 2>/dev/null | base64 -d)" || true
  [[ -n "$user" ]] || fail "Secret $secret carries no username"
  printf '%s %s' "$user" "$secret"
}

# pf_port_from reads the local port out of kubectl's own output.
#
# Its own function purely so the self-test can exercise it: a regex that stops matching
# does not error, it returns nothing — and pg_forward would then spend thirty seconds
# waiting and report "could not open a port-forward" about a port-forward that was open
# the whole time. That is a broken instrument reporting a finding, which is the failure
# this whole drill exists to make impossible.
pf_port_from() {
  sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]\{1,\}\).*/\1/p' "$1" | head -1
}

# pg_forward opens a port-forward to the relational store and prints the local port.
#
# A RANDOM local port (`:5432`), not a fixed one: a fixed port collides with whatever a
# developer already has forwarded, and the collision surfaces as an authentication
# failure against somebody else's database — a wrong answer, not an error.
pg_forward() {
  local port="" i
  : > "$pf_log"
  kubectl --context "$kube_context" -n dc-system \
    port-forward "svc/dc-postgresql" ":5432" > "$pf_log" 2>&1 &
  pf_pid=$!

  for ((i = 0; i < 60; i++)); do
    port="$(pf_port_from "$pf_log")"
    [[ -n "$port" ]] && break
    # If kubectl has already died there is nothing to wait for, and waiting the full
    # thirty seconds would report a timeout for what was an immediate refusal.
    kill -0 "$pf_pid" 2>/dev/null || break
    sleep 0.5
  done

  if [[ -z "$port" ]]; then
    stop_forward
    fail "could not open a port-forward to dc-postgresql in dc-system. kubectl said:
$(sed 's/^/    /' "$pf_log" | head -10)"
  fi
  printf '%s' "$port"
}

stop_forward() {
  [[ -n "${pf_pid:-}" ]] || return 0
  kill "$pf_pid" 2>/dev/null || true
  wait "$pf_pid" 2>/dev/null || true
  pf_pid=""
}

cmd_tablesweep() {
  need_all
  build_apiprobe
  load_exit_codes

  say "THE TABLE SWEEP — is \"one of every entity\" actually true?"

  # No --baseline-schemas: this seed is against the UPGRADED release, so nothing is
  # skipped and the coverage measured is the release's own.
  "$apiprobe" seed --instance "$instance" --receipt "$sweep_receipt" \
    --tenant "$sweep_tenant" \
    --server "$api_server" --scheme "$api_scheme" ||
    fail "the UPGRADED release could not create one of every entity (apiprobe exit $?).
This is a finding about the release, not about the upgrade: verify and the read sweep
have already passed, so the rows carried across and every door still answers. What
failed is a CREATE, on an instance that is running the code under test."

  local pod user secret port rc=0
  pod="$(pg_pod)"
  rdb_db="$(rdb_database "$pod" devices)"
  read -r user secret <<<"$(pg_credentials)"

  port="$(pg_forward)"
  # 🔴 The password goes in PGPASSWORD, never in the DSN. A password on a command line
  # is readable by any local process through /proc for as long as the command runs, and
  # is one `set -x` away from a CI step log with a ninety-day retention. apiprobe
  # REFUSES a DSN carrying one rather than trusting this comment.
  PGPASSWORD="$(kubectl --context "$kube_context" -n dc-system get secret "$secret" \
    -o jsonpath='{.data.password}' | base64 -d)" \
    "$apiprobe" tablesweep \
    --dsn "postgres://${user}@127.0.0.1:${port}/${rdb_db}?sslmode=disable" \
    --tenant "$sweep_tenant" || rc=$?
  stop_forward

  if [[ $rc -eq $APIPROBE_EXIT_SETUP ]]; then
    fail "THE TABLE SWEEP COULD NOT RUN, and this says NOTHING either way (apiprobe exit
$rc = SETUP). No table was shown to be covered and none was shown to be missed."
  fi
  if [[ $rc -eq $APIPROBE_EXIT_COVERAGE ]]; then
    fail "the seed does not reach every tenant-scoped table the platform has (apiprobe
exit $rc = COVERAGE). Note what this is NOT: it is not a defect in the release. verify
and the read sweep have already passed on this instance. It is this DRILL measuring
less than it claims — a table no seeded entity writes to is a table the upgrade drill
cannot see change. apiprobe's output names each one and both ways to close it."
  fi
  if [[ $rc -ne 0 ]]; then
    fail "the table sweep exited $rc, which this rig has no meaning for. That is a bug
in apiprobe or in this script, not a finding about the instance — treat it as
INCONCLUSIVE and fix the tool before reading anything into it."
  fi
  say "TABLE SWEEP PASSED — every tenant-scoped table holds a seeded row, or says why not"
}

# ---------------------------------------------------------------------------
# control — the check on the check
# ---------------------------------------------------------------------------

# expect_verify_to_fail runs verify and requires ONE EXACT exit code.
#
# 🔴 Requiring merely "non-zero" would be no control at all. A verify that could
# not reach the ingress exits SETUP and is non-zero; so is one whose receipt is
# unreadable. Both would let a control "hold" while proving nothing about whether
# the probe can see the damage that was actually done — and would hold just as
# happily against a probe that was broken in the positive direction too.
expect_verify_to_fail() {
  local want="$1" label="$2" rc=0
  run_verify || rc=$?
  if [[ $rc -eq 0 ]]; then
    fail "THE $label CONTROL DID NOT HOLD. The instance was damaged on purpose and
verify still reported every row intact — so its PASS above proves nothing, and
neither would any future one."
  fi
  if [[ $rc -ne $want ]]; then
    fail "THE $label CONTROL IS INCONCLUSIVE. verify failed with exit $rc where $want was
required. A failure for the wrong reason is not a control: it would hold just as
well against a probe that cannot see this damage at all. Read the output above."
  fi
  say "$label CONTROL HELD (verify exited $rc, exactly as required)"
}

cmd_control() {
  need_all
  [[ -s "$receipt" ]] || fail "$receipt is missing or empty; run 'up' then 'upgrade' first"
  build_apiprobe
  load_exit_codes

  # 🔴 THE ORDER IS LOAD-BEARING, and it cannot be reversed.
  #
  # Verify walks the receipt in coverage-table order and stops at the FIRST row
  # that fails. The delete's hole sits later in the table than the modify's row,
  # so: delete alone is seen where the hole is, and the modify is then seen BEFORE
  # the run ever reaches it. Run the other way round, the second control would
  # report MISSING where MISMATCH was required and be dismissed as inconclusive.
  # apiprobe pins this with a test rather than leaving it to this comment.
  #
  # Neither control is reversible, which is why they run after the positive pass
  # and why `all` never revisits verify afterwards.

  # 🔴 THE READ-SWEEP CONTROL RUNS FIRST, AND THAT ORDER IS AS LOAD-BEARING AS THE
  # ONE BELOW — for a different reason, so it does not fold into that paragraph.
  #
  # Its claim is a PAIR: the sweep fails AND verify still passes. That pair is the
  # whole point, because it is the difference between "the sweep works" and "the
  # sweep sees something verify cannot". The two controls below damage rows verify
  # reads, permanently, so after either of them verify can never pass again and the
  # second half of this claim becomes unmeasurable.
  say "CONTROL 1 — un-addressing a stored fence-set snapshot, so the SWEEP has something to fail on"
  local pod schema damaged tenant
  pod="$(pg_pod)"
  rdb_db="$(rdb_database "$pod" geo_fence_set_versions)"
  note "relational store: pod $pod, database $rdb_db"

  # The schema comes from the catalog, not from this file. Areas own their schemas
  # and a rename would otherwise be invisible here until the UPDATE matched nothing
  # — which exits 0, damages nothing, and would be reported as a control that did
  # not hold.
  # 🔴 QUOTED, and it has to be. An area's schema is named after the AREA — literally
  # `device-management`, with a hyphen — which is not a bare SQL identifier: unquoted,
  # `device-management.geo_fence_set_versions` parses as a subtraction and the control
  # dies on a syntax error that reads like a broken rig rather than a quoting bug.
  # `quote_ident` is the server's own answer to that, so the escaping is Postgres's
  # rather than this script's guess at it.
  schema="$(psql_rdb "$pod" "" \
    "SELECT quote_ident(table_schema) FROM information_schema.tables WHERE table_name = 'geo_fence_set_versions' LIMIT 1")"
  [[ -n "$schema" ]] || fail "no schema holds geo_fence_set_versions; the fence-set history this control
needs was never created, so nothing is proved in either direction."

  tenant="$(jq -r '.tenant' <"$receipt")"
  [[ -n "$tenant" && "$tenant" != "null" ]] || fail "the receipt names no tenant; the control cannot be scoped"

  # 🔑 THIS IS THE ONLY DAMAGE IN THIS RIG DONE BEHIND THE API, and it is what the
  # release that shipped the geometry archive left behind: every fence in the stored
  # snapshot named by a content address, with the address removed. A pre-archive
  # snapshot has no `hash` key at all, and Go's decoder reads an absent key as the
  # EMPTY STRING rather than complaining — so the snapshot resolves every fence to a
  # blob that cannot exist. Removing the key reproduces that exactly; inventing a
  # wrong hash would not, because a wrong hash is a value somebody wrote and an
  # absent one is a release that never wrote it.
  #
  # Scoped to the probe tenant, and counted: a bare UPDATE that matches nothing exits
  # 0 and prints UPDATE 0.
  damaged="$(psql_rdb "$pod" "" "
    WITH stripped AS (
      UPDATE $schema.geo_fence_set_versions v
         SET snapshot = jsonb_set(v.snapshot::jsonb, '{fences}',
               (SELECT COALESCE(jsonb_agg(f - 'hash'), '[]'::jsonb)
                  FROM jsonb_array_elements(v.snapshot::jsonb->'fences') f))
       WHERE v.tenant_id = '$tenant'
         AND v.snapshot::jsonb->'fences' <> '[]'::jsonb
      RETURNING 1)
    SELECT count(*) FROM stripped")"
  [[ "${damaged:-0}" -gt 0 ]] || fail "the read-sweep control could not be ARMED: no fence-set version for
tenant '$tenant' carried any fence to un-address. The seed writes a geofence and a
fence-set version is minted from it, so an empty result means the seed skipped the
fence (the baseline could not express it) — in which case this control measures
nothing and must not be read as though it had."
  note "un-addressed the fences in $damaged fence-set version(s) for tenant $tenant"

  local rc=0
  run_readsweep || rc=$?
  if [[ $rc -eq 0 ]]; then
    fail "THE READ-SWEEP CONTROL DID NOT HOLD. A stored fence-set snapshot was left naming
its geometry by an address it does not carry — the exact state an un-backfilled
upgrade produces — and the sweep reported every door answering. Its PASS above
proves nothing, and neither would any future one."
  fi
  if [[ $rc -ne $APIPROBE_EXIT_UNREADABLE ]]; then
    fail "THE READ-SWEEP CONTROL IS INCONCLUSIVE. The sweep failed with exit $rc where
$APIPROBE_EXIT_UNREADABLE (UNREADABLE) was required. A failure for the wrong reason is not a control:
SETUP would hold just as well against a sweep that never reached the ingress."
  fi
  say "READ-SWEEP CONTROL HELD (sweep exited $rc, exactly as required)"

  # 🔑 AND THE SECOND HALF, WHICH IS THE ONE WORTH THE PHASE. verify must still pass
  # over the very same damage. If it fails here the pair collapses: the two checks
  # would be seeing the same thing, this sweep would be a slower spelling of verify,
  # and the gap it was built to close would still be open.
  rc=0
  run_verify || rc=$?
  [[ $rc -eq 0 ]] ||
    fail "verify FAILED (exit $rc) over damage that only touched a derived artifact. Either
the damage reached a row verify reads — in which case this control is not measuring
what it claims — or verify is broken. Neither conclusion is about the read sweep."
  say "AND VERIFY STILL PASSES over the same damage — which is the claim: the sweep sees
what a round trip of one's own writes structurally cannot."

  say "CONTROL 2 — deleting a seeded row, so verify has something to MISS"
  # `tamper` proves the damage landed before this script draws any conclusion from
  # it: a delete that was silently refused leaves the row where it was, and a
  # verify that then passes would be reported as a control that did not hold when
  # nothing was ever deleted. It re-reads through verify's own query, so "armed"
  # means "verify will now fail".
  "$apiprobe" tamper --receipt "$receipt" --mode delete \
    --server "$api_server" --scheme "$api_scheme" ||
    fail "the delete control could not be ARMED (apiprobe exit $?). Nothing was proved
in either direction: this is the rig failing to set up its own control, not a
finding about the platform."
  expect_verify_to_fail "$APIPROBE_EXIT_MISSING" "DELETED-ROW"

  say "CONTROL 3 — rewriting a field, so verify has something to MISMATCH"
  # Not redundant with the first. A verify that reported every row missing would
  # pass control 1 while being completely broken; only a control that demands a
  # DIFFERENT code can tell "the probe reads rows and compares them" apart from
  # "the probe reports absence no matter what". The two together are the claim.
  "$apiprobe" tamper --receipt "$receipt" --mode modify \
    --server "$api_server" --scheme "$api_scheme" ||
    fail "the modify control could not be ARMED (apiprobe exit $?). Nothing was proved
in either direction."
  expect_verify_to_fail "$APIPROBE_EXIT_MISMATCH" "CHANGED-FIELD"
}

# ---------------------------------------------------------------------------
# operator — the half of the release `helm upgrade` cannot reach
# ---------------------------------------------------------------------------
#
# docs/docs/deployment/releases-and-upgrades.md tells an operator that one version
# covers "each service image, the operator, the Helm chart, and dcctl", and that
# there is "no per-service version skew to reason about". The documented upgrade
# is a `helm upgrade` — and the operator is NOT IN THE CHART. dcctl applies it
# from its own embedded manifests (backend/cli/bootstrap/steps.go renders
# backend/k8s's overlay), so nothing Helm does can move it.
#
# 🔴 THIS PHASE WAS BUILT KNOWING IT WOULD FAIL, AND THAT IS WHY THE FIX EXISTS.
# When it was written there was no way to move the operator at all: re-running
# bootstrap rotates every generated credential, and no other subcommand touched
# it. So an operator following the documentation to the letter ended up with new
# services, the old controller, and a promise that said otherwise. The gate said
# so out loud instead of a comment recording it as a known limitation — and
# `dcctl upgrade` is what that produced. `cmd_upgrade` now runs it as the second
# half of the documented procedure, and this phase measures the result.
#
# It still carries its own exit code, and the reason has outlived the finding: a
# workflow reading `exit 20` knows the release has a version-skew defect, as
# opposed to a runner that ran out of disk. What changed is what a red here MEANS
# — no longer "the gap nobody has closed" but "the fix did not hold". The failure
# text says so, because a stale explanation is worse than none: it would send
# whoever reads it to build something that already exists.
#
# It runs LAST, and that ordering is unchanged. The drill's primary claim — that
# rows survive — must be measured and reported before any later check can stop the
# run, or a real data-loss defect would sit behind a version mismatch.

# operator_namespace reads the namespace out of the operator's own kustomize
# overlay rather than repeating it here, so a rename moves this check with it —
# and, because the self-test calls this against the real tree, a rename that
# removed it fails in ordinary CI rather than an hour into a cluster run.
operator_namespace() {
  awk '$1 == "namespace:" { print $2; exit }' \
    "$repo_root/backend/k8s/config/default/kustomization.yaml"
}

# operator_in_step <image-ref> <wanted-tag>
#
#   0  the reference carries exactly the wanted tag
#   1  it carries a DIFFERENT tag — the skew finding
#   2  it carries no usable tag at all, which is a finding in NEITHER direction
#      and must never be reported as one
#
# 🔴 THE TWO TRAPS ARE BOTH IN THE STRING, and the obvious one-liner
# (`${ref##*:}`) walks into both. `localhost:5000/operator` has a colon and no tag,
# and that spelling yields `5000/operator` — a registry PORT read as a version, so
# an untagged reference reports skew against whatever it is compared to. And
# `…/operator@sha256:abc…` yields the digest hex, manufacturing a finding out of a
# pin that is stricter than any tag. Splitting the last path segment first, and
# refusing a digest outright, is what makes both of those say "cannot tell".
operator_in_step() {
  local last="${1##*/}" want="$2"
  [[ "$last" != *@* ]] || return 2
  [[ "$last" == *:* ]] || return 2
  [[ "${last##*:}" == "$want" ]] || return 1
  return 0
}

cmd_operator() {
  need kubectl
  [[ -s "$target_file" ]] || fail "$target_file is missing; run 'upgrade' first. Without it this
check has no version to measure the operator AGAINST, and comparing it to a guess
would report skew or agreement at random."

  local want ns listing
  want="$(sed -n 2p "$target_file")"
  [[ -n "$want" ]] || fail "$target_file names no tag; the upgrade did not finish writing it"
  ns="$(operator_namespace)"
  [[ -n "$ns" ]] || fail "backend/k8s/config/default/kustomization.yaml declares no namespace, so
this check cannot find the operator. It was renamed or removed — read that file."

  say "THE OPERATOR — is it running the version the services were upgraded to?"

  # shellcheck disable=SC2016  # $n is a GO TEMPLATE variable; shell must not expand it
  listing="$(kubectl --context "$kube_context" -n "$ns" get deployments \
    -o go-template='{{range .items}}{{$n := .metadata.name}}{{range .spec.template.spec.containers}}{{$n}} {{.image}}{{"\n"}}{{end}}{{end}}')" ||
    fail "could not read deployments in $ns. Nothing is claimed in either direction —
this is the check failing to run, not the operator failing to be upgraded."

  # 🔴 An EMPTY listing is INCONCLUSIVE, not a pass. `kubectl get` over an empty or
  # missing namespace exits 0 and prints nothing, so a check that only compared
  # what it found would report the operator perfectly in step precisely when it
  # could not see one — the loudest possible silence.
  [[ -n "$listing" ]] || fail "no deployment at all in namespace $ns, so there is no operator to
measure. Either the namespace moved (read backend/k8s/config/default/kustomization.yaml)
or dcctl never installed it. Neither is evidence about the upgrade."

  local name image skewed=() untagged=() matched=0 rc
  while read -r name image; do
    [[ -n "$name" ]] || continue
    rc=0
    operator_in_step "$image" "$want" || rc=$?
    case "$rc" in
    0) matched=$((matched + 1)); note "$name  $image  ✓ at $want" ;;
    1) skewed+=("$name  $image") ;;
    2) untagged+=("$name  $image") ;;
    esac
  done <<<"$listing"

  # An unreadable reference is INCONCLUSIVE and gets its own exit, for the reason
  # the whole taxonomy exists: a digest-pinned operator may be perfectly current
  # and this check simply cannot say, which is not the same claim as skew.
  if [[ ${#untagged[@]} -gt 0 ]]; then
    fail "the operator's image carries no tag this check can read:
  ${untagged[*]}
A digest pin may name exactly the right build — nothing is claimed here in either
direction. Compare it by digest, or read the reference by hand."
  fi

  if [[ ${#skewed[@]} -gt 0 ]]; then
    fail_code "$exit_operator_skew" "THE OPERATOR WAS NOT UPGRADED. This is a FINDING ABOUT THE RELEASE,
not a broken rig — and it is the one this drill was extended to make visible.

  still running:  ${skewed[*]}
  services now:   $want

docs/docs/deployment/releases-and-upgrades.md promises that one version covers the
service images, the operator, the chart and dcctl together, with no per-service
skew to reason about.

🔴 READ THIS AS A REGRESSION, NOT AS THE KNOWN GAP. It once was the known gap:
\`helm upgrade\` cannot reach the operator (it is not in the chart), and for a while
no subcommand moved it either, so this phase was red by design. That is closed —
\`dcctl upgrade\` exists and \`cmd_upgrade\` above RAN IT, successfully, minutes ago.
So the controller is on the old tag despite an upgrade that reported success, and
one of these is true:

  • the operator image for $want was never published, or is not public, and the
    new pod is stuck pulling while the old ReplicaSet still serves;
  • \`dcctl upgrade\` applied a Deployment other than the one measured here — read
    the namespace and names above against backend/k8s/config;
  • something outside the drill re-applied the baseline's manifests afterwards.

Whichever it is, an operator following the documentation lands exactly here, which
is why this blocks the release rather than being noted in it."
  fi

  say "OPERATOR IN STEP — $matched deployment(s) in $ns are running $want"
}

# ---------------------------------------------------------------------------
# selftest — the part of this rig that runs without a cluster
# ---------------------------------------------------------------------------
#
# The drill itself needs an hour and a kind cluster, so nothing about it runs on
# an ordinary PR. That makes its string handling exactly the kind of code that
# rots unwatched: correct the day it was written, and first exercised again at the
# moment a release is waiting on it.
#
# 🔴 THE POSITIVE CASE IS THE IMPORTANT ONE. `cmd_operator` is expected to FAIL in
# every real run until the operator gap is closed, which means its red says almost
# nothing on its own — a check hard-wired to fail would look identical. Proving it
# reports AGREEMENT when the tags agree is what makes the red mean something.
selftest() {
  # Needed before expect_mode's self-upgrade case, which asks validate_upgrade_mode to
  # refuse `upgrade_tag == baseline_tag`. An unresolved baseline makes that case pass an
  # EMPTY tag, which is refused by the previous check instead — and expect_mode asserts
  # the REASON, so it would report the wrong-reason failure rather than silently drifting.
  # That is the guard working; resolving here is what lets the case measure what it names.
  resolve_baseline_tag

  local rc baseline_tag_for_chart_test=""
  # The newest stable tag whose chart is NOT HEAD's — the refusal case, chosen from
  # the repository rather than written down, so it does not name a tag that will one
  # day carry the same chart as HEAD and turn this into a silent pass.
  local t head_chart_tree
  head_chart_tree="$(git -C "$repo_root" rev-parse "HEAD:deploy/helm/devicechain" 2>/dev/null || true)"
  while read -r t; do
    [[ "$(git -C "$repo_root" rev-parse "$t:deploy/helm/devicechain" 2>/dev/null || true)" != "$head_chart_tree" ]] || continue
    baseline_tag_for_chart_test="$t"
  done < <(git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -v '[-]' | sort -V)

  # --- the port-forward's port parse ------------------------------------------
  # Real kubectl output, including the IPv6 line it also prints, so the anchor is
  # exercised rather than assumed.
  local pf_fixture; pf_fixture="$(mktemp)"
  printf 'Forwarding from 127.0.0.1:34567 -> 5432\nForwarding from [::1]:34567 -> 5432\n' > "$pf_fixture"
  [[ "$(pf_port_from "$pf_fixture")" == "34567" ]] ||
    fail "SELF-TEST FAILED: the port-forward port was not read out of kubectl's output"
  note "the local port is read out of kubectl's own output"

  # THE COUNTERWEIGHT. A parse that printed something for any input would satisfy the
  # case above and make the thirty-second wait unreachable — pg_forward would take a
  # kubectl that failed instantly as a successful forward on a garbage port.
  printf 'error: unable to forward port because pod is not running. Current status=Pending\n' > "$pf_fixture"
  [[ -z "$(pf_port_from "$pf_fixture")" ]] ||
    fail "SELF-TEST FAILED: a kubectl ERROR was read as a port number"
  : > "$pf_fixture"
  [[ -z "$(pf_port_from "$pf_fixture")" ]] ||
    fail "SELF-TEST FAILED: an empty log yielded a port"
  rm -f "$pf_fixture"
  note "a kubectl error, and an empty log, yield no port"

  expect_step() {
    local image="$1" want="$2" expected="$3" label="$4"
    rc=0
    operator_in_step "$image" "$want" || rc=$?
    [[ "$rc" -eq "$expected" ]] ||
      fail "SELF-TEST FAILED ($label): operator_in_step '$image' '$want' returned $rc, wanted $expected"
    note "$label"
  }

  # ---- the derived baseline ------------------------------------------------
  # A derived default replaced a literal that had gone three releases stale, so the
  # derivation is checked rather than trusted. All three cases matter and the third
  # most: a resolver that ignored DC_BASELINE_TAG would pass the first two while
  # silently drilling the wrong hop for anyone who asked for a specific one.
  say "the derived baseline"
  local derived above
  derived="$(newest_stable_tag)"
  [[ -n "$derived" ]] ||
    fail "SELF-TEST FAILED: newest_stable_tag found nothing. A shallow clone is the usual
cause; this rig needs tag history and refuses rather than defaulting, because a guessed
baseline drills a hop no release note ever claimed."

  # 🔴 ASSERTS A PROPERTY — "no stable tag sorts above it" — RATHER THAN RECOMPUTING THE
  # SAME PIPELINE AND COMPARING. The obvious version was written first and is worth
  # naming: it built the expected value with the very expression under test, so the test
  # moved with any edit to that expression instead of objecting to it. A fixture derived
  # from the thing it is checking cannot catch that thing moving.
  above="$(git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -v '[-]' |
    while read -r t; do [[ "$t" == "$derived" ]] || [[ "$(printf '%s\n%s\n' "$t" "$derived" | sort -V | tail -1)" == "$derived" ]] || echo "$t"; done)"
  [[ -z "$above" ]] ||
    fail "SELF-TEST FAILED: newest_stable_tag returned '$derived', but these stable tags sort
above it: $(printf '%s' "$above" | tr '\n' ' ')"
  note "no stable tag sorts above the derived baseline ($derived)"

  # 🔴 A PRERELEASE IS NOT A RELEASE ANYONE IS RUNNING, and this repository currently
  # carries one that sorts ABOVE the derived answer (v0.13.0-rc.1 vs v0.13.0) — so the
  # case has something real to measure rather than passing by absence.
  [[ "$derived" != *-* ]] ||
    fail "SELF-TEST FAILED: the derived baseline '$derived' is a PRERELEASE. The drill must
upgrade from a release an operator is actually on."
  git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*-*' | grep -q . ||
    fail "SELF-TEST FAILED: this repository carries no prerelease tags, so the case above
passed by ABSENCE. It would hold just as well against a filter that had stopped working."
  note "a prerelease is not chosen, and there was one to reject"

  # An explicit request wins. Checked by asking the resolver, not by reading the
  # assignment: those are two encodings of one rule and only one of them runs.
  local saved="$baseline_tag"
  baseline_tag="v0.0.9-explicit"
  resolve_baseline_tag
  [[ "$baseline_tag" == "v0.0.9-explicit" ]] ||
    fail "SELF-TEST FAILED: resolve_baseline_tag overwrote an explicit DC_BASELINE_TAG with '$baseline_tag'"
  baseline_tag="$saved"
  note "an explicit DC_BASELINE_TAG is left alone"

  say "operator_in_step"
  expect_step "ghcr.io/devicechain-io/operator:v0.12.0" "v0.12.0" 0 "a matching published tag is IN STEP"
  expect_step "ghcr.io/devicechain-io/operator:v0.11.0" "v0.12.0" 1 "the baseline's tag is SKEW"
  expect_step "localhost:5000/operator:head-20260819" "head-20260819" 0 "a registry PORT is not read as a tag"
  expect_step "localhost:5000/operator" "head-20260819" 2 "an untagged reference cannot tell, and says so"
  expect_step "ghcr.io/devicechain-io/operator@sha256:abc123" "v0.12.0" 2 "a digest pin cannot tell, and says so"

  # LOCKSTEP, not a restatement. apiprobe owns the exit codes this rig imports and
  # branches on; the operator finding needs one of its own, and the two vocabularies
  # live in different files with nothing but this check between them.
  say "the operator finding's exit code is apiprobe's to collide with"
  local apiprobe_src="$repo_root/backend/tools/apiprobe/main.go" taken
  taken="$(grep -oE '^\s*exit[A-Za-z]+ = [0-9]+' "$apiprobe_src" | grep -oE '[0-9]+$' | sort -u | tr '\n' ' ')"
  [[ -n "$taken" ]] ||
    fail "SELF-TEST FAILED: no exit-code constants found in $apiprobe_src. This check
reads them out of apiprobe's source rather than repeating them, so it has just
stopped checking anything — the constants moved or were renamed."
  local code
  for code in $taken; do
    [[ "$code" != "$exit_operator_skew" ]] ||
      fail "SELF-TEST FAILED: exit $exit_operator_skew means the operator-skew finding here AND
one of apiprobe's verdicts there. The same number in one log for two different
things is how a control HOLDING reads as a release defect. Pick another."
  done
  note "apiprobe uses [$taken]; the operator finding uses $exit_operator_skew"

  say "operator_namespace"
  local ns
  ns="$(operator_namespace)"
  [[ -n "$ns" ]] ||
    fail "SELF-TEST FAILED: backend/k8s/config/default/kustomization.yaml declares no namespace,
so the operator check would have nothing to look in. This is the lockstep half of
the self-test: the rig reads that file rather than repeating the namespace, and
this is where a rename is supposed to be caught."
  note "the operator overlay still declares a namespace ($ns)"

  say "ko is a build-path tool"
  local saved_for_ko="$upgrade_images"
  upgrade_images=build
  ko_required || fail "SELF-TEST FAILED: the build path does not think it needs ko, so need_all
would let a run reach build-images.sh without a builder."
  note "the build path requires ko"
  upgrade_images=pull
  ! ko_required || fail "SELF-TEST FAILED: the pull path thinks it needs ko. It builds no image
at all, and demanding a builder there is what killed the first pull-path run three
seconds in — the gate correctly skips installing one."
  note "the pull path does not"
  upgrade_images="$saved_for_ko"

  say "the image-source modes"
  local saved_images="$upgrade_images" saved_tag="$upgrade_tag" saved_baseline="$baseline_tag"
  # 🔴 ASSERTS THE REASON, NOT JUST THE CODE — and the first version of this did
  # only the code, which cost three false greens at once. Every refusal here exits
  # 1, so "returned 1" holds equally against a check that fired for something else
  # entirely: a mutation that made an unknown tag SKIP the chart comparison still
  # exited 1 (on the next check down), a mutation that made both sides read the
  # SAME tree still exited 1 (the case was being refused as a self-upgrade before
  # it ever reached the comparison), and a broken `-n` guard still exited 1 (the
  # empty string simply compared unequal). All three passed. This rig already
  # writes the rule down for its own controls — "a failure for the wrong reason is
  # not a control" — and the self-test was not following it.
  #
  # `want` is a fragment of the message the intended check emits; the empty string
  # means the call must SUCCEED.
  expect_mode() {
    local want="$1" label="$2" out
    rc=0
    out="$( ( validate_upgrade_mode ) 2>&1 )" || rc=$?
    if [[ -z "$want" ]]; then
      [[ "$rc" -eq 0 ]] ||
        fail "SELF-TEST FAILED ($label): validate_upgrade_mode refused what it should accept.
$out"
      note "$label"
      return
    fi
    [[ "$rc" -ne 0 ]] ||
      fail "SELF-TEST FAILED ($label): validate_upgrade_mode accepted what it should refuse."
    [[ "$out" == *"$want"* ]] ||
      fail "SELF-TEST FAILED ($label): it refused, but for the WRONG REASON — the message
does not mention '$want'. A refusal that fires from a different check holds just as
well against a version where the intended one has stopped working. It said:
$out"
    note "$label"
  }
  upgrade_images="build" upgrade_tag=""
  expect_mode "" "build needs no tag"
  upgrade_images="pull" upgrade_tag=""
  expect_mode "needs DC_UPGRADE_TAG" "pull without a tag is refused"
  upgrade_images="pull" upgrade_tag="$baseline_tag"
  expect_mode "both" "upgrading the baseline to itself is refused"
  upgrade_images="pull" upgrade_tag="v9.9.9"
  expect_mode "is not a tag in this repository" "a tag this repository has never seen is refused, not skipped"
  upgrade_images="rebuild" upgrade_tag=""
  expect_mode "must be 'build' or 'pull'" "an unknown mode is refused"

  # The chart/images lockstep, both directions. A guard that only ever refuses is
  # indistinguishable from one wired shut, so the agreeing case is the one that
  # makes the refusal mean something. Both are asked of the REAL repository: a
  # tag whose chart differs from HEAD's, and a tag whose chart is HEAD's by
  # construction.
  # 🔴 TWO DIFFERENT CAUSES, NAMED SEPARATELY. "no tags at all" is a shallow clone
  # and says nothing about the tree; "tags, but none whose chart differs" is a real
  # (if unlikely) condition in which this case has nothing to measure. Reporting the
  # first as the second is the misdiagnosis this whole slice keeps running into —
  # and it happened here, on the first CI run, where a shallow checkout was reported
  # as "no stable tag carries a chart different from HEAD's".
  if [[ -z "$baseline_tag_for_chart_test" ]]; then
    git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*' | grep -qv '[-]' ||
      fail "SELF-TEST FAILED: this repository shows no stable release tags, so the chart
lockstep case has no release to measure against. A shallow clone is the usual cause —
this check needs history (fetch-depth: 0), and it refuses rather than skip, because
'checked and agreed' must not read the same as 'could not look'."
    fail "SELF-TEST FAILED: every stable tag carries the same chart as HEAD, so the
refusal case cannot be exercised. That is not a pass — this check has nothing to
measure."
  fi
  # 🔴 The baseline is moved off the target deliberately. The newest stable tag is
  # usually the DEFAULT baseline too, so leaving it alone made this case refuse as a
  # self-upgrade and never reach the chart comparison at all — a green that meant
  # only that some earlier check still worked.
  baseline_tag="$baseline_tag_for_chart_test-not-the-target"
  upgrade_images="pull" upgrade_tag="$baseline_tag_for_chart_test"
  expect_mode "different releases" "a tag whose chart differs from HEAD is refused"

  # 🔴 NOTHING BELOW WRITES TO THE REPOSITORY. The first version created and deleted
  # tags, which is wrong twice over: this checkout's .git is shared with other
  # worktrees, so a self-test killed between create and delete leaves an entry for
  # someone else to find — the same reason the rig uses `git archive` rather than
  # `git worktree add`. And it needed `git commit-tree`, which needs a committer
  # identity; a hosted runner has none, so the check died with a git fatal error
  # rather than a verdict. Every case here uses a commit-ish the repository already
  # has.
  #
  # HEAD is the accepted case by construction: its chart tree is trivially its own.
  upgrade_images="pull" upgrade_tag="HEAD"
  expect_mode "" "a target carrying THIS chart is accepted"

  # 🔴 A target with NO CHART AT ALL must say it could not READ one — not that the
  # two are different releases. Both refuse, so the outcome is the same and the
  # SENTENCE is not: "the chart and the images are different releases" sends a
  # reader after a version mismatch that does not exist. This rig's whole argument
  # is that INCONCLUSIVE and FINDING are different results.
  #
  # The root commit predates the chart, so it is that case without inventing one.
  local root
  root="$(git -C "$repo_root" rev-list --max-parents=0 HEAD | tail -1)"
  [[ -n "$root" ]] || fail "SELF-TEST FAILED: this repository reports no root commit."
  ! git -C "$repo_root" rev-parse --verify -q "$root:deploy/helm/devicechain" >/dev/null 2>&1 ||
    fail "SELF-TEST FAILED: the root commit $root already carries deploy/helm/devicechain, so
the no-chart case has nothing to measure. That is not a pass — find another
commit-ish without the chart, or this check has quietly stopped checking."
  upgrade_images="pull" upgrade_tag="$root"
  expect_mode "could not read the chart tree" "a target carrying no chart says it could not READ one"

  upgrade_images="$saved_images" upgrade_tag="$saved_tag" baseline_tag="$saved_baseline"

  say "SELF-TEST PASSED"
}

# ---------------------------------------------------------------------------
# down
# ---------------------------------------------------------------------------

cmd_down() {
  need kind
  need docker
  if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster"
  fi
  if [[ -d "$work" ]]; then
    say "removing the rig's working directory $work"
    rm -rf "${work:?}"
  fi
  rm -rf "${HOME:?}/.devicechain/$instance"
  # The registry container is deliberately LEFT RUNNING. It is the same
  # kind-registry that dcctl's --build path and deploy/local/up.sh use, so
  # removing it here would break a developer's own cluster to tidy up after this
  # one. Its images are the cost: `docker rm -f kind-registry` reclaims them.
  note "$registry_container is left running — it is shared with dcctl and deploy/local."
  note "Remove it with 'docker rm -f $registry_container' to reclaim this run's images."
}

case "${1:-all}" in
up) cmd_up ;;
upgrade) cmd_upgrade ;;
verify) cmd_verify ;;
readsweep) cmd_readsweep ;;
tablesweep) cmd_tablesweep ;;
control) cmd_control ;;
operator) cmd_operator ;;
selftest) selftest ;;
down) cmd_down ;;
all)
  cmd_up
  cmd_upgrade
  cmd_verify
  cmd_readsweep
  cmd_tablesweep
  cmd_control
  say "DATA SURVIVED: rows written through the API on $baseline_tag were read back
unchanged after the documented upgrade, every door the served schemas expose still
answered, and each check was then shown to FAIL — with the right code — against
damage of its own kind: an un-addressed stored snapshot for the sweep, a deleted
row and a rewritten field for verify. The snapshot control is the one that says
these are two checks and not one, because verify PASSED over that damage. This is
the evidence the release's upgrade claim rests on.

WHAT IS STILL NOT COVERED, so this is not read as more than it is: nothing was
upgraded under load, so no zero-downtime claim is supported; entities
$baseline_tag could not express were skipped by name, and only the table sweep speaks
for them — it re-seeds a second tenant against the UPGRADED release, so it says the
release can still create one of every entity and that the seed reaches every
tenant-scoped table, but it says nothing about those entities SURVIVING an upgrade;
and event history belongs to the DR drill, not this one."

  # LAST, and deliberately after the summary above. This phase is expected to fail
  # until the operator gap is closed, and a known red must never stand between the
  # drill and its primary result — a real data-loss defect found on a run that
  # stopped here would be invisible.
  cmd_operator
  say "UPGRADE DRILL COMPLETE — data survived AND the operator is in step."
  ;;
*) fail "unknown command ${1}; try up | upgrade | verify | readsweep | tablesweep | control | operator | all | selftest | down" ;;
esac
