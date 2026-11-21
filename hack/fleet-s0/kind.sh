#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# The Kubernetes the E-l premise is bound to. deploy/fleet_premises_test.go reads this line, so moving
# the node image turns the recorded E-l result STALE until it is measured again.
S0_KIND_NODE_IMAGE=kindest/node:v1.37.0
kind_cleanup() {
  if [[ -n ${S0_KIND_NAME:-} ]]; then kind delete cluster --name="$S0_KIND_NAME"; fi
}
probe_l() {
  local uid container node control_ip control_ip6 before after event deadline name
  local cluster="fleet-s0-$$-$RANDOM"
  cat >"$S0_OUT/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
YAML
  kind create cluster --name="$cluster" --image="$S0_KIND_NODE_IMAGE" --config="$S0_OUT/kind.yaml" --wait=180s
  # Cleanup may delete only what this run created: ownership starts here, after a successful create.
  S0_KIND_NAME="$cluster"
  # From a container, reach the cluster over the kind network (created by kind itself).
  [[ -z ${FLEET_S0_KIND_INTERNAL:-} ]] || docker network connect kind "$(hostname)"
  export KUBECONFIG="$S0_OUT/kind.kubeconfig"
  kind get kubeconfig ${FLEET_S0_KIND_INTERNAL:+--internal} --name="$S0_KIND_NAME" >"$KUBECONFIG"
  node="$S0_KIND_NAME-worker"
  cat >"$S0_OUT/runner.yaml" <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: s0-runner
  finalizers: [fleet-s0.devicechain.io/observe]
spec:
  nodeName: $node
  restartPolicy: Never
  containers:
    - name: runner
      image: busybox:1.37.0
      command: [sh, -c, 'n=0; while :; do n=\$((n+1)); echo \$n > /tmp/heartbeat; sleep 1; done']
YAML
  kubectl apply -f "$S0_OUT/runner.yaml"
  kubectl wait pod/s0-runner --for=condition=Ready --timeout=180s
  S0_KUBERNETES_VERSION="$(kubectl version -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"].removeprefix("v"))')"
  kubectl get pod s0-runner -o json >"$S0_OUT/pod-initial.json"
  uid="$(python3 - "$S0_OUT/pod-initial.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p["metadata"]["uid"]
