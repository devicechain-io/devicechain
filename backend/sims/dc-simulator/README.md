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
5. **Receives** commands, for a scenario whose manifest declares a **command far
   end** (widgetlab). Telemetry over HTTP ingress is one-way, so a scenario whose
   dashboard offers a command-button otherwise enqueues real commands nothing
   answers: they reach `SENT` and expire days later, with every layer reporting
   success. Such a scenario subscribes every device to its own command topic on
   the MQTT gateway and answers what arrives, completing `QUEUED -> SENT ->
   SUCCESSFUL`. See [The command far end](#the-command-far-end).
6. Serves a small **control API** (`GET /status`, `POST /start`, `POST /stop`,
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

The built-in scenarios are sized as **demos** — devicepulse is 1 device,
buildingpulse is 12 and widgetlab is 7, all at a 5s cadence, which is the right
default for watching the presentation page and far too small to measure what an
instance costs. Three flags override that; each also reads an env var, and each
defaults to the scenario's own sizing:

| Flag | Env | Purpose |
|------|-----|---------|
| `--devices <n>` | `DC_SIM_DEVICES` | Population size (`0` keeps the scenario's own). |
| `--emit-interval <d>` | `DC_SIM_EMIT_INTERVAL` | Cadence, e.g. `200ms` (`0` keeps 5s). |
| `--concurrency <n>` | `DC_SIM_CONCURRENCY` | Max emits in flight per tick (`0` derives it). |

**`--devices` does not apply to every scenario.** A scenario whose dashboards bind
named devices is a composed fixture, not a scale vehicle: resizing it would leave
its boards pointing at devices the run never provisions, so it refuses the flag at
startup rather than provisioning a topology its own boards do not match. widgetlab
is one. devicepulse and buildingpulse take any size.

```sh
# 500 devices every 200ms = a 2500 events/sec target
go run . --handshake ./hs.json --devices 500 --emit-interval 200ms
```

### Detection rules are proven live, not assumed

A scenario that declares detection rules does not treat a clean publish as evidence
they run. device-management compiles a profile's enabled rules at publish and fails
closed — but **only when a validator is wired**; with the service secret unset the
check returns nil having validated nothing. On such an instance a rule the compiler
rejects publishes cleanly, event-processing drops it at load with a log line nobody
reads, and the scenario's alarm widgets are permanently empty while every step
reports success.

So bootstrap post-asserts against event-processing's own `ruleHealth`, which reads
the engine's live rule set. That catches a rule absent from the engine (dropped at
load) and one reported `COMPILE_ERROR` — the latter with the engine's own diagnostic,
the former with the sim's inference, since a rule that never arrived generates no
diagnostic to quote.

⚠️ **The check is per VERSION, and that is the whole subtlety.** `ruleHealth` answers
from a projection an async consumer writes, so for a window after a publish it still
serves the *previous* version — whose rows carry the *same* rule tokens (a token is a
stable authoring id that survives every definition change), all `ACTIVE`. Matching on
token alone therefore passes on the first read and confirms a version the engine has
not loaded, which is worst precisely on "ship a corrected rule and re-bootstrap" — the
flow where the new rule is most likely to be the broken one. A rule id embeds
`{profileToken}@{version}`, so rows from any other version count as *not settled yet*
rather than as an answer, and the loop keeps polling. Separately, a post-assert
against device-management confirms its **active** version is the one just published.

This needs `endpoints.eventProcessingGraphQL` in the handshake. A scenario with
enabled rules and no such endpoint **fails** rather than skipping the check — a
verification that quietly does nothing reports the same green as a real pass. A
profile whose rules are all disabled needs neither, since a disabled rule is inert by
design.

⚠️ **The chart's `telemetry` and `ingest-only` profiles do not deploy
event-processing,** so **buildingpulse and widgetlab cannot run on them** — their alarm
path has no engine behind it, and there is nothing to opt out of (unlike the command far
end, where the gateway may simply be unreachable from the host while the platform is
fine). An opt-out flag would buy a green bootstrap over an alarm table that stays empty,
which is the failure this check exists to make visible. The timeout message names the
deployment profile as the first thing to check, because the symptom otherwise reads as a
bug in the sim. Transient failures are retried inside the settle window, so a rolling
restart of event-processing does not fail a bootstrap. devicepulse declares no rules and
runs anywhere.

Only the declared rules must be live; extra rules are ignored. A tenant may author its
own on the same profile from the console, and refusing to bootstrap over one would
make the sim hostile to the instance it runs on.

Relatedly, the publish decision reads the **active version's** label rather than the
set of all labels ever published. A marker sitting in a superseded version would
otherwise answer "already published" forever, so a board edited and republished from
the console would leave every later bootstrap skipping the publish and reporting
success over content the platform is not serving.

### Telling a governed device from a quiet one

`GET /status` reports `stats.lastTickShed` beside the cumulative `stats.shed`, and
the runner logs a warning the first tick that sheds (and an info line when it stops).

A shed is not an emit failure — the ingress refuses it cleanly at the per-tenant rate
ceiling, and a governed load run expects them — so the emit loop deliberately does not
treat one as an error. But on a scenario being *watched* rather than measured, that
silence is the problem: a device whose events are being refused looks exactly like a
device that has nothing to say, and on a board built to show what a widget does with
awkward data, those are the two readings that must not be confused. The cumulative
counter cannot separate them either, since a total that grew an hour ago reads the
same as one growing now. Only the runner does this; the load harness drives the emit
loop directly and is unaffected.

### The command far end

A device that only POSTs telemetry cannot receive anything, so a scenario with a
control widget on its board needs a second connection: every device subscribed to
`{instance}/{tenant}/device-commands/{token}` on the NATS MQTT gateway, answering
on `{instance}/{tenant}/command-responses` (the `cmdreceiver` package, shared with
the load-test command harness). A scenario opts in with `CommandFarEnd` on its
manifest; widgetlab does, devicepulse and buildingpulse do not.

`dcctl sim create` resolves the gateway address into the handshake
(`endpoints.mqttBroker`, default `ssl://<server>:1883` — the broker terminates TLS
independently of the HTTP ingress) and records whether to verify its certificate
(`mqttTLSInsecure`, on by default without `--tls`, since a local bring-up's gateway
cert is self-signed). Override either with `--mqtt-broker` / `--mqtt-insecure`.

**Bootstrap FAILS if a declared far end cannot be brought up** — no broker
configured, or any device that does not come back subscribed. Degrading to "run
without it" is the failure the seam exists to remove: the scenario would come up
green and its Send button would enqueue commands that expire unanswered. On a host
that genuinely cannot reach the gateway, `--no-command-far-end` accepts that
knowingly; it logs a warning at startup and `GET /status` reports the channel as
`declared` but `disabled`, alongside per-device receive/respond evidence when one
is attached.

⚠️ **Run one process per handshake.** An MQTT session is keyed by client id, which
is derived from `(tenant, deviceToken)` — both fixed by the handshake — so two
`dc-simulator` processes on the same record (say, a second one on another port) take
each other's sessions over in a loop. Nothing prevents the second launch; the symptom
is a reconnect storm and commands ping-ponging between the two receivers.

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
