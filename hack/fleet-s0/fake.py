#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Strict scripted CLI for S0 safety controls; unexpected commands fail closed."""
import json
import os
import pathlib
import sys

tool, *args = sys.argv[1:]
case = os.environ.get("S0_FAKE_CASE", "good")
def portable(value):
    if os.name == "nt" and value.startswith("/"):
        import subprocess
        value = subprocess.check_output(["cygpath", "-w", value], text=True).strip()
    return pathlib.Path(value)

directory = portable(os.environ["S0_FAKE_DIR"])
with (directory / "calls").open("a") as log:
    log.write(tool + " " + " ".join(args) + "\n")

def emit(value):
    print(value if isinstance(value, str) else json.dumps(value))
    sys.exit(0)


def mark(name, value="1"):
    (directory / name).write_text(value)

def exists(name):
    return (directory / name).exists()

def denied():
    print("PERMISSION_DENIED", file=sys.stderr)
    sys.exit(1)

if tool == "unshare":
    args = args[args.index("tofu")+1:]
    tool = "tofu"

if tool == "gcloud":
    if args[:2] == ["auth", "list"]:
        emit("operator@example.invalid")
    if args[:2] == ["auth", "print-access-token"]:
        if exists("disabled"):
            print("PERMISSION_DENIED: identity disabled", file=sys.stderr)
            sys.exit(1)
        emit("fake-token")
    if args[:2] == ["projects", "describe"]:
        if "value(lifecycleState)" in " ".join(args):
            emit("ACTIVE" if case == "canary-timeout" else "DELETE_REQUESTED")
        if "value(labels.fleet-s0)" in " ".join(args):
            emit("production" if case == "wrong-project" else "throwaway")
        emit("123456789")
    if args[:2] == ["resource-manager", "liens"]:
        emit("lien-1" if case == "lien" else "")
    if args[:3] == ["iam", "roles", "create"]:
        mark("canary-role")
        emit("")
    if args[:3] == ["iam", "roles", "describe"]:
        if "--project=s0-canary" in args and not exists("canary-role"):
            print("NOT_FOUND: custom role",file=sys.stderr)
            sys.exit(1)
        if "updateOnly" in args:
            emit({"includedPermissions":["container.clusters.update","container.clusters.get","container.operations.get"]})
        cluster = "fleetS0ClusterDelete" in args
        emit({"includedPermissions": ["owner"] if case == "wrong-role" else
              ["container.clusters.delete"] if cluster else ["resourcemanager.projects.delete"]})
    if args[:3] == ["scheduler", "jobs", "create"]:
        if case == "scheduler-error":
            sys.exit(1)
        emit("")
    if args[:2] in (["projects", "create"], ["projects", "add-iam-policy-binding"]):
        emit("")
    if args[:2] == ["projects", "remove-iam-policy-binding"]:
        mark("role-removed")
        emit("")
    if args[:3] == ["iam", "policies", "create"]:
        mark("deny")
        emit("")
    if args[:3] == ["iam", "policies", "delete"]:
        (directory / "deny").unlink(missing_ok=True)
        emit("")
    if args[:3] == ["iam", "service-accounts", "disable"]:
        mark("disabled")
        emit("")
    if args[:3] == ["iam", "service-accounts", "enable"]:
        (directory / "disabled").unlink(missing_ok=True)
        emit("")
    if args[:2] == ["container", "clusters"]:
        if args[2] in ("create","get-credentials"):
            emit("")
        if args[2] == "list":
            emit("s0-e-b" if case == "cluster-exists" else "")
        if args[2] == "describe":
            emit("RUNNING")
        if args[2] == "delete":
            if exists("deny") and case != "deny-ineffective":
                denied()
            emit("")
    if args[:3] == ["container", "node-pools", "create"]:
        emit("")
    if args[:3] == ["container", "node-pools", "delete"]:
        if exists("role-removed") and case != "role-ineffective":
            denied()
        emit("")
    if args[:3] == ["storage", "buckets", "create"]:
        emit("")
    if args[:2] == ["storage", "cat"]:
        if exists("lock") and case != "lock-missing":
            emit({"ID":"lock-id","Who":"test@runner","Operation":"OperationTypeApply","Created":"2026-11-21T00:00:00Z"})
        print("404 not found", file=sys.stderr)
        sys.exit(1)
    if args[:3] == ["compute", "disks", "list"]:
        emit([{"name":"s0-disk"}])
    if args[:3] == ["compute", "snapshots", "list"]:
        emit([{"name":"s0-snapshot"}])
if tool == "curl":
    if any("discovery/rest" in arg for arg in args):
        emit({"schemas":{"Cluster":{"properties":
             {"deletionProtection":{"type":"boolean"}} if case == "field-present" else
             {"name":{"type":"string"},"id":{"type":"string"}}}}})
    output = portable(args[args.index("--output")+1])
    if any("testIamPermissions" in arg for arg in args):
        output.write_text(json.dumps({"permissions":[] if exists("deny") and case != "deny-ineffective" else ["container.clusters.delete"]}))
        emit("200")
    if any("container.googleapis.com" in arg for arg in args):
        status = "503" if case == "token-server-error" else "403" if exists("disabled") else "200"
        output.write_text(json.dumps({"error":{"status":"PERMISSION_DENIED"}} if status=="403" else {}))
        emit(status)