assert p["metadata"]["finalizers"]==["fleet-s0.devicechain.io/observe"]
print(p["metadata"]["uid"])
PY
)"
  container="$(docker exec "$node" crictl ps -o json | python3 -c 'import json,sys; p=[x["id"] for x in json.load(sys.stdin)["containers"] if x.get("labels",{}).get("io.kubernetes.pod.uid")==sys.argv[1]]; assert len(p)==1; print(p[0])' "$uid")"
  heartbeat() { docker exec "$node" crictl exec "$container" cat /tmp/heartbeat; }
  snapshot() {
    local stage="$1" rc=0
    before="$(heartbeat)"
    sleep "${FLEET_S0_HEARTBEAT_SECONDS:-2}"
    after="$(heartbeat)"
    [[ $before =~ ^[0-9]+$ && $after =~ ^[0-9]+$ ]] && (( after > before )) || die "runner process not provably advancing"
    kubectl get pod s0-runner -o json >"$S0_OUT/$stage.json" 2>"$S0_OUT/get-error" || rc=$?
    if (( rc != 0 )); then
      grep -q NotFound "$S0_OUT/get-error" || die "pod observation failed for unrelated reason"
      printf '{"absent":true,"recorded_uid":"%s"}\n' "$uid" >"$S0_OUT/$stage.json"
    fi
    python3 - "$S0_OUT/$stage.json" "$uid" "$before" "$after" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
if not p.get("absent"):
    assert p["metadata"]["uid"]==sys.argv[2], "pod UID changed"
    statuses=p.get("status",{}).get("containerStatuses",[])
    if statuses and all("terminated" in x.get("state",{}) for x in statuses):
        print("E-l FAILS: all containers reported terminated while process advances",file=sys.stderr)
        sys.exit(1)
p["process_heartbeat"]=[int(sys.argv[3]),int(sys.argv[4])]
with open(sys.argv[1],"w") as f: json.dump(p,f)
PY
  }
  snapshot before-partition
  control_ip="$(docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$S0_KIND_NAME-control-plane")"
  control_ip6="$(docker inspect --format='{{range .NetworkSettings.Networks}}{{.GlobalIPv6Address}}{{end}}' "$S0_KIND_NAME-control-plane")"
  [[ $control_ip =~ ^[0-9.]+$ ]] || die "missing control-plane IP"
  # Keep the worker and its process alive; cut every packet in BOTH directions between the node and the
  # control plane, on both address families. Dropping only the node's egress is not a partition: the
  # API server's watch events still arrive on the established connection (measured: the kubelet then
  # learned of the node delete and stopped the container, so the run is void).
  docker exec "$node" iptables -I OUTPUT -d "$control_ip" -j DROP
  docker exec "$node" iptables -I INPUT -s "$control_ip" -j DROP
  if [[ -n $control_ip6 ]]; then
    docker exec "$node" ip6tables -I OUTPUT -d "$control_ip6" -j DROP
    docker exec "$node" ip6tables -I INPUT -s "$control_ip6" -j DROP
  fi
  sleep "${FLEET_S0_PARTITION_SETTLE_SECONDS:-5}"
  kubectl delete node "$node"
  snapshot node-deleted
  deadline=$((SECONDS + ${FLEET_S0_PODGC_TIMEOUT_SECONDS:-300}))
  while :; do
    event="$(kubectl get pod s0-runner -o json 2>"$S0_OUT/podgc-error")" || {
      grep -q NotFound "$S0_OUT/podgc-error" || die "PodGC observation failed"
      event='{"absent":true}'
    }
    if python3 -c 'import json,sys; p=json.loads(sys.argv[1]); sys.exit(0 if p.get("absent") or p.get("metadata",{}).get("deletionTimestamp") else 1)' "$event"; then break; fi
    (( SECONDS < deadline )) || die "INCONCLUSIVE: PodGC was not observed within the bounded wait"
    sleep "${FLEET_S0_POLL_SECONDS:-2}"
  done
  snapshot podgc
  # A held finalizer may retain the object even for --force; record that instead of inventing absence.
  kubectl delete pod s0-runner --force --grace-period=0 --wait=false --ignore-not-found
  snapshot force-deleted
  result E-l HOLDS '["kubernetes"]' "$(python3 - "$S0_OUT" "$uid" <<'PY'
import json,pathlib,sys
d=pathlib.Path(sys.argv[1])
def summary(p):
    if p.get("absent"):
        return dict(object_absent=True, process_heartbeat=p["process_heartbeat"])
    st=p.get("status",{})
    return dict(object_absent=False, uid=p["metadata"]["uid"],
        deletionTimestamp=p["metadata"].get("deletionTimestamp"), finalizers=p["metadata"].get("finalizers"),
        phase=st.get("phase"),
        conditions=[c["type"]+"="+c["status"] for c in st.get("conditions",[])],
        containerStatuses=[list(v["state"].keys())[0] for v in st.get("containerStatuses",[])],
        process_heartbeat=p["process_heartbeat"])
obs={n: summary(json.loads((d/(n+".json")).read_text())) for n in
     ["before-partition","node-deleted","podgc","force-deleted"]}
forged=sorted(n for n,o in obs.items() if o.get("phase") in ("Failed","Succeeded"))
print(json.dumps(dict(pod_uid=sys.argv[2], observations=obs,
    never_all_containers_terminated=True,
    phase_forged_terminal_while_process_runs=forged,
    consequence="status.phase and deletionTimestamp are forgeable by PodGC; only containerStatuses terminated for the recorded UID is usable, and it never appeared")))
PY
)"
}
