#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Strict scripted CLI for S0 safety controls; unexpected commands fail closed."""
import json
import os
import pathlib
import sys
import time

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

def lines(name):
    return (directory / name).read_text().split() if exists(name) else []

def add_line(name, value):
    with (directory / name).open("a") as f:
        f.write(value + "\n")

def drop_line(name, value):
    keep = [x for x in lines(name) if x != value]
    (directory / name).write_text("".join(x + "\n" for x in keep))

def create_cloud_cluster(name):
    add_line("clusters", name)
    seq = int(lines("idseq")[0]) + 1 if exists("idseq") else 1
    mark("idseq", str(seq))
    mark("id-" + name, "id-%d" % seq)

def impersonated(args):
    return any(a.startswith("--impersonate-service-account=") for a in args)

def denied():
    print("PERMISSION_DENIED", file=sys.stderr)
    sys.exit(1)

if tool == "unshare":
    args = args[args.index("tofu")+1:]
    tool = "tofu"

if tool == "gcloud":
    joined = " ".join(args)
    if args[:2] == ["iam", "workload-identity-pools"] or args[:3] in (["iam", "service-accounts", "add-iam-policy-binding"],
                                                                  ["iam", "service-accounts", "remove-iam-policy-binding"]):
        emit("")
    if args[:2] == ["container", "operations"]:
        if "status!=DONE" in joined or not exists("ops"):
            emit("")
        import re as _re
        m = _re.search(r"operationType=(\S+) AND targetLink~(\S+)", joined)
        have = (directory / "ops").read_text().splitlines()
        emit("op-1" if m and (m.group(1) + "|" + m.group(2)) in have else "")
    if args[:2] == ["container", "clusters"] and any(a.startswith("--filter=name=") for a in args):
        want = [a for a in args if a.startswith("--filter=name=")][0].split("=", 2)[2]
        emit("\n".join(x for x in lines("clusters") if x == want))
    if args[:3] == ["container", "clusters", "describe"] and "value(id)" in joined:
        if args[3] not in lines("clusters"):
            print("NOT_FOUND", file=sys.stderr)
            sys.exit(1)
        emit((directory / ("id-" + args[3])).read_text())
    if args[:3] == ["container", "clusters", "describe"] and "value(endpoint)" in joined:
        emit("203.0.113.1")
    if args[:3] == ["container", "clusters", "describe"] and "clusterCaCertificate" in joined:
        emit("Q0E=")
    if args[:3] == ["container", "clusters", "update"]:
        emit("")
    if args[:3] == ["compute", "instances", "list"]:
        emit("gke-s0-e-i-node-1")
    if args[:3] == ["compute", "disks", "create"] or args[:3] == ["compute", "disks", "delete"]:
        emit("")
    if args[:3] == ["compute", "disks", "snapshot"]:
        if impersonated(args) and case != "apply-open":
            denied()
        if not impersonated(args) and case == "control-broken":
            denied()
        emit("")
    if args[:3] in (["compute", "images", "create"], ["compute", "instances", "attach-disk"], ["compute", "instances", "add-metadata"]):
        if impersonated(args) and case != "apply-open":
            denied()
        emit("")
    if args[:3] in (["compute", "snapshots", "delete"], ["compute", "images", "delete"],
                    ["compute", "instances", "detach-disk"], ["compute", "instances", "remove-metadata"]):
        emit("")
    if args[:3] == ["container", "node-pools", "create"] and any(a.startswith("--service-account=") for a in args):
        sa = [a for a in args if a.startswith("--service-account=")][0].split("=", 1)[1]
        if impersonated(args):
            if case == "apply-narrow" or (sa != os.environ.get("FLEET_S0_NODE_IDENTITY") and case != "apply-open"):
                denied()
        emit("")
    if args[:2] == ["auth", "list"]:
        emit("operator@example.invalid")
    if args[:2] == ["auth", "print-access-token"]:
        if exists("disabled"):
            print("PERMISSION_DENIED: identity disabled", file=sys.stderr)
            sys.exit(1)
        emit("fake-token-subject" if impersonated(args) else "fake-token")
    if args[:2] == ["projects", "describe"]:
        if "value(lifecycleState)" in " ".join(args):
            emit("ACTIVE" if case == "canary-timeout" else "DELETE_REQUESTED")
        if "value(labels.fleet-s0)" in " ".join(args):
            emit("production" if case == "wrong-project" else "throwaway")
        emit("123456789")
    if args[:3] == ["alpha", "resource-manager", "liens"]:
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
        if args[2] == "create":
            create_cloud_cluster(args[3])
            emit("")
        if args[2] == "get-credentials":
            emit("")
        if args[2] == "list":
            emit("s0-e-b" if case == "cluster-exists" else "")
        if args[2] == "describe":
            emit("RUNNING")
        if args[2] == "delete":
            if exists("deny") and case != "deny-ineffective":
                denied()
            drop_line("clusters", args[3])
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
    if any("sts.googleapis.com" in arg for arg in args):
        token = [a for a in args if a.startswith("subject_token=")][0].split("=", 1)[1]
        bad = (case == "wif-baseline-refused" or
               (token.endswith("wrongaud") and case != "wif-aud-open") or
               (token.startswith("eyJ") and case != "wif-iss-open"))
        output.write_text(json.dumps({"error": {"status": "INVALID_ARGUMENT"}} if bad else {"access_token": "fed-" + token, "expires_in": 3599}))
        emit("400" if bad else "200")
    if any("iamcredentials.googleapis.com" in arg for arg in args):
        header = [a for a in args if a.startswith("Authorization: Bearer ")][0].split("Bearer ", 1)[1]
        other = "s0-wif-other" in header and case != "wif-subject-open"
        output.write_text(json.dumps({"error": {"status": "PERMISSION_DENIED"}} if other else {"accessToken": "acc"}))
        emit("403" if other else "200")
    if any("testIamPermissions" in arg for arg in args) and any(("getCredentials" in a or "osAdminLogin" in a) for a in args):
        perm = "container.clusters.getCredentials" if any("getCredentials" in a for a in args) else "compute.instances.osAdminLogin"
        subject = any("fake-token-subject" in a for a in args)
        output.write_text(json.dumps({"permissions": [perm] if (not subject or case == "apply-open") else []}))
        emit("200")
    if any("testIamPermissions" in arg for arg in args):
        output.write_text(json.dumps({"permissions":[] if exists("deny") and case != "deny-ineffective" else ["container.clusters.delete"]}))
        emit("200")
    if any("container.googleapis.com" in arg for arg in args):
        status = "503" if case == "token-server-error" else "403" if exists("disabled") else "200"
        output.write_text(json.dumps({"error":{"status":"PERMISSION_DENIED"}} if status=="403" else {}))
        emit(status)
