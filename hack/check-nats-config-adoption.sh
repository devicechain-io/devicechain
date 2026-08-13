#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assert the two settings that make a NATS config change actually REACH the running
# broker, and that a pinned chart is what they were verified against.
#
# WHY THIS EXISTS
#
# nats-server refuses to hot-reload a whole class of options. `diffOptions`
# (server/reload.go) switches on each changed field and its `default:` arm returns
# "config reload not supported for %s". `authcallout` has no case, so ANY change to
# the auth_callout block lands there; JetStream's max memory / max store are refused
# the same way. And the refusal is WHOLESALE — the caller abandons the entire reload,
# discarding every other change in the same apply with it.
#
# What that looks like in the field, measured on a live cluster rather than reasoned
# about: `tofu apply` reports success, the reloader sidecar reports it sent the
# hangup, the ConfigMap shows the new values, and the running server is still on the
# config it booted with. The only evidence is one [ERR] line inside the broker's own
# log. Services then fail `nats: Authorization Violation` against a ConfigMap that
# proves the credentials are right.
#
# podTemplate.configChecksumAnnotation is the fix: the chart stamps a hash of the
# rendered ConfigMap onto the pod template, so a config change rolls the StatefulSet
# and the server always comes up on the file. It defaults to FALSE upstream, so this
# is a setting we must keep, not one we inherit.
#
# WHAT IT DOES AND DOES NOT DISTINGUISH
#
# This is a SOURCE-level check and it is honest about being one. It catches the
# realistic regression — someone tidying away a flag whose purpose is not obvious from
# the line itself — and it cannot catch upstream changing or removing the chart
# feature. That risk is held by the version pin plus the bump instructions in the
# module's chart_version description.
#
# 🔴 The pin is a PRECONDITION of this mechanism, not unrelated hygiene: the
# pod-template hash covers the chart's own `helm.sh/chart` label, so an unpinned chart
# turns config adoption into an unscheduled outage trigger — the broker rolls whenever
# upstream cuts a release. It is enforced by hack/check-chart-pins.sh, which holds the
# same requirement for every chart this deployment installs, in module and root, and
# asserts they agree. Both scripts run in the same CI job.
#
# Deliberately NOT a `helm template` render. That would prove more, but it needs the
# chart fetched from the network at check time — and the release-asset host this repo
# already fights (see the ko install in the image-smoke job) would make a green gate
# depend on someone else's CDN. A check that fails for reasons unrelated to the code
# teaches people to ignore it.

set -euo pipefail

cd "$(dirname "$0")/.."

module=deploy/opentofu/modules/nats/main.tf
rc=0

fail() {
	echo "FAIL: $1" >&2
	rc=1
}

# 1. The annotation that makes a config change roll the broker.
if ! grep -Eq '^[[:space:]]*configChecksumAnnotation[[:space:]]*=[[:space:]]*true[[:space:]]*$' "$module"; then
	fail "$module does not set podTemplate.configChecksumAnnotation = true.
      Without it a change to the auth_callout block (or to JetStream's max memory /
      max store) is applied, reported as successful by every layer, and silently
      never adopted by the running server."
fi

if [ "$rc" -eq 0 ]; then
	echo "OK: NATS config adoption is enforced (podTemplate.configChecksumAnnotation is set)."
fi

exit "$rc"
