#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Turns one phase of hack/perf-collect.sh output into the tables the round report needs.
#
#   hack/perf-analyze.sh --dir <collect out dir> --round r25 --phase R1 \
#       [--events N | --events-hypertable NAME] [--out report.md] [--allow-incomplete]
#   hack/perf-analyze.sh --self-test
#
#   1. CPU-seconds per 1,000 events, per component: the component's container CPU over the window
#      (cAdvisor's cumulative counters, before and after) divided by the events stored in that
#      window. Both sides are RATES over their own snapshot times, because the CPU snapshots and
#      the database snapshots are taken seconds apart; a rate cancels that skew, a difference of
#      raw totals would not.
#   2. A top-20 table per service and for NATS (`go tool pprof -top`, every pod's CPU profiles
#      merged), flat and cumulative, plus the allocation view (alloc_space).
#   3. NATS: RSS, JetStream memory and store, message rates, and the heaviest streams.
#   4. Databases: the top statements by total time, WAL and checkpoint deltas.
#
# EVENTS STORED. --events N is taken as given. Otherwise it is the rows inserted into
# --events-hypertable during the window (the event store's own pg_stat counters, in the
# timescaledb sysstat-delta). With neither flag the hypertable with the largest delta is used and
# the report SAYS IT WAS ASSUMED: a per-event number built on a guessed denominator is the kind
# of figure that gets quoted.
#
# 🔴 A PHASE THE COLLECTOR MARKED INCOMPLETE IS REFUSED (exit 1) unless --allow-incomplete, which
# stamps INCOMPLETE across the report. Pods the collector marked invalid are left out of every
# table and listed. A pod whose CPU counter went backwards (a container restart) is INVALID, never
# a negative or a zero.
#
# Requires jq, awk, and `go` for the pprof tables (without it they are reported as SKIPPED, loudly).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="$ROOT/hack/$(basename "${BASH_SOURCE[0]}")"
jq_real="${JQ:-jq}"
_jq() { "$jq_real" "$@" | tr -d '\r'; }
JQ=_jq
GO="${GO:-go}"

say() { echo "perf-analyze: $*" >&2; }
usage() { sed -n '4,9p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2; }

dir="" round="" phase="" events="" hyper="" outfile="" allow_incomplete=0 self=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) dir="$2"; shift 2 ;;
    --round) round="$2"; shift 2 ;;
    --phase) phase="$2"; shift 2 ;;
    --events) events="$2"; shift 2 ;;
    --events-hypertable) hyper="$2"; shift 2 ;;
    --out) outfile="$2"; shift 2 ;;
    --allow-incomplete) allow_incomplete=1; shift ;;
    --self-test) self=1; shift ;;
    -h|--help) usage ;;
    *) say "unknown argument: $1"; usage ;;
  esac
done

# pprof_in <dir> <pprof args...>: run `go tool pprof` from inside <dir>, so profile paths are relative.
# pprof treats an argument it cannot resolve as a file as a URL, and a Git Bash for Windows absolute
# path ("C:/...") that does not resolve from the current directory fails with "lookup C: no such
# host" and an empty table. A "./name" path is always a file.
pprof_in() { local dir="$1"; shift; ( cd "$dir" && "$GO" tool pprof "$@" ); }

