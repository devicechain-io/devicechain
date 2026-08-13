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
# realistic regression — someone tidying away a flag or a pin whose purpose is not
# obvious from the line itself — and it cannot catch upstream changing or removing
# the chart feature. That risk is held by the version pin plus the bump instructions
# in the module's chart_version description, which is why the pin is checked HERE
# rather than treated as unrelated hygiene: an unpinned chart also means the
# pod-template hash covers a `helm.sh/chart` label that moves on its own, turning a
# config-adoption mechanism into an unscheduled outage trigger.
#
# Deliberately NOT a `helm template` render. That would prove more, but it needs the
# chart fetched from the network at check time — and the release-asset host this repo
# already fights (see the ko install in the image-smoke job) would make a green gate
# depend on someone else's CDN. A check that fails for reasons unrelated to the code
# teaches people to ignore it.

set -euo pipefail

cd "$(dirname "$0")/.."

module=deploy/opentofu/modules/nats/main.tf
root=deploy/opentofu/variables.tf
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

# The pinned default of one variable block.
#
# 🔴 THE `^  default` ANCHOR IS LOAD-BEARING, and a looser match false-passes.
# Both of these descriptions are heredocs that DISCUSS versions and the bump
# procedure, so a sentence containing `default = "2.14.4"` as prose is entirely
# plausible — and a version-shaped string anywhere in the block would satisfy an
# unanchored grep while the real default stayed empty. Measured: it does. Attribute
# indentation is exactly two spaces and is guaranteed by `tofu fmt -check`, which
# runs earlier in this same CI job; heredoc bodies are indented further and fmt
# never reformats them, so the anchor separates the two reliably.
pinned_default() {
	sed -n "/^variable \"$2\"/,/^}/p" "$1" \
		| sed -n 's/^  default[[:space:]]*=[[:space:]]*"\([0-9]\+\.[0-9]\+\.[0-9]\+\)".*/\1/p' \
		| head -1
}

# 2. The chart the above was verified against, in the module...
module_pin="$(pinned_default "$module" chart_version)"
if [ -z "$module_pin" ]; then
	fail "$module: variable chart_version has no pinned default.
      An empty default installs whatever upstream last shipped, so the checksum
      template, the reloader's watched paths and the lame-duck floors this module is
      sized against are all unverified — and the pod-template hash includes the
      chart's own version label, so a new upstream release rolls the broker on the
      next otherwise-unrelated apply."
fi

# 3. ...and in the root that actually passes it.
root_pin="$(pinned_default "$root" nats_chart_version)"
if [ -z "$root_pin" ]; then
	fail "$root: variable nats_chart_version has no pinned default.
      The module's own pin is not enough — it is in fact the WEAKER of the two. The
      root passes chart_version through unconditionally, and an argument that is
      passed explicitly always beats the module's default, so on the shipped path
      this value is the only one that decides anything. The module's pin protects a
      direct consumer of the module; this one protects us."
fi

# 4. And they must agree, or the module's carefully verified pin documents a chart
#    the instance does not run. Both being semver-shaped is not enough: the root
#    wins, so a drift here means every "verified at 2.14.4" claim in the module is
#    describing something else. Measured: root=9.9.9 with module=2.14.4 satisfied
#    both checks above and said OK.
if [ -n "$module_pin" ] && [ -n "$root_pin" ] && [ "$module_pin" != "$root_pin" ]; then
	fail "the two chart pins disagree: $module pins $module_pin but $root pins $root_pin.
      The root's value is the one that installs, so the module's comments — the
      checksum template, the reloader paths, the lame-duck floors it says were
      verified — would be describing a chart nobody deploys."
fi

if [ "$rc" -eq 0 ]; then
	echo "OK: NATS config adoption is enforced (checksum annotation set, chart pinned to $root_pin in module + root)."
fi

exit "$rc"
