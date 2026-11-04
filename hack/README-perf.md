# Profiling a performance round

Maintainer notes for the ingest-profiling rounds (spec: ingest performance, profile first).
Everything here is **run-only**: every switch defaults OFF, none is reachable through a
Service, Ingress or Gateway, and a switch is turned off again after the run.

| What | Switch | Default | Where | Restarts |
|---|---|---|---|---|
| Go profiles of one service | `functionalAreas.<area>.profiler.enabled` | off | Helm values, or `dcctl --values` | that area's pods |
| Block / mutex profiles | `functionalAreas.<area>.profiler.blockRate` (ns per sampled event; 1 = all), `.mutexFraction` (1 in N) | 0 (off, the endpoints answer 404) | Helm values or `dcctl --values`, need `enabled` | that area's pods |
| NATS server pprof | OpenTofu `nats_profiling_port` (instance root; module `profiling_port`) | 0 (off) | `deploy/opentofu` | broker, one server at a time |
| NATS block profile | `nats_profiling_block_rate` (needs the port) | 0 | `deploy/opentofu` | broker |
| `pg_stat_statements`, relational DB | OpenTofu `postgres_statement_stats` (cluster root) | false | `deploy/opentofu` | Postgres (rolling) |
| `pg_stat_statements`, event store | OpenTofu `timescale_statement_stats` (instance root) | false | `deploy/opentofu` | Postgres (rolling) |

The NATS monitoring endpoints (`/varz`, `/jsz`, `/connz` on the `monitor` port, 8222) are on in
the broker chart already and need no switch. The collector reaches them, and every profile
port, with `kubectl port-forward` to a pod.

## Setting the switches through `dcctl` (what the first real round found)

- The OpenTofu variables are plain `TF_VAR_*` environment variables on the `dcctl` process:
  `TF_VAR_postgres_statement_stats=true` for `dcctl install`, and
  `TF_VAR_timescale_statement_stats=true TF_VAR_nats_profiling_port=6061
  TF_VAR_nats_profiling_block_rate=100000` for `dcctl bootstrap`. Set them before the instance
  exists, so no database or broker restart lands in a measurement.
- 🔴 `dcctl bootstrap --values` REFUSES the profiler keys: `functionalAreas.<area>.profiler` is
  deliberately outside the tunable allowlist (`bootstrap/valuesoverlay.go`), so the chart value in
  the table above cannot be set through `dcctl` as shipped. r25 ran a local `dcctl` build with
  `"functionalAreas.*.profiler"` added to `tunableValues`; without that, set
  `DC_PROFILER_ADDRESS` (`127.0.0.1:6060`), `DC_PROFILER_BLOCK_RATE` and
  `DC_PROFILER_MUTEX_FRACTION` on each Deployment by hand, which the operator keeps until the next
  upgrade.
- The collector needs every Go-service Deployment profiled (the console, a static nginx, is
  skipped), so list every area the profile deploys.

## Running it against a real cluster

- The relational store lives in the cluster namespace (`dc-system`), not the instance's: pass
  `--rdb-namespace dc-system`.
- The steady marker is written by the harness, which runs in the cluster; mirror it to the file
  `--marker` names (a loop of `kubectl exec <loadgen> -- cat <marker>`).
- On Windows under Git Bash do NOT export `MSYS_NO_PATHCONV=1` for the collector: it makes `jq`
  unable to open the `/tmp` and `/c/...` paths the script passes it, and the collector then
  refuses every marker as "not JSON". The collector protects its own `kubectl get --raw /api/...`
  calls itself.

## Safety properties (each one is tested)

- The service listener binds the pod's loopback by default and is **never** a container port,
  a Service port or an ingress route: `backend/cli/bootstrap/profiler_render_test.go` and
  `profiler_route_test.go` render the chart (default, and the full profile with an ingress and
  several listeners on) and fail if the port appears anywhere but a Deployment env value.
- The service refuses to start on contention sampling without the listener, and the chart
  refuses to render it: sampling nobody can read is cost for nothing.
- NATS's `prof_port` is unauthenticated (nats-server binds it on the pod's address, not
  loopback) and is declared on no port or Service, so it is reachable only by port-forward or
  from inside the pod network. It is a run switch for that reason.
  `deploy/opentofu/modules/nats/tests/profiling.tftest.hcl` pins "off renders nothing".