# cpu_table <dir> <prefix> <manifest>: "component pods cpu_seconds window_seconds" per component, and
# lines "INVALID pod reason" for pods whose counters cannot be trusted. Reads the cAdvisor files.
cpu_rows() {
  local d="$1" pre="$2" man="$3" f
  # pod -> component TSV
  "$JQ" -r '.pods[] | [.pod, .component] | @tsv' "$man" >"$d/.pod-component.tsv"
  "$JQ" -r '.artifacts[] | select(.invalid == true) | .pod' "$man" | sort -u >"$d/.invalid-pods.txt"
  for f in "$d/$pre"-node-*-cadvisor-pre.prom; do
    [[ -e "$f" ]] || continue
    cat "$f"
  done >"$d/.cad-pre.all"
  for f in "$d/$pre"-node-*-cadvisor-post.prom; do
    [[ -e "$f" ]] || continue
    cat "$f"
  done >"$d/.cad-post.all"
  awk -v comp="$d/.pod-component.tsv" -v bad="$d/.invalid-pods.txt" '
    BEGIN { while ((getline l < comp) > 0) { split(l, a, "\t"); c[a[1]] = a[2] } while ((getline l < bad) > 0) b[l] = 1 }
    function label(s, k,   m) { if (match(s, k "=\"[^\"]*\"")) { m = substr(s, RSTART + length(k) + 2, RLENGTH - length(k) - 3); return m } return "" }
    FNR == 1 { file++ }
    /^container_cpu_usage_seconds_total\{/ {
      pod = label($0, "pod"); ctr = label($0, "container")
      if (pod == "" || ctr == "" || ctr == "POD") next
      n = split($0, parts, " "); val = parts[n - 1]; ts = parts[n]
      if (n < 3) { val = parts[n]; ts = 0 }
      key = pod SUBSEP ctr
      if (file == 1) { pre[key] = val; pre_ts[pod] = ts; seen[pod] = 1 } else { post[key] = val; post_ts[pod] = ts }
    }
    END {
      for (k in pre) { split(k, kk, SUBSEP); p = kk[1]
        if (!(k in post)) { bad_pod[p] = "a container present before the window is gone after it (restart or replacement)"; continue }
        d = post[k] - pre[k]
        if (d < 0) { bad_pod[p] = "a container CPU counter went backwards (restart)"; continue }
        cpu[p] += d }
      for (p in seen) {
        if (p in b) { print "INVALID\t" p "\tthe collector marked its artifacts invalid"; continue }
        if (p in bad_pod) { print "INVALID\t" p "\t" bad_pod[p]; continue }
        if (!(p in c)) continue
        w = (post_ts[p] - pre_ts[p]) / 1000
        if (w <= 0) { print "INVALID\t" p "\tno usable sample timestamps"; continue }
        print "POD\t" c[p] "\t" p "\t" cpu[p] "\t" w
      }
    }' "$d/.cad-pre.all" "$d/.cad-post.all"
}

analyze() {
  local d="$1" rnd="$2" ph="$3" pre man
  pre="$rnd-$ph"; man="$d/$pre-manifest.json"
  [[ -s "$man" ]] || { say "no manifest at $man: run hack/perf-collect.sh first"; return 1; }
  if [[ "$("$JQ" -r .complete "$man")" != true ]]; then
    if [[ "$allow_incomplete" -ne 1 ]]; then
      say "REFUSED: $man says the collection was INCOMPLETE ($("$JQ" '.failures | length' "$man") failure(s)); fix and re-collect, or pass --allow-incomplete"
      return 1
    fi
    echo "> **INCOMPLETE COLLECTION** -- the numbers below are missing what the manifest lists as failed."
    echo
  fi
  echo "# $rnd $ph -- profile analysis"
  echo
  echo "Namespace \`$("$JQ" -r .namespace "$man")\`; collected $("$JQ" -r .startedAt "$man") to $("$JQ" -r .finishedAt "$man"); steady marker checked: $("$JQ" -r .steady.checked "$man")."
  echo

  # ---- events stored in the window, as a rate
  local tsd ev_rate="" ev_note=""
  local tsds=("$d/$pre"-timescaledb-*-sysstat-delta.json)
  tsd=""; [[ -e "${tsds[0]}" ]] && tsd="${tsds[0]}"
  if [[ -n "$events" ]]; then
    ev_note="--events $events (given)"
  elif [[ -n "$tsd" ]] && [[ "$("$JQ" -r .valid "$tsd")" == true ]]; then
    local name n
    if [[ -n "$hyper" ]]; then
      name="$hyper"; n="$("$JQ" -r --arg h "$hyper" '.hypertables[$h] // empty' "$tsd")"
      [[ -n "$n" ]] || { say "the hypertable '$hyper' is not in $tsd (have: $("$JQ" -r '.hypertables | keys | join(", ")' "$tsd"))"; return 1; }
      ev_note="rows inserted into hypertable \`$name\` (pg_stat_user_tables n_tup_ins over its chunks)"
    else
      name="$("$JQ" -r '.hypertables | to_entries | sort_by(-.value) | .[0].key // empty' "$tsd")"
      n="$("$JQ" -r '.hypertables | to_entries | sort_by(-.value) | .[0].value // empty' "$tsd")"
      [[ -n "$name" ]] || { say "no hypertable inserts in the window; pass --events N"; return 1; }
      ev_note="**ASSUMED**: hypertable \`$name\` had the most rows inserted ($("$JQ" -r '.hypertables | to_entries | sort_by(-.value) | map("\(.key)=\(.value)") | join(", ")' "$tsd")); pass --events-hypertable to pin it"
    fi
    events="$n"
    local tw; tw="$("$JQ" -r '(.from | sub("\\.[0-9]+";"") | sub("\\+00:00$";"Z") | fromdateiso8601) as $a | (.to | sub("\\.[0-9]+";"") | sub("\\+00:00$";"Z") | fromdateiso8601) as $b | ($b - $a)' "$tsd" 2>/dev/null || echo 0)"
    [[ "${tw:-0}" -gt 0 ]] || { say "the event-store snapshots have no usable window; pass --events N"; return 1; }
    ev_rate="$(awk -v n="$events" -v w="$tw" 'BEGIN { printf "%.6f", n / w }')"
  else
    say "no events count: pass --events N (the event-store sysstat delta is missing or invalid)"; return 1
  fi
  if [[ -z "$ev_rate" ]]; then
    # --events given: rate over the CPU window below
    ev_rate=""
  fi

  # ---- CPU-seconds per 1,000 events
  echo "## CPU-seconds per 1,000 events"
  echo
  echo "Events stored: $events -- $ev_note"
  echo
  local rows; rows="$(cpu_rows "$d" "$pre" "$man")"
  if grep -q '^INVALID' <<<"$rows"; then
    echo "Pods left out as INVALID:"
    awk -F'\t' '$1 == "INVALID" { print "- " $2 ": " $3 }' <<<"$rows"
    echo
  fi
  echo "| component | pods | CPU-s over window | avg cores | CPU-s per 1,000 events |"
  echo "|---|---:|---:|---:|---:|"
  awk -F'\t' -v events="$events" -v evrate="$ev_rate" '
    $1 == "POD" { cpu[$2] += $4; pods[$2]++; w[$2] = $5 > w[$2] ? $5 : w[$2]; if ($5 > maxw) maxw = $5 }
    END {
      for (c in cpu) {
        rate = cpu[c] / w[c]                       # cores
        if (evrate != "") per1k = rate / evrate * 1000   # both sides as rates
        else per1k = cpu[c] / (events / 1000)
        printf "| %s | %d | %.1f | %.2f | %.3f |\n", c, pods[c], cpu[c], rate, per1k
      }
    }' <<<"$rows" | sort
  echo
  echo "Counters are cAdvisor container CPU summed over every container of every pod of the component (sidecars included; replicas included)."
  echo

  # ---- pprof tables
  echo "## Top 20 per service (CPU profiles merged across pods)"
  echo
  if ! command -v "$GO" >/dev/null 2>&1; then
    echo "**SKIPPED: \`go\` is not on PATH, so \`go tool pprof\` could not run.**"
  else
    local comp files
    while read -r comp; do
      [[ -n "$comp" ]] || continue
      mapfile -t files < <("$JQ" -r --arg c "$comp" '.artifacts[] | select(.component == $c and (.kind | test("^cpu[0-9]+$")) and .invalid != true) | .file' "$man")
      [[ ${#files[@]} -gt 0 ]] || continue
      local paths=() f; for f in "${files[@]}"; do paths+=("./$f"); done
      echo "### $comp (${#files[@]} CPU profile(s))"
      echo
      echo '```'
      pprof_in "$d" -top -nodecount=20 "${paths[@]}" 2>&1 | sed -n '1,32p'
      echo '```'
      echo
      echo "cumulative:"
      echo
      echo '```'
      pprof_in "$d" -top -cum -nodecount=20 "${paths[@]}" 2>&1 | sed -n '5,30p'
      echo '```'
      echo
      mapfile -t files < <("$JQ" -r --arg c "$comp" '.artifacts[] | select(.component == $c and .kind == "allocs" and .invalid != true) | .file' "$man")
      if [[ ${#files[@]} -gt 0 ]]; then
        paths=(); for f in "${files[@]}"; do paths+=("./$f"); done
        echo "allocations (alloc_space, since process start):"
        echo
        echo '```'
        pprof_in "$d" -top -nodecount=10 -sample_index=alloc_space "${paths[@]}" 2>&1 | sed -n '1,20p'
        echo '```'
        echo
      fi
    done < <("$JQ" -r '[.artifacts[] | select(.kind | test("^cpu[0-9]+$")) | .component] | unique[]' "$man")
  fi

  # ---- NATS
  echo "## NATS"
  echo
  local any=0 pod pre_v post_v
  echo "| pod | RSS (varz.mem) MiB pre -> post | JetStream memory MiB | JetStream store MiB | in msgs/s | out msgs/s | slow consumers |"
  echo "|---|---|---:|---:|---:|---:|---:|"
  for pre_v in "$d/$pre"-nats-*-varz-pre.json; do
    [[ -e "$pre_v" ]] || continue
    pod="${pre_v##*"$pre-nats-"}"; pod="${pod%-varz-pre.json}"
    post_v="$d/$pre-nats-$pod-varz-post.json"
    [[ -s "$post_v" ]] || { echo "| $pod | varz-post MISSING | | | | | |"; continue; }
    any=1
    "$JQ" -rn --arg pod "$pod" --slurpfile a "$pre_v" --slurpfile b "$post_v" \
      --slurpfile ja "$d/$pre-nats-$pod-jsz-pre.json" --slurpfile jb "$d/$pre-nats-$pod-jsz-post.json" '
      ($a[0]) as $a | ($b[0]) as $b | ($jb[0]) as $jb
      | (($b.now | sub("\\.[0-9]+";"") | fromdateiso8601) - ($a.now | sub("\\.[0-9]+";"") | fromdateiso8601)) as $w
      | "| \($pod) | \(($a.mem/1048576)|floor) -> \(($b.mem/1048576)|floor) | \((($jb.memory // 0)/1048576)|floor) | \((($jb.store // 0)/1048576)|floor) | \((($b.in_msgs-$a.in_msgs)/$w)|floor) | \((($b.out_msgs-$a.out_msgs)/$w)|floor) | \($b.slow_consumers) |"' 2>/dev/null \
      || echo "| $pod | unreadable varz/jsz | | | | | |"
  done
  [[ "$any" -eq 1 ]] || echo "| (no NATS snapshots in this phase) | | | | | | |"
  echo
  local jb
  for jb in "$d/$pre"-nats-*-jsz-post.json; do
    [[ -e "$jb" ]] || continue
    echo "Heaviest streams on \`${jb##*/}\`:"
    echo
    "$JQ" -r '[.account_details[]?.stream_detail[]? | {name, storage: .config.storage, replicas: .config.num_replicas, bytes: .state.bytes, msgs: .state.messages, consumers: .state.consumer_count}] | sort_by(-.bytes) | .[:8][] | "- \(.name): \((.bytes/1048576)|floor) MiB (\(.storage), R\(.replicas)), \(.msgs) msgs, \(.consumers) consumers"' "$jb" 2>/dev/null || echo "- (no stream detail)"
    echo
    break   # one server is enough for the stream list; the per-pod table above has the rest
  done

  # ---- databases
  echo "## Databases"
  echo
  local pgd sysd
  for pgd in "$d/$pre"-postgres-*-pgss-delta.json "$d/$pre"-timescaledb-*-pgss-delta.json; do
    [[ -e "$pgd" ]] || continue
    echo "### ${pgd##*/} -- top 20 statements by total time"
    echo
    if [[ "$("$JQ" -r .valid "$pgd")" != true ]]; then echo "**INVALID: $("$JQ" -r .reason "$pgd")**"; echo; continue; fi
    echo "| total ms | calls | mean ms | rows | db | query |"
    echo "|---:|---:|---:|---:|---|---|"
    "$JQ" -r '.statements[:20][] | "| \(.total_exec_time|floor) | \(.calls) | \((.total_exec_time/.calls*100|floor)/100) | \(.rows) | \(.db // "-") | `\(.query | gsub("[\\n|`]";" ") | .[:90])` |"' "$pgd"
    echo
  done
  for sysd in "$d/$pre"-postgres-*-sysstat-delta.json "$d/$pre"-timescaledb-*-sysstat-delta.json; do
    [[ -e "$sysd" ]] || continue
    echo "### ${sysd##*/}"
    echo
    if [[ "$("$JQ" -r .valid "$sysd")" != true ]]; then echo "**INVALID: $("$JQ" -r .reason "$sysd")**"; echo; continue; fi
    echo '```'
    "$JQ" -r --argjson ev "$events" '
      "wal_bytes: \(.wal.wal_bytes // "n/a")  (\(if $ev > 0 and .wal.wal_bytes then ((.wal.wal_bytes / ($ev/1000))|floor) else "n/a" end) bytes per 1,000 events)",
      "wal_fpi: \(.wal.wal_fpi // "n/a")  wal_records: \(.wal.wal_records // "n/a")",
      "checkpoints timed/requested: \((.checkpointer.num_timed // .bgwriter.checkpoints_timed) // "n/a") / \((.checkpointer.num_requested // .bgwriter.checkpoints_req) // "n/a")",
      "buffers written by checkpointer: \((.checkpointer.buffers_written // .bgwriter.buffers_checkpoint) // "n/a")  bgwriter buffers_clean: \(.bgwriter.buffers_clean // "n/a")"' "$sysd"
    echo '```'
    echo
  done
}

self_test() {
  command -v "$jq_real" >/dev/null 2>&1 || { say "self-test needs jq"; return 1; }
  local d rc=0 rep
  d="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$d'" RETURN
  local p="rT-R1"
  # manifest: two service pods + a tsdb primary; one invalid pod
  cat >"$d/$p-manifest.json" <<'JSON'
{"namespace":"ns1","startedAt":"a","finishedAt":"b","complete":true,"steady":{"checked":true},
 "pods":[{"pod":"dm-1","component":"device-management"},{"pod":"dm-2","component":"device-management"},{"pod":"ds-1","component":"device-state"},{"pod":"gone-1","component":"event-sources"}],
 "artifacts":[{"component":"event-sources","pod":"gone-1","kind":"heap","file":"x","invalid":true}]}
JSON
  # cadvisor: dm-1 uses 30 CPU-s over 60 s, dm-2 uses 12, ds-1 uses 6, gone-1 counter goes backwards
  {
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="dm-1",container="svc"} 100 1000000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="dm-2",container="svc"} 50 1000000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="ds-1",container="svc"} 10 1000000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="ds-1",container="POD"} 99 1000000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="gone-1",container="svc"} 500 1000000'
  } >"$d/$p-node-n1-cadvisor-pre.prom"
  {
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="dm-1",container="svc"} 130 1060000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="dm-2",container="svc"} 62 1060000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="ds-1",container="svc"} 16 1060000'
    echo 'container_cpu_usage_seconds_total{namespace="ns1",pod="gone-1",container="svc"} 3 1060000'
  } >"$d/$p-node-n1-cadvisor-post.prom"
  echo '{"valid":true,"from":"2026-11-02T10:00:00.123456+00:00","to":"2026-11-02T10:01:00.654321+00:00","hypertables":{"events":60000,"measurements":1000}}' >"$d/$p-timescaledb-t-1-sysstat-delta.json"

  rep="$("$SELF" --dir "$d" --round rT --phase R1 --events-hypertable events 2>&1)" || { echo "self-test: analysis failed: $rep" >&2; return 1; }
  # device-management: 42 CPU-s over 60 s = 0.70 cores; 60,000 events in 60 s = 1000/s => 0.70/1000*1000 = 0.700 per 1k
  grep -q '| device-management | 2 | 42.0 | 0.70 | 0.700 |' <<<"$rep" || { echo "self-test: wrong CPU-s per 1k for device-management" >&2; echo "$rep" >&2; rc=1; }
  # device-state: 6 CPU-s, the POD pause container excluded => 0.10 cores => 0.100 per 1k
  grep -q '| device-state | 1 | 6.0 | 0.10 | 0.100 |' <<<"$rep" || { echo "self-test: the pause container was counted, or the arithmetic is wrong" >&2; echo "$rep" >&2; rc=1; }
  # the pod whose counter went backwards / whose artifacts are invalid is left out and named
  grep -q 'gone-1' <<<"$rep" && ! grep -q '| event-sources |' <<<"$rep" || { echo "self-test: an invalid pod was tabulated or not named" >&2; echo "$rep" >&2; rc=1; }
  # the denominator is not guessed silently
  rep="$("$SELF" --dir "$d" --round rT --phase R1 2>&1)" || true
  grep -q 'ASSUMED' <<<"$rep" || { echo "self-test: an unpinned hypertable was not flagged ASSUMED" >&2; rc=1; }
  # an incomplete collection is refused
  jq '.complete = false | .failures = [{"component":"x"}]' "$d/$p-manifest.json" >"$d/m2" && mv "$d/m2" "$d/$p-manifest.json"
  if "$SELF" --dir "$d" --round rT --phase R1 --events 1 >/dev/null 2>&1; then echo "self-test: an incomplete collection was analysed" >&2; rc=1; fi
  [[ "$rc" -eq 0 ]] && echo "self-test: ok"
  return "$rc"
}

if [[ "$self" -eq 1 ]]; then self_test; exit $?; fi
[[ -n "$dir" && -n "$round" && -n "$phase" ]] || usage
[[ -d "$dir" ]] || { say "$dir is not a directory"; exit 2; }
dir="$(cd "$dir" && pwd)"
if [[ -n "$outfile" ]]; then analyze "$dir" "$round" "$phase" >"$outfile" && say "wrote $outfile"; else analyze "$dir" "$round" "$phase"; fi
