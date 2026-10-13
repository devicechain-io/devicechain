#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Start (or stop) the two background processes a guide capture needs on the dev box:
#
#   1. kubectl port-forward svc/event-sources 8081:8081   (the ingress does not expose
#      the ingest port the simulator posts to)
#   2. the dc-simulator actor, which actually POPULATES the sim tenant (`dcctl sim create`
#      only mints the tenant and identity: devices, assignments, rules and dashboards come
#      from this process)
#
# Both die with the shell that started them, so this runs them detached with pid files.
# Idempotent: a process that is already up (by pid file, or by its port) is left alone.
#
#   hack/guide-capture/bringup.sh up        # default
#   hack/guide-capture/bringup.sh status
#   hack/guide-capture/bringup.sh down
#
# Environment (defaults match `dcctl bootstrap local guide`):
#   GUIDE_INSTANCE=guide  GUIDE_KUBE_CONTEXT=kind-devicechain
#   GUIDE_SIMS="acme:8090 widgets:8091"   name:control-port, one actor per sim. The hero sim
#       (buildingpulse) is required; any other sim whose handshake does not exist is skipped
#       with a note (the dashboards chapter needs widgets: `dcctl sim create widgets
#       --manifest widgetlab --control-addr http://localhost:8091 -i guide`).
#
# Git Bash on Windows is the supported shell; it works unchanged on Linux/macOS.
# Never prints the sim handshake: that file holds the sim identity's password.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
run="$here/.run"
instance="${GUIDE_INSTANCE:-guide}"
kctx="${GUIDE_KUBE_CONTEXT:-kind-devicechain}"
sims="${GUIDE_SIMS:-acme:8090 widgets:8091}"

case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) win=1 ;; *) win=0 ;; esac
exe=""
[ "$win" = 1 ] && exe=".exe"

mkdir -p "$run"

# A pid file is trusted only while its process is alive: a stale one (reboot, crash)
# must not make `up` believe the process is running.
alive() {
  local pid="$1"
  [ -n "$pid" ] || return 1
  if [ "$win" = 1 ]; then
    grep -q "$pid" <<<"$(tasklist //FI "PID eq $pid" //NH 2>/dev/null)"
  else
    kill -0 "$pid" 2>/dev/null
  fi
}

# The pid we record is the OS pid (on Windows the WINPID, which taskkill needs), because
# under MSYS the shell's own $! is a different number.
os_pid() {
  if [ "$win" = 1 ]; then ps -W -p "$1" 2>/dev/null | awk 'NR==2 {print $4}'; else echo "$1"; fi
}

# The image name behind a pid, lowercased and without .exe. A pid file outlives its process,
# and the OS reuses pids, so a pid alone is not proof it is still OURS.
image_of() {
  local pid="$1" img=""
  if [ "$win" = 1 ]; then
    img="$(tasklist //FI "PID eq $pid" //FO CSV //NH 2>/dev/null | sed -n '1p' | cut -d, -f1 | tr -d '"')"
  else
    img="$(ps -o comm= -p "$pid" 2>/dev/null || true)"
  fi
  img="${img##*/}"
  img="$(printf '%s' "$img" | tr '[:upper:]' '[:lower:]')"
  echo "${img%.exe}"
}

# ours <name> <expected-image>: alive AND still the image we started.
ours() {
  local name="$1" want="$2" pid=""
  [ -f "$run/$name.pid" ] && pid="$(cat "$run/$name.pid")"
  alive "$pid" && [ "$(image_of "$pid")" = "$want" ]
}

stop_pid() {
  local pid="$1"
  if [ "$win" = 1 ]; then taskkill //PID "$pid" //T //F >/dev/null 2>&1 || true; else kill "$pid" 2>/dev/null || true; fi
}

port_open() { # host:port
  (exec 3<>"/dev/tcp/${1%:*}/${1#*:}") 2>/dev/null
}

start_bg() { # name, then the command
  local name="$1"
  shift
  nohup "$@" >"$run/$name.log" 2>&1 &
  local sh_pid=$!
  sleep 1
  local pid
  pid="$(os_pid "$sh_pid")"
  echo "$pid" >"$run/$name.pid"
  echo "$name: started (pid $pid, log $run/$name.log)"
}

