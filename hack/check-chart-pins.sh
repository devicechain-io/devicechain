#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Every third-party Helm chart this deployment installs must be PINNED to an exact
# version, in the module that installs it and in the root that passes it, at the same
# value.
#
# WHY
#
# An unpinned chart version is not "we track upstream" — nothing here tracks anything.
# It has three costs, and the first one is the surprise:
#
#  1. It must be RESOLVED AT APPLY TIME, so the chart repository becomes a dependency
#     of planning, not just of installing. When that lookup fails, the helm provider
#     reports "Provider produced inconsistent final plan ... .version: was known, but
#     now unknown" — which names neither the chart nor the network. That cost two
#     failed bootstraps in one afternoon, off a release-asset host that returned 503
#     three separate times that day.
#  2. Two bootstraps of the same commit install different software, so "it worked
#     yesterday" stops being evidence about anything.
#  3. Several of these modules are sized against chart INTERNALS — the nats module's
#     config-checksum template and helm timeout, the ingress-nginx module's snippet
#     and risk-level values, the cert-manager module's CRD-install value, the
#     monitoring module's install-once CRDs. Those were verified at a version.
#     Upstream moving underneath them surfaces as an admission rejection or an
#     unadopted config, never as a version diff.
#
# 🔴 IT WALKS FROM THE `helm_release` RESOURCES, NOT FROM THE VARIABLES
#
# The obvious design — enumerate the `*_chart_version` variables and check each is
# pinned — has a hole that was MEASURED on the first draft of this script: delete a
# chart's root variable and pass `chart_version = ""` as a literal in the module call,
# and the gate reports "OK: all 5 charts are pinned" while that chart installs latest.
# Both the discovered set and any count floor derived from it shrink in lockstep with
# the edit, because they are read out of the same tree the edit changes.
#
# So the walk starts from what actually INSTALLS a chart — a `helm_release` whose
# repository is a remote URL — and works outward to the value that decides its
# version. A chart cannot leave that set without ceasing to be installed, which is the
# only kind of removal that should silently stop being checked.
#
# WHAT IT CHECKS, AND WHY BOTH SIDES
#
# The root's value is the one that INSTALLS: it is passed to the module explicitly,
# and an explicit argument always beats the module's own default. So the root pin is
# the load-bearing one, and the module pin protects a direct consumer of the module.
# They must also AGREE, because every "verified at version X" claim in a module's
# comments is describing the chart the ROOT chose. A module pinned to 2.14.4 under a
# root pinned to 9.9.9 satisfies both checks individually and documents fiction.
#
# WHAT IT DOES NOT CHECK
#
# That the pinned version exists, or that the chart still behaves the way the module
# assumes. Both would need the chart fetched at check time, making a green gate depend
# on someone else's CDN — the exact failure this repo already fights. Those live with
# whoever bumps the pin, which is why each variable's description says what to
# re-verify. There is also a RUNTIME half to this guard: each of these variables
# carries a validation block refusing anything that is not an exact version, so a
# tfvars override cannot do what this script stops the source from doing. Those blocks
# are exercised by hack/check-tofu-validations.sh.

set -euo pipefail

cd "$(dirname "$0")/.."

tofu_root=deploy/opentofu
main=$tofu_root/main.tf
root_vars=$tofu_root/variables.tf
rc=0

fail() {
	echo "FAIL: $1" >&2
	rc=1
}

# An exact chart version and nothing else.
#
# 🔴 THE ANCHORED TAIL IS LOAD-BEARING. A trailing `[^"]*` was measured to accept
# `"4.15.1 - 5.0.0"` — a helm version RANGE, which is a constraint resolved at apply
# time wearing a pin's clothes, i.e. precisely the failure this gate exists to stop.
# The optional suffix admits pre-release tags (`1.2.3-rc.1`) and nothing with a space
# or a comparator in it.
exact='v\?[0-9]\+\.[0-9]\+\.[0-9]\+\(-[0-9A-Za-z.]\+\)\?'

# The pinned default of one variable block, or empty.
#
# 🔴 HEREDOC BODIES ARE DELETED BEFORE MATCHING, and that is not tidiness. These
# descriptions discuss versions and bump procedures, so a version-shaped string inside
# the prose is not hypothetical — several blocks contain one today. Anchoring on
# `^  default` (two spaces, the indentation `tofu fmt` guarantees for an attribute)
# separates prose from attribute for ordinary bodies, but fmt does NOT reformat
# heredoc bodies, so nothing stops a line inside one from sitting at two spaces.
# Measured: a two-space `  default = "v1.21.1"` planted in a description passed the
# anchor alone while the real default was empty. Cutting the heredocs out first closes
# that, and leaves the anchor as the second line of defence rather than the only one.
pinned_default() {
	sed -n "/^variable \"$2\" {/,/^}/p" "$1" \
		| sed '/<<-\?[A-Z]\+$/,/^[[:space:]]*[A-Z]\+$/d' \
		| sed -n "s/^  default[[:space:]]*=[[:space:]]*\"\($exact\)\".*/\1/p" \
		| head -1
}

