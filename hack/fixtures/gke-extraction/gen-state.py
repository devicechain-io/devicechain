#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Generate a sanitized state fixture for the extraction plan gate, offline.

Usage: gen-state.py <root-dir> <vars-file> <out.state.json> [--legacy-loadgen]

Plans the root against an empty state (no cloud: dummy token, -refresh=false), builds a
version-4 state from the plan's PLANNED values (a state is what the provider would have
recorded after an apply), then plans again against that state to learn the values the
terraform_data settle resource would hold, and adds it. Every id is derived from the example
project, so nothing in the output names a real project, endpoint or certificate.
With --legacy-loadgen the loadgen pool is recorded at its pre-for_each address,
google_container_node_pool.loadgen[0].
"""
import json, os, subprocess, sys, tempfile, uuid

PROJECT = "example-project"
SCHEMA_VERSION = {"google_container_cluster": 2, "google_container_node_pool": 1, "terraform_data": 0}
PROVIDER = {"google": 'provider["registry.opentofu.org/hashicorp/google"]',
            "terraform_data": 'provider["terraform.io/builtin/terraform"]'}

def run(args, cwd, env):
    return subprocess.run(args, cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout

def plan_json(root, vars_file, state):
    env = dict(os.environ, GOOGLE_OAUTH_ACCESS_TOKEN="dummy", GOOGLE_CLOUD_PROJECT=PROJECT, TF_IN_AUTOMATION="1")
    with tempfile.TemporaryDirectory() as t:
        out = os.path.join(t, "p.tfplan")
        run(["tofu", "plan", "-refresh=false", "-input=false", "-no-color", "-var-file=" + os.path.abspath(vars_file),
             "-state=" + state, "-out=" + out], root, env)
        return json.loads(run(["tofu", "show", "-json", out], root, env))

def instances(plan):
    res = []
    def walk(m):
        res.extend(m.get("resources", []))
        for c in m.get("child_modules", []):
            walk(c)
    walk(plan["planned_values"]["root_module"])
    return res

def cty_type(v):
    """The cty JSON type of a plain JSON value, for a dynamically typed attribute."""
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, (int, float)):
        return "number"
    if isinstance(v, str):
        return "string"
    if isinstance(v, dict):
        return ["object", {k: cty_type(x) for k, x in v.items()}]
    if isinstance(v, list):
        return ["tuple", [cty_type(x) for x in v]]
    sys.exit("cannot type %r" % (v,))

def zero_for(ty):
    if ty in ("bool",):
        return False
    if ty == "number":
        return 0
    if ty == "string":
        return ""
    if isinstance(ty, list) and ty[0] in ("list", "set", "tuple"):
        return []
    if isinstance(ty, list) and ty[0] in ("map", "object"):
        return {}
    return None

def schema_attr_types(provider_key, typ):
    """name -> cty type for the top level attributes, name -> nested block schema for blocks."""
    sch = SCHEMAS["provider_schemas"][provider_key]["resource_schemas"][typ]["block"]
    return sch

def fill_unknowns(vals, unknown, block):
    """Set every value the plan says is unknown-after-apply to a typed zero, guided by the schema."""
    attrs = (block or {}).get("attributes", {})
    blocks = (block or {}).get("block_types", {})
    for k, u in (unknown or {}).items():
        if k in attrs:
            if u is True:
                if vals.get(k) is None:
                    vals[k] = zero_for(attrs[k]["type"])
        elif k in blocks and isinstance(u, list):
            sub = blocks[k]["block"]
            cur = vals.get(k)
            if not isinstance(cur, list):
                continue
            for i, ui in enumerate(u):
                if i < len(cur) and isinstance(ui, dict) and isinstance(cur[i], dict):
                    fill_unknowns(cur[i], ui, sub)

def to_entry(r, legacy):
    typ, name, idx = r["type"], r["name"], r.get("index")
    vals = dict(r["values"])
    loc = vals.get("location")
    if typ == "google_container_cluster":
        vals["id"] = "projects/%s/locations/%s/clusters/%s" % (PROJECT, loc, vals["name"])
    elif typ == "google_container_node_pool":
        cluster = "projects/%s/locations/%s/clusters/%s" % (PROJECT, loc, CLUSTER[0])
        vals["cluster"] = cluster
        vals["id"] = cluster + "/nodePools/" + vals["name"]
    elif typ == "terraform_data":
        vals.setdefault("id", str(uuid.UUID(int=1)))
        for k in ("input", "output", "triggers_replace"):
            if vals.get(k) is not None:
                vals[k] = {"value": vals[k], "type": cty_type(vals[k])}
    e = {"mode": "managed", "type": typ, "name": name, "provider": PROVIDER["google" if typ.startswith("google") else "terraform_data"],
         "instances": [{"schema_version": SCHEMA_VERSION[typ], "attributes": vals}]}
    if legacy and typ == "google_container_node_pool" and idx == "loadgen":
        idx = 0
        e["name"] = "loadgen"
    if idx is not None:
        e["instances"][0]["index_key"] = idx
    return e

CLUSTER = [None]
SCHEMAS = {}

def main():
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    root, vars_file, out = sys.argv[1:4]
    legacy = "--legacy-loadgen" in sys.argv
    SCHEMAS.update(json.loads(subprocess.run(["tofu", "providers", "schema", "-json"], cwd=root, check=True, capture_output=True, text=True).stdout))
    with tempfile.TemporaryDirectory() as t:
        empty = os.path.join(t, "empty.json")
        json.dump({"version": 4, "terraform_version": "1.10.10", "serial": 1, "lineage": str(uuid.UUID(int=2)), "outputs": {}, "resources": []}, open(empty, "w"))
        p1 = plan_json(root, vars_file, empty)
        rs = [r for r in instances(p1) if r["type"] != "terraform_data"]
        for r in rs:
            if r["type"] == "google_container_cluster":
                CLUSTER[0] = r["values"]["name"]
        state = {"version": 4, "terraform_version": "1.10.10", "serial": 1, "lineage": str(uuid.UUID(int=2)), "outputs": {},
                 "resources": [to_entry(r, legacy) for r in rs], "check_results": None}
        s1 = os.path.join(t, "s1.json")
        # Settle: attributes the provider reports as computed come back "known after apply" in
        # an update plan. Fill them with typed zeroes until the plan no longer wants an update.
        for _ in range(6):
            json.dump(state, open(s1, "w"))
            pn = plan_json(root, vars_file, s1)
            dirty = False
            for rc in pn.get("resource_changes", []):
                if rc["address"].split(".")[-2:-1] == [] or not rc["type"].startswith("google_"):
                    continue
                if rc["change"]["actions"] == ["update"]:
                    for e in state["resources"]:
                        if e["type"] == rc["type"]:
                            for inst in e["instances"]:
                                key = inst.get("index_key")
                                addr_key = rc.get("index")
                                if (key == addr_key) or (legacy and key == 0 and addr_key == "loadgen"):
                                    fill_unknowns(inst["attributes"], rc["change"]["after_unknown"], SCHEMAS["provider_schemas"]["registry.opentofu.org/hashicorp/google"]["resource_schemas"][e["type"]]["block"])
                                    dirty = True
            if not dirty:
                break
        p2 = plan_json(root, vars_file, s1)
        for r in instances(p2):
            if r["type"] == "terraform_data":
                state["resources"].append(to_entry(r, legacy))
    state["resources"].sort(key=lambda e: (e["type"], e["name"]))
    with open(out, "w", newline="\n") as f:
        json.dump(state, f, indent=2, sort_keys=True)
        f.write("\n")

main()