if tool == "tofu":
    if args == ["version","-json"]:
        emit({"terraform_version":"1.10.10"})
    d = portable(args[0].split("=",1)[1])
    action = args[1]
    if d.name.startswith("encryption") and not os.environ.get("TF_ENCRYPTION") and action in ("plan","show","apply") and case != "encryption-ineffective":
        print("Error: encryption key provider missing\n  on encryption.tf line 2",file=sys.stderr)
        sys.exit(1)
    if d.name == "encryption" and action == "init" and "-backend=false" in args:
        # measured on tofu 1.10.10: an existing encrypted local state is read by init
        print("Error refreshing state: Unsupported state file format",file=sys.stderr)
        sys.exit(1)
    if d.name == "pg" and action == "plan" and exists("noinherit") and case != "noinherit-ineffective":
        print("pq: permission denied for schema s0",file=sys.stderr)
        sys.exit(1)
    if action in ("init","validate","test","plan","show","force-unlock"):
        if action == "plan":
            for arg in args:
                if arg.startswith("-out="): (d/arg.split("=",1)[1]).write_text("fake saved plan")
        if action == "force-unlock":
            (directory/"lock").unlink(missing_ok=True)
        emit("")
    if action == "apply":
        live = "-var=pause=true" in args or d.name == "gcs"
        if live:
            if d.name=="gcs": mark("lock")
            (d/"started").touch()
            import time
            while not (d/"release").exists(): time.sleep(0.01)
            if exists("revoked") and case != "cut-ineffective":
                print("Error: Failed to persist state to backend\nError: Error releasing the state lock\ncannot reconnect: role is not permitted to log in",file=sys.stderr)
                sys.exit(1)
            (d/"after").write_text("provider-called")
            mark("pg-state","changed")
            if d.name=="gcs": (directory/"lock").unlink(missing_ok=True)
        else:
            mark("pg-state","initial")
        emit("")
if tool == "psql":
    sql = args[args.index("-c")+1] if "-c" in args else sys.stdin.read()
    if "CREATE ROLE" in sql:
        (directory/"revoked").unlink(missing_ok=True)
        (directory/"sessions-dropped").unlink(missing_ok=True)
    if "ALTER ROLE" in sql and "NOLOGIN" in sql:
        mark("revoked")
    if "WITH INHERIT FALSE" in sql:
        mark("noinherit")
    elif "WITH INHERIT TRUE" in sql:
        (directory/"noinherit").unlink(missing_ok=True)
    if "md5(data)" in sql:
        emit((directory/"pg-state").read_text() if exists("pg-state") else "empty")
    if "pg_terminate_backend" in sql and "WHERE stopped" in sql:
        mark("sessions-dropped")
        emit("1")
    if "pg_locks" in sql:
        emit("0" if exists("sessions-dropped") else "1")
    if "SELECT count(*)" in sql:
        emit("0")
    if "SELECT * FROM other.secret" in sql:
        if case == "cross-schema-open": emit("secret")
        print("permission denied for schema other",file=sys.stderr)
        sys.exit(1)
    if sql.strip() == "SELECT 1":
        if exists("revoked"):
            print("role is not permitted to log in",file=sys.stderr)
            sys.exit(1)
        emit("1")
    if "SHOW server_version" in sql: emit("16.0")
    if any(x in sql for x in ("REVOKE","DROP ROLE","CREATE DATABASE","CREATE ROLE","ALTER ROLE","pg_terminate_backend","DROP DATABASE")):
        emit("")
if tool == "unshare":
    os.execvp("tofu",args[args.index("tofu")+1:])
if tool == "kind":
    if args[:2] == ["get","kubeconfig"]: emit("fake kubeconfig")
    if args[:2] in (["create","cluster"],["delete","cluster"]): emit("")
if tool == "docker":
    if args[0]=="inspect": emit("127.0.0.2")
    if "iptables" in args or "ip6tables" in args: emit("")
    if "crictl" in args and "ps" in args:
        emit({"containers":[{"id":"container","labels":{"io.kubernetes.pod.uid":"pod-uid"}}]})
    if "crictl" in args and "exec" in args:
        value=int((directory/"heartbeat").read_text())+1 if exists("heartbeat") else 1
        mark("heartbeat",str(value))
        emit(str(value))
if tool == "kubectl":
    if args[:2] == ["version","-o"]:
        emit({"serverVersion":{"gitVersion":"v1.34.1"}})
    if args[:2] == ["delete","node"]:
        mark("node-deleted")
        emit("")
    if args[0] in ("apply","wait","delete"):
        emit("")
    if args[:2] == ["get","pod"]:
        terminated=exists("node-deleted") and case=="forged-termination"
        metadata={"uid":"pod-uid","finalizers":["fleet-s0.devicechain.io/observe"]}
        if exists("node-deleted"): metadata["deletionTimestamp"]="2026-11-21T00:00:00Z"
        emit({"metadata":metadata,"status":{"phase":"Failed" if terminated else "Running",
            "conditions":[],"containerStatuses":[{"state":{"terminated":{}} if terminated else {"running":{}}}]}})
    if args[:2] == ["get","pv"]:
        emit({"items":[] if case=="no-disk" else [{"spec":{"csi":{"volumeHandle":"projects/s0/zones/z/disks/s0-disk"}}}]})
    if args[:2] == ["get","volumesnapshotcontent"]:
        emit({"items":[{"status":{"snapshotHandle":"projects/s0/global/snapshots/s0-snapshot"}}]})
print("scripted fake: unexpected command: " + tool + " " + " ".join(args), file=sys.stderr)
sys.exit(97)
