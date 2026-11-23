#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ ${1:-} == --self-test ]]; then exec bash "$here/self-test.sh" run; fi
source "$here/lib.sh"
source "$here/probes.sh"
source "$here/local.sh"
source "$here/kind.sh"
source "$here/instrument.sh"
unset S0_PREFLIGHT S0_KIND_NAME S0_PG_DB S0_PG_OWNER S0_PG_RUNNER S0_PG_ROLES_OWNED S0_WRITER_PID
S0_BACKEND=pg
S0_REAPER_ONLY=false
ids=()
for arg in "$@"; do
  case "$arg" in
    --backend=pg) S0_BACKEND=pg ;;
    --backend=gcs) S0_BACKEND=gcs ;;
    --reaper-only) S0_REAPER_ONLY=true ;;
    E-a|E-b|E-c|E-d|E-e|E-f|E-f2|E-g|E-h|E-i|E-j|E-k|E-l) ids+=("$arg") ;;
    *) die "usage: run.sh [--backend=pg|gcs] E-a ... E-l" ;;
  esac
done
(( ${#ids[@]} > 0 )) || [[ $S0_REAPER_ONLY == true ]] || die "choose at least one experiment"
umask 077
export TF_CLI_ARGS=-no-color
exec 3>&1 4>&2
S0_OUT="${FLEET_S0_OUTPUT_DIR:-$(mktemp -d)}"
mkdir -p "$S0_OUT"
[[ ! -e $S0_OUT/transcript.log ]] || die "output directory already contains a run"
S0_OUT="$(cd "$S0_OUT" && pwd)"
finish() {
  local rc=$? cleanup_rc=0
  trap - EXIT
  set +e
  # die inside main exits with its redirect still live; restore the console first.
  exec 1>&3 2>&4
  [[ ! -d $S0_OUT/pg ]] || touch "$S0_OUT/pg/release"
  [[ ! -d $S0_OUT/gcs ]] || touch "$S0_OUT/gcs/release"
  if [[ -n ${S0_WRITER_PID:-} ]]; then kill "$S0_WRITER_PID" 2>/dev/null; wait "$S0_WRITER_PID"; fi
  if [[ -n ${S0_DISABLED_IDENTITY:-} ]]; then
    gcloud iam service-accounts enable "$S0_DISABLED_IDENTITY" --project="$FLEET_S0_PROJECT" --quiet >>"$S0_OUT/transcript.log" 2>&1 || cleanup_rc=$?
  fi
  if [[ ${S0_RESTORE_UPDATE_ROLE:-} == true ]]; then
    gcloud projects add-iam-policy-binding "$FLEET_S0_PROJECT" --member="serviceAccount:$FLEET_S0_UPDATE_ONLY_IDENTITY"       --role="projects/$FLEET_S0_PROJECT/roles/$FLEET_S0_UPDATE_ONLY_ROLE" --condition=None --quiet >>"$S0_OUT/transcript.log" 2>&1 || cleanup_rc=$?
  fi
  pg_cleanup >>"$S0_OUT/transcript.log" 2>&1 || cleanup_rc=$?
  kind_cleanup >>"$S0_OUT/transcript.log" 2>&1 || cleanup_rc=$?
  (( cleanup_rc == 0 || rc != 0 )) || rc=$cleanup_rc
  if [[ -s $S0_OUT/results.jsonl ]]; then
    python3 - "$S0_OUT" "$S0_TOFU_VERSION" "$FLEET_S0_PROVIDER_VERSION" <<'PY'
import datetime,hashlib,json,pathlib,sys
d=pathlib.Path(sys.argv[1])
digest=hashlib.sha256((d/"transcript.log").read_bytes()).hexdigest()
premises=[]
for line in (d/"results.jsonl").read_text().splitlines():
    p=json.loads(line)
    p.update(tofu_version=sys.argv[2],provider_version=sys.argv[3],
        date=datetime.datetime.now(datetime.timezone.utc).date().isoformat(),transcript_sha256=digest)
    premises.append(p)
(d/"gke.json").write_text(json.dumps(dict(schema_version=1,experiments=premises),indent=2)+"\n")
PY
    (( $? == 0 )) || rc=2
  fi
  cat "$S0_OUT/transcript.log"
  echo "private evidence directory: $S0_OUT"
  exit "$rc"
}
trap finish EXIT
main() {
  local needs_cloud=false id
  for id in "${ids[@]}"; do
    case "$id" in
      E-j|E-l) ;;
      E-g) [[ $S0_BACKEND != gcs ]] || needs_cloud=true ;;
      *) needs_cloud=true ;;
    esac
  done
  if [[ $needs_cloud == true || $S0_REAPER_ONLY == true ]]; then reaper_preflight; fi
  [[ $S0_REAPER_ONLY != true ]] || return 0
  required FLEET_S0_PROVIDER_VERSION
  [[ $FLEET_S0_PROVIDER_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "provider version must be an exact pin"
  S0_TOFU_VERSION="$(tofu version -json | python3 -c 'import json,sys; print(json.load(sys.stdin)["terraform_version"])')"
  [[ $S0_TOFU_VERSION == 1.10.10 ]] || die "S0 requires tofu 1.10.10"
  for id in "${ids[@]}"; do experiment "$id"; done
}
main >"$S0_OUT/transcript.log" 2>&1
