#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Check UNTAGGED go test -json or -v output, from a file or stdin.
# Exit 1: unreviewed skip; exit 2: no usable evidence (or invalid allowlist).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

check() {
  "${PYTHON:-python3}" -c "$(cat <<'PY'
import json
import re
import sys
from pathlib import Path

sys.stdin.reconfigure(encoding='utf-8')
sys.stdout.reconfigure(encoding='utf-8')
sys.stderr.reconfigure(encoding='utf-8')

def refuse(message):
    print('ERROR: ' + message, file=sys.stderr)
    sys.exit(2)

try:
    allowed = set()
    for number, line in enumerate(Path(sys.argv[1]).read_text(encoding='utf-8').splitlines(), 1):
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        entry, comment, reason = line.partition('#')
        fields = entry.split()
        if len(fields) != 2 or not comment or not reason.strip():
            refuse(f'allowlist line {number} needs package TestName # reason')
        if tuple(fields) in allowed:
            refuse(f'duplicate allowlist entry on line {number}')
        allowed.add(tuple(fields))
    raw = sys.stdin.read() if sys.argv[2] == '-' else Path(sys.argv[2]).read_text(encoding='utf-8-sig')
except (OSError, UnicodeError) as error:
    refuse(str(error))
lines = [line for line in raw.splitlines() if line.strip()]
if not lines:
    refuse('empty test output; examined nothing')

skips, messages, completed = [], {}, 0
if lines[0].lstrip().startswith('{'):
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            refuse('malformed go test JSON: ' + line)
        if not isinstance(event, dict) or not isinstance(event.get('Package'), str):
            refuse('not a go test JSON event: ' + line)
        action, package, test = event.get('Action'), event['Package'], event.get('Test')
        if action not in {'start', 'run', 'pause', 'cont', 'output', 'pass', 'fail', 'skip', 'bench'}:
            refuse('unknown go test action: ' + str(action))
        if action == 'output':
            if not isinstance(event.get('Output'), str):
                refuse('output event without text')
            if test:
                messages.setdefault((package, test), []).append(event['Output'].rstrip())
        if action == 'skip' and test:
            skips.append((package, test))
        if action in {'pass', 'fail', 'skip'} and not test:
            completed += 1
else:
    # go test -v emits each package's buffered transcript before its summary.
    pending, output = [], []
    for line in lines:
        match = re.match(r'\s*--- SKIP: (\S+)', line)
        if match:
            pending.append(match[1])
        output.append(line)
        summary = re.match(r'^(?:ok\s+|FAIL\s+|\?\s+)(\S+)\s+', line)
        if summary:
            package = summary[1]
            for test in pending:
                skips.append((package, test))
                messages[(package, test)] = output[:]
            pending, output = [], []
            completed += 1
    if pending:
        refuse('SKIP without a package summary')
if not completed:
    refuse('no completed package results; output is unparseable or truncated')

bad = [key for key in skips if key not in allowed]
for package, test in bad:
    print(f'UNALLOWED SKIP: {package} {test}', file=sys.stderr)
    for message in messages.get((package, test), ['(no skip message emitted)']):
        print('  ' + message, file=sys.stderr)
if bad:
    sys.exit(1)
print(f'silent-skip gate: {completed} package results, {len(skips)} allowed skips')
PY
)" "$2" "$1"
}

self_test() (
  local tmp rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  printf '%s\n' 'example/pkg TestAllowed # OS gate fixture' > "$tmp/allowlist"
  cat > "$tmp/json" <<'EOF'
{"Action":"output","Package":"example/pkg","Test":"TestUnexpected","Output":"fixture missing\n"}
{"Action":"skip","Package":"example/pkg","Test":"TestUnexpected"}
{"Action":"pass","Package":"example/pkg"}
EOF
  rc=0; check "$tmp/json" "$tmp/allowlist" > "$tmp/result" 2>&1 || rc=$?
  [[ "$rc" == 1 ]] && grep -q 'fixture missing' "$tmp/result" || { cat "$tmp/result"; return 1; }
  sed 's/TestUnexpected/TestAllowed/g' "$tmp/json" > "$tmp/allowed"
  check "$tmp/allowed" "$tmp/allowlist"
  printf '    test.go:12: missing broker\n--- SKIP: TestUnexpected (0.00s)\nPASS\nok  example/pkg 0.01s\n' > "$tmp/verbose"
  rc=0; check "$tmp/verbose" "$tmp/allowlist" > "$tmp/result" 2>&1 || rc=$?
  [[ "$rc" == 1 ]] && grep -q 'missing broker' "$tmp/result" || { cat "$tmp/result"; return 1; }
  sed 's/TestUnexpected/TestAllowed/g' "$tmp/verbose" > "$tmp/allowed"
  check "$tmp/allowed" "$tmp/allowlist"
  for input in '' 'not a transcript' '{broken' '{"Action":"start","Package":"example/pkg"}'; do
    printf '%s' "$input" > "$tmp/invalid"
    rc=0; check "$tmp/invalid" "$tmp/allowlist" > "$tmp/result" 2>&1 || rc=$?
    [[ "$rc" == 2 ]] || { cat "$tmp/result"; return 1; }
  done
  printf 'example/pkg TestAllowed\n' > "$tmp/invalid-allowlist"
  rc=0; check "$tmp/allowed" "$tmp/invalid-allowlist" > "$tmp/result" 2>&1 || rc=$?
  [[ "$rc" == 2 ]]
  echo 'silent-skip self-test: PASS (unallowed skips red, allowed green, empty/malformed exit 2)'
)

if [[ "${1:-}" == --self-test && $# == 1 ]]; then
  self_test
elif [[ $# -le 1 && "${1:-}" != --* ]]; then
  check "${1:--}" "$ROOT/hack/silent-skip-allowlist.txt"
else
  echo "usage: $0 [--self-test | FILE | -]" >&2
  exit 2
fi
