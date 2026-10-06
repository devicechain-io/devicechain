# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Confirms the sitepulse scenario's objects through the platform GraphQL, using the
# runner's tenant-admin token in-process (never printed).
import json, urllib.request
cfg = json.load(urllib.request.urlopen("http://127.0.0.1:8090/config.json"))
tok = cfg["token"]
def gql(area, q, v=None):
    req = urllib.request.Request(f"http://localhost/api/{area}/graphql",
        data=json.dumps({"query": q, "variables": v or {}}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + tok})
    r = json.load(urllib.request.urlopen(req))
    if r.get("errors"): raise SystemExit(f"{area}: {r['errors']}")
    return r["data"]
print("config.json:", {k: cfg.get(k) for k in ("tenant","instanceId","manifestId","wsUrl","mqttBroker","mqttTLSInsecure")})
ids = [f"SP-{p}-{n:04d}" for p in ("HL","LD","DZ") for n in range(1,7)] + ["SP-PL-0001"]
d = gql("device-management", """query($ids:[String!]!){ devicesByExternalId(externalIds:$ids){ token externalId
  deviceType{ token profile{ token metricDefinitions{metricKey} commandDefinitions{token commandKey} detectionRules{token name enabled} } } } }""", {"ids": ids})["devicesByExternalId"]
print("devicesByExternalId:", len(d), "of", len(ids))
missing = set(ids) - {x["externalId"] for x in d}
print("missing:", sorted(missing))
for x in sorted(d, key=lambda x: x["externalId"]): print(" ", x["externalId"], x["token"], x["deviceType"]["token"])
profiles = {x["deviceType"]["profile"]["token"]: x["deviceType"]["profile"] for x in d}
for p in profiles.values():
    print("profile", p["token"], "metrics", [m["metricKey"] for m in p["metricDefinitions"]],
          "commands", [c["commandKey"] for c in p["commandDefinitions"]],
          "rules", [(r["token"], r["enabled"]) for r in p["detectionRules"]])
r = gql("device-management", "query{ detectionRules(criteria:{pageNumber:1,pageSize:50}){ results{ token name enabled deviceProfile{token} } pagination{ totalRecords } } }")
print("detectionRules (tenant):", [(x["token"], x["enabled"], x["deviceProfile"]["token"]) for x in r["detectionRules"]["results"]])
db = gql("dashboard-management", "query{ dashboards(criteria:{pageNumber:1,pageSize:20}){ results{ token name } } }")
print("dashboards:", [(x["token"], x["name"]) for x in db["dashboards"]["results"]])