- `pg_stat_statements` needs **no image change**: the library and its control file ship in the
  CloudNativePG PostgreSQL 17 image the TimescaleDB operand image is built on (checked in
  `ghcr.io/cloudnative-pg/postgresql:17.11-standard-bookworm`, the `PG_IMAGE` of
  `deploy/images/timescaledb/Dockerfile`). The switch adds it to `shared_preload_libraries`
  **after** `timescaledb` and sets `pg_stat_statements.track=all`, `.max=10000`,
  `track_io_timing=on`; a value the caller already set wins. The collector runs
  `CREATE EXTENSION IF NOT EXISTS pg_stat_statements` (the view) itself.

## One round, in order

1. Before the run, with the instance up on a lane-private kubeconfig, turn the switches on
   (profiler on every area you want profiled with `blockRate`/`mutexFraction`, the NATS and
   Postgres variables above) and let the roll finish. Do this **before** the measurement,
   never during it.
2. Start the load with the steady-state marker:

   ```bash
   dc-loadtest-mqtt ... --steady-marker run/steady.json --steady-phase R2 --steady-warmup 5m
   dc-loadtest      ... --steady-marker run/steady.json --steady-phase R1 --steady-warmup 5m
   ```

   The marker is JSON: `warmup`, then `steady`, then `ended` or `aborted`
   (`backend/sims/dc-simulator/loadtest/steady.go` has the contract). MQTT promotes it on its
   progress cadence (`--report-interval`, 10 s by default).
3. Preflight, then collect inside the steady window:

   ```bash
   hack/perf-collect.sh --kubeconfig $KC --namespace dci-x --round r25 --phase R1 \
       --duration 60 --cpu-profiles 3 --marker run/steady.json --out perf-r25 --dry-run
   hack/perf-collect.sh --kubeconfig $KC --namespace dci-x --round r25 --phase R1 \
       --duration 60 --cpu-profiles 3 --marker run/steady.json --out perf-r25
   ```

   It refuses (exit 3) unless the marker says steady for this phase with enough window left, and
   it discards the phase if the marker moved or a pod was replaced meanwhile. Any missing
   component or non-200 profile is a named failure and exit 1 (`complete: false` in
   `r25-R1-manifest.json`).
4. Analyse:

   ```bash
   hack/perf-analyze.sh --dir perf-r25 --round r25 --phase R1 --events-hypertable <events table> --out r25-R1.md
   ```

5. Turn the switches off and verify the roll, as for any round.

## Self-tests

`hack/perf-collect.sh --self-test` and `hack/perf-analyze.sh --self-test` run against stub
tools. Each refusal has a case that proves it fires (a warmup/ended/aborted/absent marker, a
closing window, a short service, a 404 contention profile, a missing `pg_stat_statements`, a
replaced pod, `--dry-run` writing nothing), and the analyser has a counter that goes backwards.

## What only a cluster can show

The stubs answer the calls the scripts make; they cannot show that a real `kubectl exec ... psql`
authenticates as `postgres` on a CNPG pod, that the API server proxies each node's cAdvisor, that
the nats chart passes `config.merge.prof_port` into `nats.conf` and the monitor port is named
`monitor`, or that a 60 s CPU profile survives a port-forward. Expect to fix those on the first
use, and re-run the self-test after.

## Local A/B rig (`hack/perf-ab.sh`, `hack/perf-ab-compare.sh`)

Checks a change's effect on **per-event costs** on a kind cluster before a GKE round is spent on
it. One run of `perf-ab.sh` is one arm: warm-up, a steady window at a fixed offered rate, counters
read before and after, and a `summary.json` / `summary.md` with, per event persisted: NATS
messages (client in/out, non-client in/out, route in/out), NATS bytes, JetStream API calls, WAL
bytes and records on the event-store and relational primaries, rows per `INSERT INTO events` and per
`device_states` upsert, statements per second, CPU-seconds per 1,000 events per component, appends
per stream and deliveries per consumer, and the harnesses' own loss/duplicate verdict.

**What transfers to GKE.** Counts and bytes per event (messages, bytes, WAL records, rows per
statement), **compared at the same offered rate**: messages per event fall as the rate rises, so a
kind figure at 1,500 events/s is comparable to a GKE figure at 1,500 events/s and to nothing else.
CPU is a **ratio** between two arms on the same machine only. Latency, saturation, throttling,
absolute CPU and tail behaviour do not transfer; the rig reports CFS throttling so that a CPU-bound
arm is flagged rather than trusted.

Set-up, once per cluster (an HA kind cluster so route traffic exists; `deploy/local/kind-cluster-ha.yaml`,
changing the host ports if they are taken):

1. `TF_VAR_postgres_statement_stats=true dcctl install local --ha ...` and
   `TF_VAR_timescale_statement_stats=true dcctl bootstrap local <instance> ...` (use a unique image
   tag; the chart pulls `IfNotPresent`). The rig refuses to run without `pg_stat_statements`.
