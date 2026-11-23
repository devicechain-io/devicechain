# Fleet S0 premise spike

Run a selected experiment with `bash hack/fleet-s0/run.sh E-a`, or its
`E-a.sh` entry point. E-g defaults to local PostgreSQL; select its GCS half
with `--backend=gcs`. Every entry point has `--self-test`; these use scripted
commands and make no cloud calls. Run the entry points with Bash, Python 3,
and the usual Unix tools. Real local probes also need OpenTofu 1.10.10,
PostgreSQL clients, and (E-l) kind, Docker and kubectl. E-j's offline probe
requires Linux user/network namespaces and a cached/downloadable google plugin.

Tier C requires all of `FLEET_S0_ADMIN_PROJECT`, `FLEET_S0_PROJECT`,
`FLEET_S0_FOLDER`, `FLEET_S0_LOCATION`, `FLEET_S0_SCHEDULER_LOCATION`,
`FLEET_S0_CANARY_PROJECT`, and `FLEET_S0_TEST_IDENTITY`. The spike project
must already exist, be dedicated to this spike, and carry
`fleet-s0=throwaway`. The canary project ID must be unused. The admin project
is deliberately required, with no default. `FLEET_S0_FOLDER` is the canary's parent: a folder ID,
or `organizations/ID` when the projects sit directly under the organization. Provision
`s0-reaper@ADMIN.iam.gserviceaccount.com` and
`s0-cluster-reaper@ADMIN.iam.gserviceaccount.com` there and enable Scheduler,
Resource Manager and the relevant APIs. The invoking user needs project
creation, scoped custom-role/IAM administration and Scheduler creation/act-as
permissions; the Scheduler service agent needs permission to mint OAuth
tokens for those admin identities. No key file or service-account CLI login
is accepted for Tier C.

The shared outer code creates/verifies a custom role containing only project
delete on each target project and arms the project DELETE at T+6h. Inner
targets use the separate cluster-delete-only identity at T+4h. The same
outer code arms the canary at its own T+5min and waits for
`DELETE_REQUESTED`; a read-back or manually triggered job is not a rehearsal.
Only reaper setup and the canary are bootstrap exceptions to the creation
barrier. No experiment resource is created until all checks pass in this
process. Project liens and the exact singleton E-b principal are checked
again immediately before attaching a deny. The jobs use UTC cron deadlines
rounded up to the next minute and retries; remove the annual Scheduler jobs
after independently verifying that their target projects have been deleted.

All measured runs require `FLEET_S0_PROVIDER_VERSION` (the exact candidate
google pin). E-c also needs a pre-granted, exclusively update/get/operation-get
role via `FLEET_S0_UPDATE_ONLY_ROLE` and `FLEET_S0_UPDATE_ONLY_IDENTITY`.
E-g(pg) and E-j need `FLEET_S0_PG_ADMIN_URL`, restricted to localhost; they
create and remove a dedicated database and per-run roles. Cloud clusters
are zonal, one-node fixtures; keep the spike below the spec's 25 USD budget.
The outer project reaper remains armed on every error.

E-d, E-f, E-h and E-i are instrumented (`instrument.sh`, with their verdict functions in
`instrument.py`), and each has been run ONCE against a throwaway project under the grants of the IAM
request. A run that produced no verdict is recorded INCONCLUSIVE with `blocked: "ATTEMPTED"` and its own
cause; it is rerun only on a new approval. Every other Tier C record stays `blocked: "U2"`. Their
self-tests use scripted CLIs and make no cloud calls; each one shows the probe recording HOLDS, then
recording FAILS under each scripted defect, then recording NO verdict when its negative control cannot
fail (exit non-zero, nothing written to `results.jsonl`: INCONCLUSIVE is an absent entry, never a pass).

| Probe | Measures | Negative control (a scripted run must turn it FAILS or void) |
| --- | --- | --- |
| E-d | A kind pod (apiserver pinned to a known issuer, its public JWKS read from `/openid/v1/jwks`, checked to hold no private member, and uploaded with the provider) exchanges its projected token through STS and impersonation. Another KSA subject, a wrong audience and a token forged under a wrong issuer must each be refused. A deleted and recreated same-name KSA is RECORDED, never judged. | Any refused-control accepted gives FAILS. A baseline exchange that is itself refused gives no verdict. |
| E-f | A runner on a 3-node kind cluster refreshes the full chain (KSA token, STS, access token) and calls the cloud every 5 s. Its node is partitioned both ways, the node deleted, the pod force-deleted (E-l shows the process survives). The three lifetimes are recorded separately with the first 401/403 after the cut. Mode `kind_jwks` only. | The bound `ksa + sts + access + skew` is COMPUTED. A refusal after it, or none, gives FAILS. An uncut runner must still be authorized at or after the bound or there is no verdict. |
| E-h | Five kill points of a first create (cluster accepted, default pool removal, between cluster and first pool, between pools, after the last pool), each followed by the next plain plan, the next plain apply (must fail ALREADY_EXISTS, no duplicate), and a verified import into a copy of the state. Also: whether the provider deletes after a failed create (30 s create timeout), whether the API id is stable across delete and recreate, and whether `deletion_protection = true` in state makes an orphan cluster delete fail. | A second cluster, a retry that did not fail ALREADY_EXISTS, or an orphan delete not refused gives FAILS. The same orphan delete with protection off must really delete the cluster, or there is no verdict. |
| E-i | The candidate apply identity attempts each indirect path of the H14 table: secrets through the Kubernetes API, getCredentials, disk snapshot/image/attach, node ssh keys and OS Login, actAs a foreign identity, and the trust-changing updates IAM cannot separate (recorded open, with the control that closes them). | Every attempt also runs as the invoking operator and must SUCCEED (else no verdict: the attempt was malformed). A `must_succeed` calibration (actAs the designated node identity) that is refused also gives no verdict. An open `refuse` path gives FAILS. |

