#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Every container image a tracked shell script pulls must be addressed by DIGEST.
#
# WHY
#
# A tag is a pointer somebody else owns. `registry:2` is not a version — it is a
# name upstream can repoint, fetched anonymously from Docker Hub, and that pull
# has to succeed for a pull request to go green. Both halves have already cost a
# release here: the v0.15.0 stable tag stopped because that one pull came back
# `500 Internal Server Error`, and because the publish jobs depend on the upgrade
# drill, every one of them skipped.
#
# The digest fixes the half that is ours to fix. It does not make a registry
# reachable — nothing here can — but it makes the pull REPRODUCIBLE (two runs of
# the same commit start the same bytes), it makes an upstream repush visible as a
# diff rather than as a behaviour change with no commit behind it, and it lets a
# cached or mirrored copy satisfy the pull, because the content is addressed
# rather than named.
#
# 🔴 THE TAG STAYS, next to the digest, as `image:tag@sha256:...`. Docker ignores
# the tag and pulls the digest, so it costs nothing at runtime, and it is the only
# thing that tells a reader WHICH VERSION they are on. A bare digest is a fact
# nobody can act on, which is how a pin ends up frozen: the next person cannot see
# what bumping it would mean, so they do not bump it.
#
# WHAT IT CHECKS — two rules, because an image reference reaches `docker` two ways
#
#   A. ASSIGNMENT. A variable whose name contains "image" and whose value carries
#      a literal `name:tag` must also carry `@sha256:`. This is where the pinned
#      references in this tree actually live (migration-diff, integration-tests,
#      check-prometheus-rules, dr-rig), so it is the rule that does most of the
#      work.
#
#   B. INLINE. A `docker run` / `pull` / `create` command must not carry a bare
#      `name:tag` literal at all. Rule A alone would have missed the site that
#      broke the release: hack/upgrade-rig.sh passed `registry:2` inline, with no
#      variable anywhere for a name-based rule to find. A gate that only sees the
#      well-formed shape is a gate that cannot see the defect.
#
# WHAT IT DOES NOT CHECK
#
#   - That the digest is current. A frozen pin is its own hazard, and the answer
#     to it is a bumper plus a liveness check — hack/check-ko-base-pin.sh does
#     exactly that, and says why, for the base image every published artifact is
#     built on. The images here are development and CI tooling that never reaches
#     an operator, so they take the pin without the bumper.
#   - That the digest resolves. That needs the network at check time, which would
#     put someone else's registry back in front of a green gate — the failure this
#     script is a response to.
#   - Go, or anything that is not a tracked *.sh. dcctl starts the same local
#     registry container from backend/cli/bootstrap/steps.go, and that constant is
#     pinned by TestLocalRegistryImageIsDigestPinned in that package rather than
#     here. Teaching a shell tokenizer to read Go would trade a rule that is right
#     for one that is nearly right.
#
# Run `--self-test` first, as every guard here does: this is a text scanner, and a
# text scanner with a wrong pattern reports a clean tree it never read.

set -euo pipefail

usage() {
  echo "usage: $(basename "$0") [--self-test] [file ...]" >&2
  exit 2
}

SELF_TEST=0
case "${1:-}" in
  --self-test)
    SELF_TEST=1
    shift
    ;;
  -h | --help) usage ;;
esac

