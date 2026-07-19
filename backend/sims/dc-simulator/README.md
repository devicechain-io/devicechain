# dc-simulator

The Go reference runner for the DeviceChain sim subsystem (see
`.agent-os/product/sim-subsystem-contract.md` and the slice-1 build spec,
`sim-slice1-devicepulse-spec.md` — maintainer-only, `.agent-os/` is a gitignored
symlink). It is an **untrusted external client** of the platform, not a
DeviceChain microservice: it holds no service token, touches no database, and
never calls `/admin/graphql` — it only ever authenticates as a scoped tenant
identity and drives the same tenant-facing surfaces a real integration would.

## What it does

1. Reads a **handshake** JSON file (written by `dcctl sim create`) describing a
   scoped sim identity, its tenant, and the platform's resolved endpoints.
2. Authenticates via `dc-microservice/userclient.TenantSession`
   (`login -> selectTenant`, auto-refreshed).
3. **Provisions** its manifest's topology through device-management's tenant
   GraphQL API: a device profile + metric definition, published; a device
   type; one device with an `externalId` (ADR-049) and an `ACCESS_TOKEN`
   credential (ADR-014). Every step is create-or-ignore-if-exists, so bootstrap
   (and `reset`, which just re-runs it) is idempotent.
4. **Emits** measurements over the real device-plane HTTP ingress
   (`POST /{instanceId}/{tenant}/events`), authenticated by the provisioned
   credential — exactly the path a physical device uses. The cadence and
   population default to the scenario's own demo sizing (~5s) and are
   overridable — see [Driving load](#driving-load).
5. Serves a small **control API** (`GET /status`, `POST /start`, `POST /stop`,
   `POST /reset`) and a **presentation page** (`web/index.html`, embedded in
   the binary) that subscribes to event-management's `measurementStream` over
   `graphql-ws` and lists live measurements — read-back of *resolved* platform
   truth, not emitted intent.

## Run

```sh
go run . --handshake /path/to/handshake.json [--port 8090]
# or: DC_SIM_HANDSHAKE=/path/to/handshake.json DC_SIM_PORT=8090 go run .
```

### Driving load

The built-in scenarios are sized as **demos** — devicepulse is 1 device and
buildingpulse is 12, both at a 5s cadence (2.4 events/sec), which is the right
default for watching the presentation page and far too small to measure what an
instance costs. Three flags override that; each also reads an env var, and each
defaults to the scenario's own sizing:

| Flag | Env | Purpose |
|------|-----|---------|
| `--devices <n>` | `DC_SIM_DEVICES` | Population size (`0` keeps the scenario's own). |
| `--emit-interval <d>` | `DC_SIM_EMIT_INTERVAL` | Cadence, e.g. `200ms` (`0` keeps 5s). |
| `--concurrency <n>` | `DC_SIM_CONCURRENCY` | Max emits in flight per tick (`0` derives it). |

```sh
# 500 devices every 200ms = a 2500 events/sec target
go run . --handshake ./hs.json --devices 500 --emit-interval 200ms
```

**Compare `achievedRatePerSec` against `targetRatePerSec`.** A target rate is a
request; whether the sim reached it depends on emit latency, ingress
backpressure, and per-tenant rate limiting (ADR-023). `GET /status` reports both,
so a run can be believed rather than assumed:

```json
{
  "deviceCount": 500, "emitIntervalMs": 200, "targetRatePerSec": 2500,
  "stats": { "emitted": 74812, "failed": 0, "overruns": 0, "ticks": 150,
             "elapsedSeconds": 30.1, "achievedRatePerSec": 2485.4 }
}
```

**Triage in this order.** The order matters, because two of these counters have
blind spots and the first one does not:

1. **`achievedRatePerSec` vs `targetRatePerSec`** — the shortfall, and the only
   figure that carries its magnitude. Always start here.
2. **`failed`** — nonzero means the ingress *rejected* load (per-tenant rate
   limiting, ADR-023, or backpressure). Rejected load is load the platform never
   carried, so it is excluded from the achieved rate.
3. **`overruns`** — nonzero means the ingress was *slow*: ticks were still
   emitting when the next fired, so the rate is bounded by emit latency rather
   than by the interval asked for. Lower the device count, lengthen the
   interval, or raise `--concurrency`.
4. **`ticks`** — the sample size behind the rate. See the caveat below.

Do not start at `overruns`. It detects a slow ingress and is **structurally
blind to a fast-rejecting one** — a 429 makes a tick *shorter*, not longer.
Measured against a 10%-accept ingress: the sim applied a tenth of its target
with `overruns` at exactly 0 for the whole run, while `failed` and the achieved
rate both told the truth. It is also an incidence count, not a magnitude: a tick
running k intervals long drops roughly k−1 ticks for every 1 it counts.

**Give the rate enough ticks to mean anything.** It is discretized — after k
ticks the numerator is k×devices while the denominator is wall-clock — so a run
only a few ticks long reports a sawtooth well below the true rate. At a 5s
cadence sampled over 10s that reads **50% low**; at 200ms over 10s it is within
2%. Use a short interval and let the run go, and check `ticks` before believing
the rate.

**Concurrency ceilings the rate.** Emits are bounded to `--concurrency` in
flight (derived default: the device count, capped at 64), so the ceiling is
roughly `concurrency / per-emit-latency`. Measured against a local fake ingress:
~28,600/s at 1ms, ~10,100/s at 5ms, ~2,870/s at 20ms. The 500-device / 200ms
example above sits comfortably under that at 1–5ms, and within ~13% of it at
20ms — raise `--concurrency` if the ingress is slower than that. Over-driven,
the rate pins to the ceiling and holds rather than collapsing.

Two more things to expect at scale: bootstrap provisions each device through the
tenant GraphQL API **serially**, so a large `--devices` takes a while on first
run (it is create-or-ignore, so a re-run is fast), and the counters describe the
**current** run — `POST /start` resets them, and `POST /stop` freezes the
elapsed window so a stopped run's rate stays what it achieved.

The handshake shape (written by `dcctl sim create`, read by `sim.LoadHandshake`):

```json
{
  "tenant": "acme",
  "simEmail": "sim-acme@devicechain.local",
  "simPassword": "...",
  "endpoints": {
    "userGraphQL": "http://localhost/api/user-management/graphql",
    "deviceMgmtGraphQL": "http://localhost/api/device-management/graphql",
    "ingress": "http://localhost:8081",
    "eventMgmtWS": "ws://localhost/api/event-management/graphql"
  },
  "manifestId": "devicepulse",
  "seed": 1,
  "instanceId": "dc"
}
```

On startup the process bootstraps its manifest, starts emitting immediately,
and serves the control API + presentation page on `--port` (default `8090`).
Open `http://localhost:8090/` to watch measurements arrive live.

## Package layout

- `sim/manifest.go` — `SimManifest` / `ProfileSpec` / `DeviceTypeSpec` /
  `PopulationSpec` / `DeviceInstance`, and `Expand(seed)` — deterministic,
  pattern-driven device generation (no unseeded randomness anywhere).
- `sim/sim.go` — the `Sim` interface (`Manifest`/`Bootstrap`/`Tick`) and the
  `devicepulse` reference scenario. **This interface is the headless reference
  driver, not the wire contract** — a future Unity (or any other) sim
  implements the wire seams below directly.
- `sim/runtime.go` — `Runtime`: the authenticated session, resolved endpoints,
  and the devices Bootstrap provisioned.
- `sim/handshake.go` — the `Handshake` wire struct shared with `dcctl sim`.
- `sim/bootstrap.go` — the idempotent provisioning chain (raw GraphQL over
  `TenantSession.Query`).
- `sim/emit.go` — builds a `dc-event-sources/processor.JsonEvent` and POSTs it
  to the device-plane ingress.
- `sim/lifecycle.go` — the `CREATED -> BOOTSTRAPPED -> RUNNING <-> STOPPED`
  FSM and the control HTTP API.
- `sim/presentation.go` — serves the static page + its `/config.json`.
- `web/index.html` — the presentation page (plain HTML/JS, no build step).

## Wire contract, not this Go interface

Everything this module does is expressible as four language-agnostic wire
seams (device-plane ingress, tenant GraphQL provisioning, the control HTTP
API, and `graphql-ws` subscribe) — see the contract doc. A sim written in any
other language/engine (e.g. Unity/C#) talks to the same platform surfaces
without ever depending on this Go module.
