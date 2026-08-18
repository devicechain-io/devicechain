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
#   hack/upgrade-rig.sh control   THE NEGATIVE CONTROLS: break a row two different
#                                 ways and require verify to notice each, with the
#                                 exact exit code for that kind of damage
#   hack/upgrade-rig.sh all       up + upgrade + verify + control
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
#
# Requires: kind, kubectl, docker, helm, ko, git, curl, `tofu` OR `terraform` on
# PATH, and a Go toolchain (the rig builds dcctl, apiprobe and every service image
# itself). Two full image builds and a bring-up — budget an hour, and run `down`
# afterwards.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The release this drill upgrades FROM. Overridable so the same rig serves the
# next release without being edited — which is the point of it being a rig.
baseline_tag="${DC_BASELINE_TAG:-v0.11.0}"

# NOT configurable: kind takes the cluster name from the top-level `name:` in the
# config file in preference to --name, so a variable here would name the context
# one thing and the cluster another. Change both or neither.
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

work="${DC_UPGRADE_WORK:-$HOME/.devicechain-upgrade-rig}"
baseline_src="$work/src"
baseline_dcctl="$baseline_src/backend/cli/build/dcctl"
apiprobe="$work/bin/apiprobe"
receipt="$work/receipt.json"
values_file="$work/values.yaml"
tag_file="$work/head-tag"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
need_all() {
  # Every tool, checked BEFORE anything is provisioned. A missing binary
  # discovered twenty minutes into a bring-up is the posture this script rejects
  # everywhere else.
  for t in kind kubectl docker helm ko git curl go; do need "$t"; done
  command -v tofu >/dev/null 2>&1 || command -v terraform >/dev/null 2>&1 ||
    fail "one of tofu or terraform is required but neither is on PATH"
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
  for code in MISSING MISMATCH SHAPE SETUP; do
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
}

build_baseline_dcctl() {
  say "building $baseline_tag's own dcctl (its chart, not the working tree's)"
  make -C "$baseline_src/backend/cli" build >/dev/null
  [[ -x "$baseline_dcctl" ]] || fail "the $baseline_tag dcctl was not built at $baseline_dcctl"
  note "$("$baseline_dcctl" version | head -1)"
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

  # A PER-RUN tag, and it is load-bearing rather than tidy.
  #
  # 🔴 Reusing a fixed tag (`dev`, or the release name) means the kubelet can
  # satisfy the new image reference from a layer it already has: the workloads
  # roll, report Ready, and run the PREVIOUS build. An upgrade drill that silently
  # upgrades to the same code passes every check it has, which is the worst
  # available failure. A tag that has never existed before cannot be served from
  # cache.
  local head_tag
  head_tag="head-$(date -u +%Y%m%d%H%M%S)"
  echo "$head_tag" >"$tag_file"

  say "building the working tree's images → $registry (tag $head_tag)"
  REGISTRY="$registry" TAG="$head_tag" "$repo_root/deploy/local/build-images.sh"

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
    --set image.registry="$registry" --set image.tag="$head_tag" \
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
  say "UPGRADED — the working tree's images are serving, from the release's own values"
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
  if [[ $rc -ne 0 ]]; then
    fail "rows written on $baseline_tag did NOT survive the upgrade (apiprobe exit $rc).
Read apiprobe's output above, and note that the code says which KIND of defect:
  $APIPROBE_EXIT_MISSING  a row is GONE — a migration dropped data, and a schema-only diff cannot see it
  $APIPROBE_EXIT_MISMATCH a field CHANGED — a migration rewrote data, which is the failure this drill exists for
  $APIPROBE_EXIT_SHAPE  the QUERY was rejected — the schema moved; the row may be perfectly intact
  $APIPROBE_EXIT_SETUP  the drill could not run, and this says nothing either way"
  fi
  say "DRILL PASSED — every row written by $baseline_tag reads back unchanged from the working tree"
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

  say "CONTROL 1 — deleting a seeded row, so verify has something to MISS"
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

  say "CONTROL 2 — rewriting a field, so verify has something to MISMATCH"
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
control) cmd_control ;;
down) cmd_down ;;
all)
  cmd_up
  cmd_upgrade
  cmd_verify
  cmd_control
  say "UPGRADE DRILL COMPLETE: rows written through the API on $baseline_tag were read
back unchanged after the documented upgrade to the working tree, and the probe was
then shown to FAIL — with the right code — against both a deleted row and a
rewritten field. This is the evidence the release's upgrade claim rests on.

WHAT IS STILL NOT COVERED, so this is not read as more than it is: nothing was
upgraded under load, so no zero-downtime claim is supported; the operator was not
upgraded, matching the documented procedure; entities $baseline_tag could not
express were skipped by name and nothing here speaks for them; and event history
belongs to the DR drill, not this one."
  ;;
*) fail "unknown command ${1}; try up | upgrade | verify | control | all | down" ;;
esac