up() {
  # The hero sim is mandatory; the others are skipped when their handshake is absent.
  local hero="${sims%%:*}"
  [ -f "$HOME/.devicechain/sims/$hero.json" ] || {
    echo "no sim handshake for $hero: run 'dcctl sim create $hero --manifest buildingpulse --seed 42 -i $instance' first" >&2
    exit 1
  }

  # 1. port-forward
  if ours port-forward kubectl; then
    echo "port-forward: ours (pid $(cat "$run/port-forward.pid")) and running"
  elif port_open localhost:8081; then
    if [ "${GUIDE_ADOPT_EXISTING:-}" = 1 ]; then
      echo "port-forward: localhost:8081 is held by something this script did not start; adopting it (GUIDE_ADOPT_EXISTING=1)"
    else
      echo "localhost:8081 is already listening, but not from a process this script started. Stop it, or set GUIDE_ADOPT_EXISTING=1 to use it as is." >&2
      exit 1
    fi
  else
    start_bg port-forward kubectl --context "$kctx" -n "dci-$instance" port-forward svc/event-sources 8081:8081
    for _ in 1 2 3 4 5 6 7 8 9 10; do port_open localhost:8081 && break; sleep 1; done
    port_open localhost:8081 || {
      echo "port-forward did not open localhost:8081; see $run/port-forward.log" >&2
      exit 1
    }
  fi

  # 2. one simulator actor per sim, each on its own control port
  local built=0 entry name control
  for entry in $sims; do
    name="${entry%%:*}"
    control="localhost:${entry##*:}"
    if [ ! -f "$HOME/.devicechain/sims/$name.json" ]; then
      echo "dc-simulator[$name]: no handshake, skipped (create it with dcctl sim create)"
      continue
    fi
    if ours "dc-simulator-$name" dc-simulator; then
      echo "dc-simulator[$name]: ours (pid $(cat "$run/dc-simulator-$name.pid")) and running"
    elif port_open "$control"; then
      if [ "${GUIDE_ADOPT_EXISTING:-}" = 1 ]; then
        echo "dc-simulator[$name]: $control is answering for a process this script did not start; adopting it (GUIDE_ADOPT_EXISTING=1)"
      else
        echo "$control is already answering, but not from a process this script started. Stop it, or set GUIDE_ADOPT_EXISTING=1 to use it as is." >&2
        exit 1
      fi
    else
      if [ "$built" = 0 ]; then
        (cd "$root/backend/sims/dc-simulator" && go build -o "$run/dc-simulator$exe" .)
        built=1
      fi
      start_bg "dc-simulator-$name" "$run/dc-simulator$exe" --handshake "$HOME/.devicechain/sims/$name.json" --port "${entry##*:}"
      for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do port_open "$control" && break; sleep 1; done
      port_open "$control" || {
        echo "dc-simulator[$name] did not come up on $control; see $run/dc-simulator-$name.log" >&2
        exit 1
      }
    fi
  done
  echo "bring-up complete"
}

status() {
  ours port-forward kubectl && echo "port-forward: ours, running (pid $(cat "$run/port-forward.pid"))" || echo "port-forward: not started by this script"
  port_open localhost:8081 && echo "localhost:8081: open" || echo "localhost:8081: closed"
  local entry name
  for entry in $sims; do
    name="${entry%%:*}"
    ours "dc-simulator-$name" dc-simulator && echo "dc-simulator[$name]: ours, running (pid $(cat "$run/dc-simulator-$name.pid"))" || echo "dc-simulator[$name]: not started by this script"
    port_open "localhost:${entry##*:}" && echo "localhost:${entry##*:}: open" || echo "localhost:${entry##*:}: closed"
  done
}

down() {
  local entry pairs=""
  for entry in $sims; do pairs="$pairs dc-simulator-${entry%%:*}:dc-simulator"; done
  for pair in $pairs dc-simulator:dc-simulator port-forward:kubectl; do
    local name="${pair%%:*}" want="${pair##*:}"
    if [ -f "$run/$name.pid" ]; then
      local pid
      pid="$(cat "$run/$name.pid")"
      if ! alive "$pid"; then
        echo "$name: pid file was stale"
      elif [ "$(image_of "$pid")" != "$want" ]; then
        echo "$name: pid $pid is now a different process ($(image_of "$pid")), not ours; NOT killing it"
      else
        stop_pid "$pid"
        echo "$name: stopped (pid $pid)"
      fi
      rm -f "$run/$name.pid"
    else
      echo "$name: no pid file (not started by this script; left alone)"
    fi
  done
}

case "${1:-up}" in
  up) up ;;
  status) status ;;
  down) down ;;
  *) echo "usage: $0 [up|status|down]" >&2; exit 2 ;;
esac