# Strip HCL end-of-line comments so a trailing `# pinned` cannot break an expression
# match. Not a full parser — a `#` inside a string would be cut too — but no line this
# reads carries one, and failing to match is a loud failure here, never a silent pass.
uncomment() { sed 's/[[:space:]]*#.*$//'; }

# --- 1. Find every remotely-sourced helm_release and the expression that versions it.
#
# Emits: <module dir> TAB <release name> TAB <version expression>
releases=$(
	for f in "$tofu_root"/modules/*/main.tf; do
		dir=$(basename "$(dirname "$f")")
		uncomment <"$f" | awk -v dir="$dir" '
			/^resource "helm_release"/ { inres = 1; name = $3; gsub(/"/, "", name); repo = ""; ver = "<none>"; next }
			inres && /^[[:space:]]*repository[[:space:]]*=/ { repo = $3; gsub(/"/, "", repo) }
			inres && /^[[:space:]]*version[[:space:]]*=/    { sub(/^[[:space:]]*version[[:space:]]*=[[:space:]]*/, ""); ver = $0 }
			inres && /^}/ {
				inres = 0
				if (repo ~ /^https?:\/\//) print dir "\t" name "\t" ver
			}
		'
	done
)

# 🔴 THE PARSER'S OWN CONTROL. Everything below loops over what that awk found, so a
# walk that matches nothing would report a clean pass over zero charts — the shape of
# gate that cannot fail. No count floor above zero is needed or wanted: a chart leaves
# this set only by ceasing to be installed, and a chart nobody installs is correctly
# not checked.
found=$(printf '%s' "$releases" | grep -c . || true)
if [ "$found" -eq 0 ]; then
	echo "FAIL: found no remotely-sourced helm_release resources under $tofu_root/modules." >&2
	echo "      This check iterates over what it parses, so zero would otherwise be" >&2
	echo "      reported as success. Either every chart moved or this parser is broken." >&2
	exit 1
fi

while IFS=$'\t' read -r dir release ver; do
	[ -n "$dir" ] || continue

	# --- 2. The version must come from exactly one module variable.
	case "$ver" in
	var.*)
		mvar=${ver#var.}
		;;
	*)
		fail "modules/$dir: helm_release \"$release\" versions its chart with \`$ver\`.
      It must read a single module variable, so the pin has one name that the root,
      terraform.tfvars.example and this check can all talk about. A literal, a
      conditional or a missing version line all put the deciding value somewhere
      none of them look."
		continue
		;;
	esac

	# --- 3. That variable must be pinned in the module.
	module_file=$tofu_root/modules/$dir/main.tf
	module_pin=$(pinned_default "$module_file" "$mvar")
	if [ -z "$module_pin" ]; then
		fail "$module_file: variable $mvar has no exact pinned default.
      It versions helm_release \"$release\", so an empty or range-shaped default
      installs whatever upstream last shipped and leaves anything this module is
      sized against chart internals for unverified."
	fi

	# --- 4. And wherever the root instantiates this module, it must pass a pinned
	#        root variable of its own — or pass nothing and inherit the module's.
	while IFS=$'\t' read -r rvar; do
		[ -n "$rvar" ] || continue
		case "$rvar" in
		var.*) ;;
		*)
			fail "$main passes $mvar=$rvar to modules/$dir.
      An explicit argument BEATS the module's pinned default, so this — not the
      module — is the value that installs. It must be a root variable this check can
      follow, never a literal."
			continue
			;;
		esac
		rvar=${rvar#var.}
		root_pin=$(pinned_default "$root_vars" "$rvar")
		if [ -z "$root_pin" ]; then
			fail "$root_vars: variable $rvar has no exact pinned default.
      This is the WEAKER-looking pin and the one that actually decides: the root
      passes it to modules/$dir explicitly, and an explicit argument beats a module
      default."
		elif [ -n "$module_pin" ] && [ "$module_pin" != "$root_pin" ]; then
			fail "the two pins for $mvar disagree: $module_file pins $module_pin but
      $root_vars pins $root_pin via $rvar. The root's value installs, so the
      module's comments would be describing a chart nobody deploys."
		fi
		[ "$rc" -eq 0 ] && echo "  $dir/$release <- $rvar = $root_pin"
	done <<<"$(
		uncomment <"$main" | awk -v dir="./modules/$dir" -v mvar="$mvar" '
			/^module "/                        { insrc = 1; src = "" }
			insrc && /^[[:space:]]*source[[:space:]]*=/ { src = $3; gsub(/"/, "", src) }
			insrc && $1 == mvar && $2 == "="   { if (src == dir) print $3 }
			/^}/                               { insrc = 0 }
		'
	)"
done <<<"$releases"

if [ "$rc" -eq 0 ]; then
	echo "OK: all $found third-party charts are pinned to an exact version, module and root agreeing."
fi

exit "$rc"
