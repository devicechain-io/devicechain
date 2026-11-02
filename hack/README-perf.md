# Profiling a performance round

Maintainer notes for the ingest-profiling rounds (spec: ingest performance, profile first).
Everything here is **run-only**: every switch defaults OFF, none is reachable through a
Service, Ingress or Gateway, and a switch is turned off again after the run.

| What | Switch | Default | Where | Restarts |
|---|---|---|---|---|
| Go profiles of one service | `functionalAreas.<area>.profiler.enabled` | off | Helm values | that area's pods |
| Block / mutex profiles | `functionalAreas.<area>.profiler.blockRate` (ns per sampled event; 1 = all), `.mutexFraction` (1 in N) | 0 (off, the endpoints answer 404) | Helm values, need `enabled` | that area's pods |
| NATS server pprof | OpenTofu `nats_profiling_port` (instance root; module `profiling_port`) | 0 (off) | `deploy/opentofu` | broker, one server at a time |
| NATS block profile | `nats_profiling_block_rate` (needs the port) | 0 | `deploy/opentofu` | broker |
| `pg_stat_statements`, relational DB | OpenTofu `postgres_statement_stats` (cluster root) | false | `deploy/opentofu` | Postgres (rolling) |
| `pg_stat_statements`, event store | OpenTofu `timescale_statement_stats` (instance root) | false | `deploy/opentofu` | Postgres (rolling) |

The NATS monitoring endpoints (`/varz`, `/jsz`, `/connz` on the `monitor` port, 8222) are on in
the broker chart already and need no switch. The collector reaches them, and every profile
port, with `kubectl port-forward` to a pod.

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