# ---------------------------------------------------------------------------
# scan: the whole checker, as one awk program over the files named in "$@".
#
# It works on LOGICAL lines — backslash continuations joined — because
# hack/migration-diff.sh spreads its `docker run` over five physical lines, and a
# per-line scan would look at each of them and find an image on none.
#
# Prints one `path:line: message` per violation; exits 1 if there were any.
# ---------------------------------------------------------------------------
scan() {
  awk '
    # A quoted string is not shell we can tokenize, and a comment is not code at
    # all. Both become whitespace, so the tokenizer sees only bare words. A "#"
    # opens a comment only at the start of a word, which leaves a digest and a
    # URL fragment alone.
    function bare(l,   i, c, out, q) {
      q = ""; out = ""
      for (i = 1; i <= length(l); i++) {
        c = substr(l, i, 1)
        if (q != "") { if (c == q) { q = ""; out = out " " }; continue }
        if (c == "\047" || c == "\042") { q = c; out = out " "; continue }
        if (c == "#" && (i == 1 || substr(l, i - 1, 1) ~ /[ \t]/)) break
        out = out c
      }
      return out
    }

    function report(file, line, msg) {
      printf "%s:%d: %s\n", file, line, msg
      bad++
    }

    function check(l, ln,   s, n, tok, i, t, j, verb) {
      # ---- Rule A: an image-named variable carrying a literal name:tag ----
      if (l ~ /^[ \t]*(local[ \t]+|export[ \t]+|readonly[ \t]+|declare[ \t]+(-[a-zA-Z]+[ \t]+)?)?[A-Za-z_][A-Za-z0-9_]*[Ii][Mm][Aa][Gg][Ee][A-Za-z0-9_]*=/) {
        if (l ~ /[a-z0-9][a-zA-Z0-9._\/-]*:[a-zA-Z0-9][a-zA-Z0-9._-]*/ && l !~ /@sha256:/) {
          report(FILENAME, ln, "image reference is not pinned by digest — write image:tag@sha256:... (why: the header of hack/check-image-pins.sh)")
        }
      }

      # ---- Rule B: a bare name:tag literal on a docker run/pull/create ----
      s = bare(l)
      n = split(s, tok, /[ \t]+/)
      verb = 0
      for (i = 1; i <= n && !verb; i++) {
        if (tok[i] !~ /(^|\/)docker$/) continue
        for (j = i + 1; j <= n; j++) {
          if (tok[j] ~ /^-/) continue
          if (tok[j] == "run" || tok[j] == "pull" || tok[j] == "create") verb = j
          break
        }
      }
      if (!verb) return
      for (i = verb + 1; i <= n; i++) {
        t = tok[i]
        gsub(/^[<>|&(){};]+/, "", t)
        gsub(/[<>|&(){};]+$/, "", t)
        if (t ~ /^-/) continue
        if (index(t, "@sha256:") > 0) continue
        if (t ~ /^[a-z0-9][a-zA-Z0-9._\/-]*:[a-zA-Z0-9][a-zA-Z0-9._-]*$/) {
          report(FILENAME, ln, "docker is given the bare tag \"" t "\" — pull by digest, as image:tag@sha256:... (why: the header of hack/check-image-pins.sh)")
        }
      }
    }

    FNR == 1 { if (acc != "") check(acc, start); acc = ""; start = 0 }
    {
      line = $0
      if (acc == "") start = FNR
      if (line ~ /\\$/) { sub(/\\$/, "", line); acc = acc line; next }
      acc = acc line
      check(acc, start)
      acc = ""
    }
    END {
      if (acc != "") check(acc, start)
      exit(bad > 0 ? 1 : 0)
    }
  ' "$@"
}

main() {
  local -a files
  if [ -n "${DC_IMAGE_PIN_FILES+x}" ]; then
    # Set (possibly to nothing) only by the self-test, to exercise the empty
    # enumeration below without depending on the state of the working tree.
    read -r -a files <<<"${DC_IMAGE_PIN_FILES}"
  elif [ "$#" -gt 0 ]; then
    files=("$@")
  else
    cd "$(git rev-parse --show-toplevel)"
    # Derived from git rather than a hand-written list, for the reason
    # hack/shellcheck.sh gives: it picks up new scripts on its own, and it can
    # never reach into somebody's untracked scratch directory.
    mapfile -t files < <(git ls-files '*.sh')
  fi

  # An empty enumeration is how a scanner reports a clean tree it never opened.
  if [ "${#files[@]}" -eq 0 ]; then
    echo "::error::found no tracked *.sh files to scan — the enumeration is broken, not the tree" >&2
    return 1
  fi

  local out rc=0
  out="$(scan "${files[@]}")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: unpinned container image references (${#files[@]} scripts scanned)" >&2
    echo "$out" >&2
    return 1
  fi
  echo "OK: every container image reference in ${#files[@]} tracked scripts is pinned by digest"
}