2. Linux builds of the load harnesses:
   `GOOS=linux CGO_ENABLED=0 go build -o <bin-dir>/dc-loadtest ./cmd/loadtest` (and `loadtest-mqtt`)
   in `backend/sims/dc-simulator`.

One arm (about 9 to 10 minutes with the defaults: 45 s warm-up, 40 s settle, 180 s window, then the
harnesses' read-back):

```bash
hack/perf-ab.sh --kubeconfig $KC --namespace dci-perf --instance perf --arm main-1 \
    --bin-dir $BIN --dcctl $DCCTL --out ab-out --rebalance [--roll] [--mqtt-rate 1000 --http-rate 500]
```

The load runs in a pod inside the cluster (no port-forward is held open). One arm at a time; the same
rate for every arm you compare; `--rebalance` so the broker's movable leaders sit in the same place
(the layout is recorded, and `compare` warns if two arms differ).

What the script does around the window, and why (each one was a measured confound, not a guess):

- **The sims run in a tier with headroom** (`ab-load-<rate>`: ingest ceiling 10x the offered rate, burst 20x,
  made by `dcctl sim create --tier --tier-config`; sim names `abt-<rate>-m/-h`). A tenant AT the default
  ceiling (1,000/s, burst 2,000) has events shed by the ingest gate whenever the host stalls the broker for
  more than ~2 s, and the harness reports that as loss. The rig measures cost, not governance. Each arm also
  snapshots the gate's shed counters over the drive and classifies a failed verdict as `counted shed: N` (the
  counters account for the loss) or `UNEXPLAINED LOSS` (exit 4: a durability alarm); both exclude the arm from
  replicates, with the reason recorded.
- **Broker stalls are timed against the window.** The NATS logs of the arm are scanned for `took too long`,
  `NO quorum` and leader changes; each episode is placed relative to the window start. One inside the window
  excludes the arm; one outside is only reported.
- **The sims are reused** (the same two tenants every run). Destroying a sim starts a tenant purge in every area, and
  its DELETEs of the whole previous run overlapped the next arm: the event-store statement rate
  rose 1,450 to 2,770 per second over five identical arms. `--fresh-sims` gives the old behaviour.
- **It waits until the databases have run no DELETE** for two looks before it starts.
- **It takes a CHECKPOINT** on both primaries when the load is steady, so the 5-minute timed one
  falls after the window. A checkpoint inside the window adds full-page images (+20 to +50 percent
  event-store WAL bytes); a window that holds one is reported with `wal_bytes_per_event_with_ckpt`
  and not compared. WAL **records** per event are the stable signal either way.
- **A base backup on a standby** (the scheduled one) burns 1 to 2 cores in the barman sidecar of a
  standby pod; that CPU is left out of the metrics and the run says so.
- **Rolling the pods moves the route message count by 15 to 20 percent** with an identical build and
  configuration (the broker connections and pod placement are fixed when a pod starts). An arm that
  changes the deployment is rolled by that change, so give every replicate `--roll` (it restarts the
  Deployments, not the broker or the databases) and compare against rolled baselines.

**Trust no delta before the noise is known.** Run the same arm at least three times, derive the
band, then compare. A run whose harness verdict failed (a PUBACKed event not persisted) is left out
of the replicates, loudly; `--include-failed` keeps it for an arm that is expected to fail one.

```bash
hack/perf-ab-compare.sh noise noise.json ab-out/roll-1 ab-out/roll-2 ab-out/roll-3
hack/perf-ab-compare.sh compare --noise noise.json ab-out/roll-1,ab-out/roll-2,ab-out/roll-3 ab-out/candidate
```

The band for a metric is the 99 percent prediction interval of one new run from the replicates
(floor 2 percent, 5 percent for CPU; `noise --confidence 95` for a tighter, leakier one). It is wide
with three replicates and narrows with more; a test arm averaged over several runs gets a tighter
band. Bands measured at 1,500 events/s (three replicates on unchanged pods, six on rolled pods):
client messages per event 5 to 14 percent, route messages and bytes 10 to 33, WAL records 2 to 4,
rows per statement 16 to 25, CPU 14 to 55 by component. Read a list of flagged metrics as a
signature (a cause and its consequences move together), not as a count; a change smaller than its
band needs more replicates, or the GKE round. JetStream API calls per event are shown but never
flagged (they are mostly timed calls, so per event they follow the rate).

`perf-ab.sh --self-test` and `perf-ab-compare.sh --self-test` check the arithmetic, the refusals
(a `pg_stat_statements` reset), and that identical arms flag nothing while a 24 percent change is
flagged.
