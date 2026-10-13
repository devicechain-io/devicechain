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
#   GUIDE_INSTANCE=guide  GUIDE_SIM=guide  GUIDE_KUBE_CONTEXT=kind-devicechain
#   GUIDE_CONTROL_ADDR=localhost:8090
#
# Git Bash on Windows is the supported shell; it works unchanged on Linux/macOS.
# Never prints the sim handshake: that file holds the sim identity's password.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
run="$here/.run"
instance="${GUIDE_INSTANCE:-guide}"
sim="${GUIDE_SIM:-guide}"
kctx="${GUIDE_KUBE_CONTEXT:-kind-devicechain}"
control="${GUIDE_CONTROL_ADDR:-localhost:8090}"
handshake="$HOME/.devicechain/sims/$sim.json"

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
    tasklist //FI "PID eq $pid" //NH 2>/dev/null | grep -q "$pid"
  else
    kill -0 "$pid" 2>/dev/null
  fi
}

# The pid we record is the OS pid (on Windows the WINPID, which taskkill needs), because
# under MSYS the shell's own $! is a different number.
os_pid() {
  if [ "$win" = 1 ]; then ps -W -p "$1" 2>/dev/null | awk 'NR==2 {print $4}'; else echo "$1"; fi
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
  [ -f "$handshake" ] || {
    echo "no sim handshake at $handshake: run 'dcctl sim create $sim --manifest buildingpulse --seed 42 -i $instance' first" >&2
    exit 1
  }

  # 1. port-forward
  if port_open localhost:8081; then
    echo "port-forward: localhost:8081 already open, leaving it"
  else
    start_bg port-forward kubectl --context "$kctx" -n "dci-$instance" port-forward svc/event-sources 8081:8081
    for _ in 1 2 3 4 5 6 7 8 9 10; do port_open localhost:8081 && break; sleep 1; done
    port_open localhost:8081 || {
      echo "port-forward did not open localhost:8081; see $run/port-forward.log" >&2
      exit 1
    }
  fi

  # 2. the simulator actor
  if port_open "$control"; then
    echo "dc-simulator: control API on $control already answering, leaving it"
  else
    (cd "$root/backend/sims/dc-simulator" && go build -o "$run/dc-simulator$exe" .)
    start_bg dc-simulator "$run/dc-simulator$exe" --handshake "$handshake"
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do port_open "$control" && break; sleep 1; done
    port_open "$control" || {
      echo "dc-simulator did not come up on $control; see $run/dc-simulator.log" >&2
      exit 1
    }
  fi
  echo "bring-up complete"
}

status() {
  for name in port-forward dc-simulator; do
    local pid=""
    [ -f "$run/$name.pid" ] && pid="$(cat "$run/$name.pid")"
    if alive "$pid"; then echo "$name: running (pid $pid)"; else echo "$name: not started by this script"; fi
  done
  port_open localhost:8081 && echo "localhost:8081: open" || echo "localhost:8081: closed"
  port_open "$control" && echo "$control: open" || echo "$control: closed"
}

down() {
  for name in dc-simulator port-forward; do
    if [ -f "$run/$name.pid" ]; then
      local pid
      pid="$(cat "$run/$name.pid")"
      if alive "$pid"; then
        stop_pid "$pid"
        echo "$name: stopped (pid $pid)"
      else
        echo "$name: pid file was stale"
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