# ---------------------------------------------------------------------------
# Self-test. Both directions on throwaway fixtures, and each defect is planted
# ALONE — a fixture carrying every defect at once is satisfied by a checker that
# finds any one of them.
# ---------------------------------------------------------------------------
self_test() {
  local tmp rc out
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  # 1. Clean, and deliberately full of the tokens a looser tokenizer mistakes for
  #    an image reference: a published host:port mapping, a volume mount, an
  #    --entrypoint path, a redirect, and a mode variable whose name ends in
  #    "images" but whose value is not one.
  cat >"$tmp/clean.sh" <<'FIXTURE'
#!/usr/bin/env bash
img="registry:2.8.3@sha256:aaaa"
docker run -d -p "127.0.0.1:5000:5000" -v "$PWD:/w" --name c "$img" >/dev/null
docker run --rm -u 0 -v "$d:/w" --entrypoint /bin/promtool \
  timescale/timescaledb:latest-pg16@sha256:bbbb check rules /w/x.yaml
docker pull "$img"
upgrade_images="pull"
FIXTURE
  if ! out="$(scan "$tmp/clean.sh" 2>&1)"; then
    echo "FAIL: the checker flagged a clean fixture:" >&2
    echo "$out" >&2
    return 1
  fi

  # 2. Rule A alone.
  printf '#!/usr/bin/env bash\npromtool_image="prom/prometheus:v3.5.0"\n' >"$tmp/a.sh"
  rc=0
  out="$(scan "$tmp/a.sh" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ] || ! grep -q 'a.sh:2: image reference is not pinned' <<<"$out"; then
    echo "FAIL: an unpinned image-named variable was not caught (rc=$rc): $out" >&2
    return 1
  fi

  # 3. Rule B alone, spread over a continuation and sharing its line with a
  #    published port. This is the shape that broke the release, and Rule A
  #    cannot see it: there is no variable to name.
  #
  # 🔴 THE OFFENDING TOKEN IS ASSEMBLED, NOT WRITTEN. This script is a tracked
  # *.sh, so the gate scans it too — and it caught this fixture the moment the
  # file was added, reporting the line below as a real unpinned pull. The
  # comfortable fix is to exempt this file, and it is the wrong one: an
  # exclusion list is a second thing to keep true, and it would exempt every
  # future line here rather than this one string. Splitting the literal keeps
  # the gate free of exceptions and keeps the fixture honest — the file the
  # scanner writes still contains the bare tag, which is what is under test.
  local bad_ref="registry"
  bad_ref="${bad_ref}:2"
  cat >"$tmp/b.sh" <<FIXTURE
#!/usr/bin/env bash
docker run -d --restart=always \\
  -p "127.0.0.1:5000:5000" --name kind-registry ${bad_ref} >/dev/null
FIXTURE
  rc=0
  out="$(scan "$tmp/b.sh" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ] || ! grep -q "b.sh:2: docker is given the bare tag \"${bad_ref}\"" <<<"$out"; then
    echo "FAIL: an inline unpinned docker run was not caught (rc=$rc): $out" >&2
    return 1
  fi

  # 4. A commented-out recipe is documentation, not a pull. Several integration
  #    suites carry `# docker run ... postgres:16` in their header; failing on
  #    those would teach the next person to delete the recipe.
  printf '#!/usr/bin/env bash\n# docker run -d --name x postgres:16\n' >"$tmp/c.sh"
  if ! out="$(scan "$tmp/c.sh" 2>&1)"; then
    echo "FAIL: a commented-out example was treated as a pull: $out" >&2
    return 1
  fi

  # 5. The enumeration itself, which is the way this check would go silently
  #    vacuous: no files scanned, nothing found, exit 0.
  rc=0
  out="$(DC_IMAGE_PIN_FILES="" main 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ] || ! grep -q 'found no tracked' <<<"$out"; then
    echo "FAIL: an empty file list was not refused (rc=$rc): $out" >&2
    return 1
  fi

  echo "self-test passed: a clean fixture passes, each rule fails on its own defect, and an empty enumeration is refused"
}

if [ "$SELF_TEST" = "1" ]; then
  self_test
else
  main "$@"
fi