E-i needs `FLEET_S0_APPLY_IDENTITY`, `FLEET_S0_NODE_IDENTITY` and `FLEET_S0_OTHER_IDENTITY`;
`candidate-apply-role.txt` is the permission list under test. Tunables: `FLEET_S0_KSA_TTL_S` (>= 600),
`FLEET_S0_ACCESS_TTL_S` (<= 3600), `FLEET_S0_SKEW_SECONDS`, `FLEET_S0_IAM_PROPAGATION_SECONDS` (E-d waits up to this, default 420, for a new binding to take effect before its baseline). E-f waits roughly the bound (about 80
minutes at the defaults). The gcloud flags of E-i's trust-changing attempts are calibrated by the first
real run: an attempt that fails for a reason other than a permission refusal yields no verdict, never a pass.

## Tier K, reproducibly

`bash hack/fleet-s0/in-container.sh [E-g] [E-j] [E-l]` runs the local experiments inside the
pinned toolchain image `hack/fleet-s0/runner/Dockerfile` (OpenTofu 1.10.10, kubectl, kind and a
PostgreSQL client), so the result does not depend on what the host has installed. It starts one
throwaway Postgres container for E-g and E-j, and one kind cluster (three nodes, node image pinned
in `kind.sh`) for E-l, and removes them. It needs only Docker; it never touches another cluster or
container. Evidence lands in `FLEET_S0_OUTPUT_DIR` (default `./fleet-s0-<run>-out`).
`python3 hack/fleet-s0/assemble.py OUT.json RUN_DIR...` folds the runs' candidate records into
the reviewed record and adds every Tier C experiment as `INCONCLUSIVE` with
`measured.blocked = "U2"`: a refusal to measure, never a pass. A blocked entry's digest covers its
notice text, since it has no transcript. `--base gke.json` carries forward the checked-in measured
entries (their run directories are private), and `--attempted ID RUN_DIR REASON` records a run that
produced no verdict as `blocked: "ATTEMPTED"`, digested over that run's transcript.

## Premise record format (schema version 1)

`deploy/fleet/premises/gke.json` is an object with `schema_version: 1` and `experiments`, an
array. An experiment that is absent has not been measured. One that is present with
`measured.blocked` set must be `INCONCLUSIVE` (the parser rejects anything else), and
`RequireHolds` never accepts it. Each item has:

| Field | Type and constraint |
| --- | --- |
| `id` | Unique E-a through E-l, E-f2; split measurements E-g-pg/E-g-gcs and E-j-1 through E-j-5 |
| `verdict` | HOLDS, FAILS or INCONCLUSIVE |
| `measured` | Object containing the actual measurements and negative controls |
| `tofu_version`, `provider_version` | Exact three-part versions, optional prerelease/build suffix |
| `kubernetes_version` | Exact version, required when binds includes kubernetes |
| `date` | UTC measurement date YYYY-MM-DD |
| `transcript_sha256` | 64 lowercase hexadecimal characters |
| `binds` | Nonempty unique array drawn from provider, tofu, kubernetes, cloud |

E-j-N corresponds to spec E-j(N). Split results must be required by their exact IDs; a parent ID
never implies its children's results. Unknown fields, duplicate IDs/bindings and malformed
metadata are rejected by the Go parser.

The runner writes a review candidate `gke.json` and confidential evidence into a fresh private
directory. It never modifies the checked-in record. Store transcripts, states and plans in the
maintainer's private bucket under the transcript digest before hand-committing reviewed
measurements. Do not commit them. Integrity rests on review plus that digest, not a signature or
automatic attestation; self-test results are not cloud evidence.

## Staleness

`Staleness(record, pins, now)` is pure. `ProviderPinFromRoots("deploy/fleet")` reads the exact
google pin from the fleet roots' own `versions.tf` (the roots must agree and the pin must not be
a range), the tofu pin is CI's `tofu_version`, and the Kubernetes pin is `S0_KIND_NODE_IMAGE` in
`kind.sh`. Only a component in a premise's binds can stale that premise; cloud-only premises
expire after 180 UTC calendar days. Future or invalid cloud-only dates fail closed.
`deploy/fleet_premises_test.go` applies it to the E-j premises the fleet roots need; until
`deploy/fleet/gke-*` exist it skips and says why. A moved pin turns only that gate red, not the
tree.
