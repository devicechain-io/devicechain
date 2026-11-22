#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The fleet roots (deploy/fleet/*) and their ENFORCED state and plan encryption, checked three ways
# so that a key supplied by CI cannot hide a missing `enforced`:
#
#   1. WITH a throwaway pbkdf2 key (TF_ENCRYPTION): fmt, `init -backend=false`, validate and
#      `tofu test` (with a non-zero pass count, because `tofu test` with no test files succeeds).
#   2. A static test (deploy/fleet_roots_test.go) asserts `enforced = true` under both `state` and
#      `plan` in the files themselves.
#   3. THIS script's negative step: a plan with NO key must FAIL, and the same plan WITH the key
#      must succeed (the counterweight: a gate that only refuses is an outage). It runs on a copy of
#      the root without its backend block, so the refusal it sees is the encryption's, not a missing
#      backend's. Plus: `init` WITH the backend block and no backend configuration must fail, because
#      a fleet root has no implicit local backend.
#
# Offline: a dummy access token and -refresh=false, so no cloud call is made.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

export TF_IN_AUTOMATION=1 GOOGLE_OAUTH_ACCESS_TOKEN=dummy GOOGLE_CLOUD_PROJECT=example-project
KEY='key_provider "pbkdf2" "ci" { passphrase = "ci-throwaway-passphrase-0123456789" }
method "aes_gcm" "ci" { keys = key_provider.pbkdf2.ci }
state { method = method.aes_gcm.ci }
plan { method = method.aes_gcm.ci }'

# A scratch copy left behind by an interrupted run would be discovered as a root.
trap 'rm -rf "$repo_root"/deploy/fleet/.neg.*' EXIT

roots=()
while IFS= read -r f; do roots+=("$(dirname "$f")"); done < <(find deploy/fleet -mindepth 2 -maxdepth 2 -name backend.tf | sort)
if [ "${#roots[@]}" -eq 0 ]; then
	echo "FAIL: no fleet root (deploy/fleet/*/backend.tf) was found: a gate over nothing must not pass" >&2
	exit 1
fi
# Every directory of .tf files under deploy/fleet must have declared a backend: an implicit local
# backend inside a Job pod is the failure this rule exists to prevent.
while IFS= read -r d; do
	[ -f "$d/backend.tf" ] || { echo "FAIL: $d holds .tf files but no backend.tf" >&2; exit 1; }
done < <(find deploy/fleet -mindepth 2 -maxdepth 2 -name '*.tf' -exec dirname {} \; | sort -u)

VARS=(-var project_id=example-project -var location=us-central1-a -var cluster_name=dcf-aaaaaaaaaaaaaaaaaaaaaaaaaa
	-var cluster_uid=11111111-1111-1111-1111-111111111111 -var ownership_nonce=bbbbbbbbbbbbbbbbbbbbbbbbbb -var data_class=retained)

for root in "${roots[@]}"; do
	echo "::group::$root"

	# 1. With the key.
	(
		export TF_ENCRYPTION="$KEY"
		cd "$root"
		tofu fmt -check -recursive
		"$repo_root/hack/ci-retry.sh" tofu init -backend=false
		tofu validate
		log="$(mktemp)"
		tofu test -no-color | tee "$log"
		grep -Eq '^Success! [1-9][0-9]* passed, 0 failed\.$' "$log"
		rm -f "$log"
	)

	# 3a. A plan with no key fails, on a copy with no backend block (so the refusal is the encryption's).
	# The copy lives beside the real roots because the module is referenced by a relative path.
	neg="$(mktemp -d "$repo_root/deploy/fleet/.neg.XXXXXX")"
	cp -R "$root/." "$neg/"
	rm -rf "$neg/backend.tf" "$neg/tests" "$neg/.terraform" "$neg/.terraform.lock.hcl"
	(
		cd "$neg"
		TF_ENCRYPTION="$KEY" "$repo_root/hack/ci-retry.sh" tofu init -backend=false >/dev/null
		# The counterweight first: with the key, the same plan succeeds.
		TF_ENCRYPTION="$KEY" tofu plan -refresh=false -input=false -no-color "${VARS[@]}" -out=with-key.plan >/dev/null
		grep -aq . with-key.plan
		# The saved plan is ENCRYPTED: it must not be plain JSON or a plain zip.
		magic="$(head -c 2 with-key.plan)"
		if [ "$magic" = 'PK' ]; then
			echo "FAIL: $root: the saved plan is a plain archive, not encrypted" >&2
			exit 1
		fi
		rm -f with-key.plan
		out="$(unset TF_ENCRYPTION; tofu plan -refresh=false -input=false -no-color "${VARS[@]}" -out=no-key.plan 2>&1)" && rc=0 || rc=$?
		if [ "$rc" -eq 0 ] || [ -e no-key.plan ]; then
			echo "FAIL: $root: a plan with NO encryption key was not refused (rc=$rc)" >&2
			exit 1
		fi
		if ! grep -q 'encryption' <<<"$out"; then
			echo "FAIL: $root: the plan was refused, but not by the encryption configuration:" >&2
			sed -n '1,20p' <<<"$out" >&2
			exit 1
		fi
		echo "  no key: plan refused by the encryption configuration, as it must be"
	)
	rm -rf "$neg"

	# 3b. `init` WITH the backend block and no backend configuration fails: no implicit local backend.
	neg="$(mktemp -d "$repo_root/deploy/fleet/.neg.XXXXXX")"
	cp -R "$root/." "$neg/"
	rm -rf "$neg/tests" "$neg/.terraform" "$neg/.terraform.lock.hcl"
	if (cd "$neg" && TF_ENCRYPTION="$KEY" tofu init -input=false -no-color >/dev/null 2>&1); then
		rm -rf "$neg"
		echo "FAIL: $root: init succeeded with no backend configuration" >&2
		exit 1
	fi
	rm -rf "$neg"
	echo "  init with no backend configuration: refused, as it must be"
	echo "::endgroup::"
done
echo "ok: ${#roots[@]} fleet root(s)"