if tool == "tofu" and args and args[0].startswith("-chdir=") and portable(args[0].split("=",1)[1]).parent.name == "h":
    d = portable(args[0].split("=",1)[1])
    action = args[1]
    name = None
    names = []
    for a in args:
        if a.startswith("-var=names="):
            names = json.loads(a.split("=", 2)[2])
    if names:
        name = names[0]
    state = d / "terraform.tfstate"
    resources = state.read_text().split() if state.exists() else []
    def write_state(items):
        state.write_text("\n".join(items))
    if action == "init":
        emit("")
    if action == "state" and args[2] == "list":
        emit("\n".join(resources))
    if action == "state" and args[2] == "show":
        emit("tainted" if case == "failed-create-tainted" else "")
    if action == "import":
        # Real tofu: ADDR ID are the last two arguments, and a flag after them is a usage error.
        tail = args[args.index("import")+1:]
        if len(tail) < 2 or any(a.startswith("-") for a in tail[-2:]) or any(not a.startswith("-") for a in tail[:-2]):
            print("The import command expects two arguments.", file=sys.stderr)
            sys.exit(1)
        write_state(resources + ["google_container_cluster.c"])
        emit("")
    if action == "plan":
        if "google_container_cluster.c" in resources or not name:
            emit("")
        print(json.dumps({"type": "planned_change", "change": {"resource": {"addr": 'google_container_cluster.c["%s"]' % name}, "action": "create"}}, separators=(",", ":")))
        sys.stdout.flush()
        sys.exit(2 if "-detailed-exitcode" in args else 0)
    if action == "apply":
        protect = "-var=protect=true" in args
        if d.name == "pdp":
            if name and name not in lines("clusters"):
                create_cloud_cluster(name)
            if name:
                mark("protect", "true" if protect else "false")
                emit("")
            held = exists("protect") and (directory / "protect").read_text() == "true"
            if (held and case != "protect-ineffective") or (not held and case == "dp-control-ineffective"):
                print("Error: Instance cannot be destroyed: deletion_protection is set to true", file=sys.stderr)
                sys.exit(1)
            drop_line("clusters", "s0-e-h-dp")
            emit("")
        if d.name == "pfc":
            create_cloud_cluster(name)
            if case == "provider-deletes":
                add_line("ops", "DELETE_CLUSTER|" + name)
                drop_line("clusters", name)
            print("Error: timeout while waiting for state to become DONE", file=sys.stderr)
            sys.exit(1)
        if d.name == "p1b":
            create_cloud_cluster(name)
            write_state(["google_container_cluster.c"])
            emit("")
        first = not (d / "applies").exists()
        (d / "applies").write_text("1")
        if first:
            point = int(d.name[1:])
            create_cloud_cluster(name)
            add_line("ops", "CREATE_CLUSTER|" + name)
            if point == 2:
                add_line("ops", "DELETE_NODE_POOL|" + name)
            events = {3: ["google_container_cluster.c"], 4: ["google_container_cluster.c", "google_container_node_pool.p1"],
                      5: ["google_container_cluster.c", "google_container_node_pool.p1", "google_container_node_pool.p2"]}.get(point, [])
            write_state(events)
            for r in events:
                print(json.dumps({"type": "apply_complete", "hook": {"resource": {"addr": r + '["%s"]' % name}}}))
                sys.stdout.flush()
            deadline = time.time() + 120
            while time.time() < deadline:
                time.sleep(0.05)
            sys.exit(1)
        if "google_container_cluster.c" in resources:
            write_state(["google_container_cluster.c", "google_container_node_pool.p1", "google_container_node_pool.p2"])
            emit("")
        if case == "dup-create":
            create_cloud_cluster(name)
            emit("")
        print("Error: googleapi: Error 409: Already exists: cluster " + name, file=sys.stderr)
        sys.exit(1)
    emit("")
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
    if "crictl" in args and "exec" in args and any(x.endswith("chain.log") for x in args):
        now = int(time.time())
        cut = int(lines("cutat")[0]) if exists("cutat") else None
        control = args[1].endswith("-worker2")
        out = ["L sts 3599", "L access 600"]
        if cut is None:
            out.append("%d 200" % (now - 30))
        else:
            refuse_at = {"late-refusal": 5000, "no-refusal": None}.get(case, 4000)
            for off in range(-30, 6001, 300):
                code = 200
                if not control and refuse_at is not None and off >= refuse_at:
                    code = 403
                if control and case == "ctl-dead" and off >= 100:
                    code = 403
                out.append("%d %d" % (cut + off, code))
        emit("\n".join(out))
    if "crictl" in args and "exec" in args:
        value=int((directory/"heartbeat").read_text())+1 if exists("heartbeat") else 1
        mark("heartbeat",str(value))
        emit(str(value))
if tool == "kubectl":
    if args[0].startswith("--kubeconfig="):
        kc = portable(args[0].split("=", 1)[1]).read_text()
        if "fake-token-subject" in kc and case != "apply-open":
            print('Error from server (Forbidden): secrets is forbidden: User cannot list resource "secrets"', file=sys.stderr)
            sys.exit(1)
        emit("NAME TYPE")
    if args[:2] == ["get", "--raw"]:
        key = {"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256", "n": "abc", "e": "AQAB"}
        if case == "jwks-private":
            key["d"] = "secret"
        emit({"keys": [key]})
    if args[:1] == ["exec"]:
        emit("tok-" + args[1] + "-" + args[-1].rsplit("/", 1)[1])
    if args[:2] == ["create", "serviceaccount"]:
        emit("")
    if args[:1] == ["delete"] and len(args) > 1 and args[1] == "pod" and "--force" in args:
        mark("cutat", str(int(time.time())))
        emit("")
    if args[:2] == ["get", "pod"] and "-o" in args and args[args.index("-o") + 1].startswith("jsonpath"):
        emit("pod-uid")
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
