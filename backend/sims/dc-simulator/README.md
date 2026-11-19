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
   credential (ADR-014), and, for a scenario that declares them, geofences
   (sitepulse's pit). Every step checks by token first and creates what is
   missing; profiles and geofences also converge to the manifest's content, so
   bootstrap (and `reset`, which just re-runs it) is idempotent.
4. **Emits** measurements — and, for a scenario that reports position,
   **locations** — over the real device-plane HTTP ingress
   (`POST /{instanceId}/{tenant}/events`), authenticated by the provisioned
   credential — exactly the path a physical device uses. The cadence and
   population default to the scenario's own demo sizing (~5s) and are
   overridable — see [Driving load](#driving-load).
5. **Receives** commands, for a scenario whose manifest declares a **command far
   end** (widgetlab, towerpulse, harborpulse and fleetpulse; sitepulse declares one too, `external`, and attaches no receiver). Telemetry over HTTP ingress is one-way, so a scenario whose
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
buildingpulse is 12, widgetlab is 7, fleetpulse is 6, towerpulse is 31, harborpulse is 37 and sitepulse is 19 (18 machines and one
plant; its devices publish their own telemetry, so the sim itself emits nothing), all at a 5s cadence, which is
the right default for watching the presentation page and far too small to measure what
an instance costs. Three flags override that; each also reads an env var, and each
defaults to the scenario's own sizing:

| Flag | Env | Purpose |
|------|-----|---------|
| `--devices <n>` | `DC_SIM_DEVICES` | Population size (`0` keeps the scenario's own). |
| `--emit-interval <d>` | `DC_SIM_EMIT_INTERVAL` | Cadence, e.g. `200ms` (`0` keeps 5s). |
| `--concurrency <n>` | `DC_SIM_CONCURRENCY` | Max emits in flight per tick (`0` derives it). |

**`--devices` does not apply to every scenario.** A scenario whose dashboards bind
devices chosen in source for their behaviour is a composed fixture, not a scale vehicle:
resizing it would leave its boards pointing at devices the run never provisions, so it
refuses the flag at startup rather than provisioning a topology its own boards do not
match. widgetlab, towerpulse and harborpulse are. devicepulse, buildingpulse and fleetpulse take any size
(buildingpulse's board binds named devices too, but it is rebuilt from the resized
population). sitepulse takes any machine count: `--devices n` gives each of its three
machine kinds `n` devices and leaves its single crusher plant at one. Its board
(`sp-dashboard`) shows one fuel card per machine, up to six per kind, and says so when
it is showing fewer than the fleet; it is the 18-machine demo's board, and its site-wide
alarm widgets query once per member device and read at most 500 members, so a very large
`--devices` run makes it both expensive and partial.

⚠️ **A small buildingpulse is a quieter demo, not a broken one.** Every device raises and
clears at any size, but below a handful the tenant-wide view has moments with no active
alarm, and below one-per-building a board scoped to an empty building shows nothing at
all. Bootstrap warns on both, with the size it computed — no number is written down here,
because the curve's constants decide it.

```sh
# 500 devices every 200ms = a 2500 events/sec target
go run . --handshake ./hs.json --devices 500 --emit-interval 200ms
```

### Load-testing over MQTT (`dc-loadtest-mqtt`)

The HTTP load test (`cmd/loadtest`) cannot see the loss that matters most on the MQTT path: a
QoS 1 publish the broker **PUBACKed** is a promise to the device that the event is stored. Rounds
13 to 15 found that promise broken (a capture-stream leader step-down dropped 150 to 560 PUBACKed
events at a time, silently), using a throwaway driver. `cmd/loadtest-mqtt` is that driver in the
repository: it provisions the same fleet with the same credentials as `dc-loadtest`, holds **one
MQTT session per device** on the platform's MQTT gateway, publishes at a target rate for a set
duration, waits for the platform to persist, and compares.

```sh
go build -o dc-loadtest-mqtt ./cmd/loadtest-mqtt

# 3,000 devices, 6,000 events/s, QoS 1 over TLS, 10 minutes
./dc-loadtest-mqtt --handshake ~/.devicechain/sims/perf/handshake.json \
    --devices 3000 --rate 6000 --duration 10m --report mqtt.json

# the 60 minute rung: read all ~21M events back, so the loss is exact (~22k queries)
./dc-loadtest-mqtt --handshake ... --devices 3000 --rate 6000 --duration 60m \
    --identity full --quiesce-timeout 10m --report mqtt.json
```

**The verdict.** Exit `0`: every invariant held. Exit `1`: an invariant failed. Exit `2`: the run
could not be carried out and has no verdict (bad flags, unreachable platform or broker, aborted).
Exit `3`: **LOSS**, PUBACKed events are missing from the store; the report's `storage.lost` is the
number and `loss` is `true`. A PUBACK is counted only when the session store saw it
(`core/test/mqttack`), never because paho completed a publish token: a persistent session that
reconnects completes the old token with no error and no PUBACK. A publish that ends with no PUBACK
(timeout, lost connection) is *unconfirmed*: the device would resend it, it may or may not be
stored, and it is never counted as a loss. A persistent session's re-sent publishes are counted
(`resent`) too: either kind can hide a loss from a count (see the identity modes). A count still
*rising* when `--quiesce-timeout` ends is a drain, not a loss: the run fails with the shortfall in
`storage.undrained` and exit `1`, never `3`.

| Invariant | Holds when |
|-----------|-----------|
| `fleet-connected` | every device connected at least once |
| `transport-as-requested` | the sockets observed are the transport requested: all TLS with `--tls` (the default), all plaintext with `--no-tls` |
| `load-applied` / `load-offered` | the broker acknowledged at least `--min-accepted` events, and the run both offered and had acknowledged at least `--min-offered-ratio` (0.95) of `rate x duration` |
| `puback-durability` (QoS 1) | every PUBACKed event is persisted; none persisted beyond what could have been stored; with `--identity count`, nothing was unconfirmed or re-sent (`maskedLossBound` is 0) |
| `qos0-delivery` (QoS 0) | the missing share of written events is within `--qos0-tolerance` (default 0: QoS 0 promises no delivery, so this is a measurement) |
| `ingest-identity` | with `--identity full`: every PUBACKed event was stored exactly once, by (device, occurredTime) |
| `ack-latency` | with `--max-ack-p99`: the PUBACK p99 is within the ceiling |

**Identity modes.** `--identity full` reads every event back and names the exact loss; it costs
one GraphQL query per 1,000 events. `--identity count` compares the windowed persisted count with
the PUBACK count in one query, and can be fooled: an unconfirmed publish that *was* stored takes
the place of a PUBACKed one that was not, and so does a re-sent one stored twice. The report
states how far (`maskedLossBound`, unconfirmed plus re-sent), and a count with a bound above zero
reaches **no durability verdict**: `puback-durability` fails as inconclusive (exit `1`), because a
count that cannot exclude a loss must not pass as having excluded one. Even at a bound of zero a
count cannot see a duplicate the platform stored on its own offsetting a loss; only `full` can.
`auto` (the default) reads everything back up to `--identity-max-events` (2,000,000) PUBACKed
events and counts above that. The release-candidate run uses `full`; raise `--quiesce-timeout`
if the run builds a backlog.

**What the flags mean.**

- `--devices` and connections are the same number. The broker confines a device credential to its
  own client id and its own topic, so devices cannot share a connection. `--connect-concurrency`
  ramps the fleet (and spreads a reconnect storm) instead.
- `--rate` is the total across the fleet, open loop: every device ticks on its own schedule
  whatever the broker is doing. A tick that finds its session down, or `--max-inflight` (64)
  publishes already waiting, is counted (`notSentDisconnected`, `skippedInflight`) and dropped.
- `--pad-bytes` adds an inert padding member to each event for a larger payload; the decoder
  ignores it and it adds no stored row.
- `--clean-session=false` keeps the session, and its unacknowledged publishes, across reconnects.
  The default is a clean session, as rounds 12 to 15 ran.
- **TLS is on by default, and the flag is truthful.** `--tls=false` (or `--no-tls`) dials plaintext
  and `--tls` dials TLS; the harness opens the sockets itself and the report records what it
  observed (`tlsConnectionsObserved`, `plaintextConnectionsObserved`, negotiated versions). Round
  12 found that `dcctl bootstrap --no-tls` turned off HTTPS on the ingress and left the broker's
  TLS on, so two rounds ran with a flag that did not mean what it said. If the broker disagrees with the
  flag, the connect fails; it is never silently upgraded. The handshake's `mqttTLSInsecure` skips
  certificate verification (a local bring-up's self-signed gateway); `--tls-insecure=false` forces
  verification.
- **Reconnects** back off with jitter (`--backoff-base` 500ms doubling to `--backoff-max` 30s) and
  are bounded by `--connect-concurrency`, so a broker restart does not bring 3,000 devices back in
  the same instant. Losses, reconnects and connect failures are in the report.
- `--report-interval` (10s) logs a progress line and records a slice (sent, PUBACKed, unconfirmed,
  sessions up, in flight, p99) so a step-down shows as a gap in a 60 minute run.

**With the DETECT oracle.** `dc-loadtest-detection --transport mqtt` carries the background fleet
*and* the planted probes over MQTT (one session per device, a probe emit accepted only on its
PUBACK), so the detection and alarm path is judged while the broker is under load. Every harness
needs a tenant to itself, so the release-candidate run is three handshakes on three tenants:
`hack/loadtest-rc-run.sh` starts the HTTP rung, the MQTT rung and the detection oracle together
for one duration (60 minutes by default) and exits non-zero if any of them did.

- **Detection lag is gated, not just the end state.** The oracle reads the edge probes' durable
  alarms throughout the drive and the drain, and holds every cycle to a deadline: the alarm's
  `clearedTime` (the occurred-time the harness stamped on the event) must reach the emit time of
  each falling edge within `--lag-bound` (30s). A pipeline that falls behind and later catches up
  fails the `detection-lag` invariant, which `alarm-liveness` alone cannot see (it reads an end
  state the early cycles had already produced). The report carries the per-cycle lag percentiles.
- **The live detection signal survives long runs.** Its socket is replaced with a fresh access token
  every five minutes, so a run longer than a token's life no longer goes without it.
- **A stopped rung exits 143 (SIGINT: 130), never 1,** so the run script's summary tells a rung it
  stopped from one that failed. Every count read-back is retried with a bounded backoff
  (5 attempts, 5s doubling to 60s) before a run is reported as having no verdict.

**Scope.** Telemetry only: the broker's command and presence planes are covered by
`dc-loadtest-command` and `dc-loadtest-presence`. Brokers are dialled over `tcp://` or `ssl://`
(a websocket address is refused, since the harness could not observe what it connected over).
The per-event identity ledger lives in memory for the length of the run (`--identity count` still
keeps it, since `auto` decides after the drive; a 60 minute run at 6,000/s holds about 21M
entries, a few hundred MB).

### Rehearsing a broker that is not DeviceChain (`--target`)

The loss verdict does not need a DeviceChain platform on the other end. `dc-loadtest-mqtt --target`
aims the same open-loop, PUBACK-true fleet at any MQTT broker, with a credential list you bring,
and reads delivery back through a **subscriber tap** on that broker. A QoS 1 publish the broker
PUBACKed is a promise; a PUBACKed message the tap never receives is a **LOSS**, with the same exit
codes (0 held, 1 an invariant failed, 2 no verdict, 3 loss) and the same split between *lost* (the
broker promised) and *unconfirmed* (it never did).

```bash
go build -o dc-loadtest-mqtt ./cmd/loadtest-mqtt
./dc-loadtest-mqtt --target mqtts://broker.example:8883 --credentials fleet.csv \
    --rate 500 --duration 5m --identity full \
    --tap-username rehearsal-tap --tap-password-file tap.pass --report report.json
```

`--target` takes `mqtt://host:port` or `mqtts://host:port`; the scheme decides TLS (a contradicting
`--tls` is refused). A `ws(s)://` or `https://` target is **refused by name**, not approximated:
this build drives and observes only TCP and TLS sockets. There is no handshake and no platform.

**The credential CSV** has a header row and one row per device; the file *is* the fleet.

| Column | Meaning |
| --- | --- |
| `device` | required; the identity events are filed and read back under |
| `client_id` | MQTT client id (default: the device); unique, since a broker evicts a duplicate |
| `token` | a bearer token, presented as the username with an empty password |
| `username`, `password` | the other password-style option (not together with `token`) |
| `cert_file`, `key_file` | PEM client certificate and key, for an `mqtts://` target (the pair `x509sim` writes) |
| `topic` | this device's publish topic (default: `--topic-template`, `{device}` replaced) |

A row with none of these connects anonymously, and the report counts devices by how they
authenticated, never by what they presented. A malformed list is refused whole, naming the line.

**The tap** subscribes before anything publishes, at QoS 1, to `--topic-template` with `{device}`
as a single-level wildcard (override with `--tap-topic`), and files each delivery under
`(device, occurredTime in microseconds)` from the message body. Redeliveries are counted once, in
the report. If the tap's own session drops, a shortfall is reported as inconclusive (exit 1), never
as a loss: the tap may be the one that missed it. Use a topic prefix the run owns: other traffic
that matches the filter is unexpected events and fails the run.

**What the verdict is, and is not.** It measures *broker-to-subscriber* loss: PUBACKed, then
delivered to one QoS 1 subscriber of the broker you named. It does not see what any pipeline
behind the broker does with a message afterward, and a subscriber slower than the broker's
per-client queue allows reads as the broker's loss, because from the publisher's side it is one
(raise the queue and in-flight limits on a broker under rehearsal). The report carries this
statement. Against a DeviceChain platform the stronger `--handshake` mode reads the event store
itself.

**Latency regression.** `--baseline report.json --max-p99-regression 0.25` fails the run when the
PUBACK p99 grows by more than 25% of an earlier *passing* report's.

**The negative control.** `dc-loadtest-dropproxy --listen 127.0.0.1:1884 --upstream broker:1883
--every 100` is a plaintext MQTT proxy that PUBACKs every 100th QoS 1 publish and never forwards
it. Publish through it (`--target mqtt://127.0.0.1:1884`) and tap the real broker
(`--tap-target mqtt://broker:1883`): the run must exit 3 and its `lost` count must equal the
proxy's `dropped` exactly. A gate that has only ever passed may be unable to fail; this is how you
see that it can. `hack/rehearsal-rig.sh` runs both halves against an off-the-shelf authenticated
Mosquitto in its own kind cluster, and `.github/actions/rehearsal` wraps the tool as a composite
action that fails a job on a loss or a latency regression.

### The aggregate identity oracle (`dc-loadtest-aggoracle`)

The per-device read-back (`--identity full`) pages every stored event back through the tenant API:
about 90M events at 100k/s for fifteen minutes, hours of reads that load the shard under test. The
aggregate oracle asks the event store's Postgres for one grouped result per tenant instead:

```
per device:  ( count,  sum over rows of  hash(occurred_time in microseconds)  mod 2^64 )
```

computed in SQL on the shard and compared with the same pair the harness computed from what it sent.
It catches loss (both fall), duplication (both rise) and substitution (the count agrees, the hash sum
does not: `A,A,C,D` stored for `A,B,C,D`), which is what the equal-totals count cannot see. The hash is
the first eight bytes of SHA-256 of the instant, so regular timestamps do not sum to colliding totals;
a loss is missed only if a different row set hashes to the same 64-bit total.

```bash
# the harnesses write their expected aggregates (a few KB per device thousand, not per event)
./dc-loadtest-mqtt --handshake ... --rate 6000 --duration 15m --aggregate-ledger run-a.json
# anywhere the event store's Postgres is reachable (a port-forward); one --ledger per harness
DC_EVENTSTORE_DSN=postgres://... ./dc-loadtest-aggoracle verify --ledger run-a.json --ledger run-b.json
```

A device whose pair is neither its accepted events nor its accepted plus all ambiguous ones is read
exactly and judged by identity with multiplicity, so the report names the missing and the duplicated
instants. A moving store (rows still arriving) is inconclusive, not clean: `--settle` reads twice.
The command needs direct, read-only access to the event store; the harness itself still has none.

**The oracle is proven before it is trusted.** `dc-loadtest-aggoracle rig --scratch` seeds a scratch
database (10M rows by default), then perturbs it one row at a time beneath the API, **red stages first**:
one row deleted (must be red, naming the row); a row on another device duplicated so every total is
equal again (must be red, naming both, while a grand-total count comparison stays green); both put
back (must be green); one row's instant moved by a microsecond so no device count changes (must be red
while a per-device count comparison stays green); put back (must be green). The comparators that stay
green are the mutants the oracle must beat. It also checks the SQL hash against the Go one, negative
instants included.

### Many harnesses, one measurement (`dc-loadtest-multi`)

One harness is one tenant from one process; a single pod cannot offer 100k. A fleet is N harnesses on
as many pods, each with its own tenant and ledger. Give every one the same `--start-at` (an RFC3339
instant, held after provisioning and, for MQTT, after the fleet connected) and merge their reports:

```bash
./dc-loadtest-multi merge --expect 6 harness-*.json        # pods elsewhere; reads dc-loadtest and dc-loadtest-mqtt reports
./dc-loadtest-multi run --handshake a.json --handshake b.json ... --lead 3m --hold 10m   # N HTTP harnesses from one process
```

The merged run passes only if every harness passed (`harnesses-passed`), no tenant is driven twice
(`tenants-distinct`: two harnesses on one tenant would each count the other's events as unexpected),
the windows overlap (`windows-aligned`: start spread within `--align-tolerance`, and all harnesses
driving together for `--min-overlap` of the mean hold, since a summed rate is only a rate of one
window), and, with `--expect`, no harness failed to write a report (`harness-count`). Exit `3` if any
harness lost PUBACKed events.

### Location events

`fleetpulse` is the scenario that reports position: a small fleet driving a closed
loop, emitting a **Location** and a **Measurement** per device per tick as two separate
events. It exists because the platform's location spine — decoder, resolver,
hypertable, GraphQL — had no in-repo producer at all, and a wire shape nothing emits is
a wire shape nobody has had to be right about.

The contract the emitter holds to, all of it enforced at decode rather than shrugged
off:

- content lives under `payload.entries[]`; a payload with no entries is a decode error,
  not an empty success;
- **every value is a JSON string**, including the numbers — a bare `33.749` fails the
  whole decode;
- `latitude` and `longitude` are required per entry, the rest optional, and an
  unreported optional is **absent** rather than `"0"` (a device with no compass has not
  reported due north);
- units are fixed platform-wide: WGS84 decimal degrees, elevation above the ellipsoid
  and accuracy in **metres**, speed in **metres per second**, heading in degrees
  clockwise from true north in `[0, 360)`;
- **one entry per event** — the per-entry time is discarded at persistence and
  identical positions in one batch collide, so batching loses data.

Fleet Pulse also answers one typed command, `set-speed-limit`, with a required `limit_mps`
parameter (a number from 5 to 30). The vehicle applies the limit, then answers `SUCCESSFUL`,
and from its next reading on never reports a speed above it (the location fix and the
`speed_mps` metric stay one value). The 5 to 30 range is enforced by the platform when the
command is queued, against the published profile; the vehicle does not range-check, it only
answers `FAILED` to a command it cannot act on. The limit lives in the process, so a restart
clears it. A run that never sends the command behaves exactly as before. Because the scenario
now declares a command far end, its handshake must carry `endpoints.mqttBroker` (a current
`dcctl sim create` writes it), or the sim is started with `--no-command-far-end`.

The emitter's tests run the exact bytes it produces through event-sources' own JSON
decoder, so a change to that contract fails here rather than against a live cluster.

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
bug in the sim. devicepulse declares no rules and runs anywhere.

⚠️ **Only the post-publish check retries; the publish itself does not.** `publishDeviceProfile`
validates rules synchronously and **fails closed on a transport error**, so a bootstrap
started while event-processing is mid-rollout (the chart deploys it `Recreate`, so it is
briefly absent) fails there rather than in the settle window. Re-running heals it: the
version's content marker is stamped only on a publish that succeeded.

Only the declared rules must be live; extra rules are ignored. A tenant may author its
own on the same profile from the console, and refusing to bootstrap over one would
make the sim hostile to the instance it runs on.

Relatedly, the publish decision reads the **active version's** label rather than the
set of all labels ever published. A marker sitting in a superseded version would
otherwise answer "already published" forever, so a board edited and republished from
the console would leave every later bootstrap skipping the publish and reporting
success over content the platform is not serving.

### Telling a governed device from a quiet one

`GET /status` reports `stats.lastTickShed` beside the cumulative `stats.shed` (429s
at the tenant's rate ceiling) and `stats.backpressured` (503s with a `Retry-After`
from the platform's backpressure gate); `lastTickShed` counts both. The runner logs a
warning the first tick that is refused, with that tick's count of both under `refused`
(and an info line when it stops).

A refusal is not an emit failure — the ingress refuses cleanly, at the per-tenant rate
ceiling or under backpressure, and a governed load run expects them — so the emit loop deliberately does not
treat one as an error. But on a scenario being *watched* rather than measured, that
silence is the problem: a device whose events are being refused looks exactly like a
device that has nothing to say, and on a board built to show what a widget does with
awkward data, those are the two readings that must not be confused. The cumulative
counter cannot separate them either, since a total that grew an hour ago reads the
same as one growing now. Only the runner does this; the load harness drives the emit
loop directly and is unaffected.

### Towerpulse (building management)

`towerpulse` is `buildingpulse` grown into the model of the second showcase: an office
campus run by a facility-services tenant, a five-floor tower and a two-floor lab annex,
31 devices in all. It is the **headless backbone**: everything the platform side of the
story needs, with none of the scene. (`buildingpulse` keeps its id and its small board;
`towerpulse` is the name the showcase uses.)

🔴 **Every device is an MQTT-native controller (a connected retrofit).** The platform has
no BACnet, Modbus or OPC-UA support, so this scenario must never be described as
integrating with a building management system.

| Devices | Count | Metrics | Commands |
| --- | --- | --- | --- |
| `tp-vav-zone` (VAV zone controller) | 24 (20 tower, 4 annex) | `temp_c`, `setpoint_c`, `co2_ppm`, `occupancy`, `damper_pct` | `boost-cooling{minutes}`, `set-setpoint{celsius}` |
| `tp-ahu` (air handler) | 3 | `supply_air_c`, `fan_pct`, `filter_dp_pa`, `mode` (0 normal, 1 economy, 2 boost) | `set-mode{mode}` |
| `tp-chiller` | 2 | `chw_supply_c`, `power_kw`, `cooling_kw`, `running`, and the calculated `cop` | `restart` |
| `tp-energy-meter` | 2 | `kw`, `kwh`, and the calculated `kwh_per_m2` | none |

**Hierarchy.** Two customers (the tower's owner and the annex's owner), a building area for
each, a floor area per floor and a plant area per building. Every device is assigned to its
floor (or plant), its building and its owner, so every event carries a building and a customer
anchor. Plants have their own area type (`tp-plant`), so a Floor picker limited to the `tp-floor`
type does not offer "Annex plant". The platform has no area-in-area hierarchy, so the floor-to-building link is carried by
the device's two assignments rather than by an edge between the areas.

**Rules.**

| | Rule | Alarm | Command |
| --- | --- | --- | --- |
| R1 | the 5-minute average zone temperature is above 26 C **while the room is occupied** | MAJOR | `boost-cooling{15}` to the same controller |
| R2 | CO2 above 1000 ppm | MINOR | none |
| R3 | air-handler filter pressure drop above 250 Pa | MINOR | none, the human beat |
| R4 | a chiller sends nothing for 2 minutes | CRITICAL | none |

🔴 **A rule can only command the device that produced the event.** REACT's `sendCommand`
targets the detection's own device, so "a hot zone sensor commands its air handler" is not
expressible. Sensing and actuation live in one VAV controller, which is also how a real VAV box
is built. R1 is a sliding **aggregate** rule, and a windowed rule CAN carry `sendCommand`: the
action chain is independent of the detection kind (only a correlation rule, whose series is an
area, is refused), and event-processing's real publish gate and DETECT engine were run against
the document this scenario publishes.

**Calculated metrics need a tier that allows them, and the showcase makes its own.**
`cop = cooling_kw / power_kw` and `kwh_per_m2 = kwh / device.floor_area_m2` (the meters carry
`floor_area_m2` as a device constant, written into their metadata). A tenant's tier sets
`calculatedMetricsPerProfile`, and it is **0 in every platform tier**; that stays the default.
So `dcctl sim create --manifest towerpulse` ensures a tier named `tp-showcase` first, using the
operator login it already needs: it is created if absent, and if present only RAISED to the
scenario's floors (`calculatedMetricsPerProfile` 1, which is the most any one Towerpulse profile
declares, `ingestReadingsPerSecond` 1000, `ingestBurst` 2000), never lowered, so re-running is safe
and a higher value you set by hand survives. Nothing else is touched: silver, gold and bronze
keep calculated metrics off, and only the showcase's own tenant has them on. `sim destroy`
removes the sim's tenant and identities but leaves `tp-showcase` in place, because it is shared
by every Towerpulse sim on the instance (delete it in the console, Administration, Tiers, when no
tenant is at it). To use a tier you manage yourself, pass `--tier <token>`; that tier is used as
given and never created or changed, and the publish fails naming `calculatedMetricsPerProfile` if
it is below 1. If the operator login cannot create tiers, create fails at once naming the tier and
its config (dcctl has no tier command; use `createTenantTier` on the admin API or the console).

**The annex owner.** Bootstrap invites a customer user into the tenant with the read-only `viewer`
role and a scope of exactly one customer, the annex owner, and accepts the invitation with the
sim's own password (the one in the sim record), so the account can sign in at once. A member
limited to a customer sees only the devices assigned to it.

*Each sim has its own owner account.* Identities are global to the instance but every sim record
carries a password of its own, so one fixed address could belong to only one sim. The address is
derived from the sim's tenant: `owner@halden-labs.example` in tenant `sim-tp-live` is
**`owner+sim-tp-live@halden-labs.example`**, and the sim logs the exact address it provisioned on
every converge (`customer login: sign in as this address with the sim password`). The password is
the sim record's `simPassword`. Nothing resets the password of an account the sim did not mint, so
a second Towerpulse sim on the same instance gets its own owner and neither sign-in touches the
other. The simulator writes each account it mints into the sim record (`scenarioIdentities`)
before it creates it, and `dcctl sim destroy` deletes **only** the accounts on that list: it
removes their memberships from the tenant, deletes one that belongs to nothing else, and only
removes the membership of one that also belongs to another tenant. An address is never taken for the
sim's because it carries the `+<tenant>` tag, so an unrelated account or a person added to the
tenant by hand with the same tag is left alone (and the tenant delete says so). An older simulator
attached the shared `owner@halden-labs.example` instead: converge removes that membership from this
tenant, destroy does too for a sim not converged since, and the identity itself is never deleted.

Two boards. The operator's, `tp-dashboard`, shows both buildings and carries the setpoint control;
it is shared with nobody. The annex owner's, `tp-annex-dashboard`, is a different board, not the
operator's re-opened: it is anchored on the annex building and its floors (Building, Floor and Zone pickers, the first
two limited to their area type; a member limited to a customer is answered the areas their own
devices are in, so the pickers list the annex alone), and
carries no command widget, so a read-only member sees no write affordance and no widget that names a
Tower device. It is shared with the annex owner (a member limited to a customer lists only what is
shared with it). A re-run repairs a member left with a pending invitation, a wrong role or a
disabled account, and never widens the scope. The operator board's area selectors list every area,
floors and plants included, because the platform has no hierarchy to filter a building list by.

**An external device plane (`--external-device-plane`).** When a Unity player (or another
simulator) is the 31 devices, start this process with `--external-device-plane` (or
`DC_SIM_EXTERNAL_DEVICE_PLANE=1`). It then provisions and converges the whole tenant (profiles,
rules, devices, credentials, boards, the owner account) and idles with the control API and
presentation page up: it runs no `Tick`, publishes no reading, and opens no MQTT session as any
device, so there is exactly one writer per device. `POST /start` is refused (409) and `GET /status`
reports `externalDevicePlane: true` with the command far end in `external` mode; `POST /reset`
re-converges without publishing. `GET /config.json`, the only endpoint the Unity plane's config client and token broker call, is served as normal (fresh operator token on every request). This is not `--no-command-far-end`, which stops the command
replies but leaves the telemetry running. The broker address is still required in the handshake,
because the external client learns it from the same record.

**Command replies survive a re-dispatch.** command-delivery accepts a response only against the
dispatch nonce it currently holds, and it dispatches a command again, with a new nonce, when it
cannot be sure the first frame arrived. The command engine therefore deduplicates the physical
work by command token (a boost or restart is actuated once) but keeps one reply callback per
distinct nonce, up to a bound, and answers every live attempt when the work finishes. A late frame
of an older dispatch never replaces a newer one's callback.

**The operator board** (`tp-dashboard`) carries a building, floor and zone selector (the zone is a
slot scoped to the building), alarms by severity, devices online, each building's power with a
sparkline, a status card per chiller, each building's energy per floor area, the zones of a
floor compared on one chart with their alarms marked, the annex chiller's COP, the alarm table
with a tab per kind of equipment, a setpoint stepper that sends `set-setpoint` to the selected
zone, the zone's temperature and CO2, and the tower's power history, which the widget frame can
download as CSV. The time range is the board header's own control, an hour by default, and the
viewer can move it to an absolute window; nothing about it is authored beyond the default.

**The device plane is this process.** `Tick` advances a small, seeded thermal model and
posts every device's reading. It is a pure function of the elapsed time, advanced in fixed
one-second steps, so two runs agree at the same moment whatever the tick jitter. The real
thermal model, with its day timeline, is the Unity showcase's own; this one is the smallest
thing that is plausible, and should not grow toward it.

A fixed script, in minutes from the start of the run, crosses each rule once on a named device
and nothing else crosses any:

| At | Event | Rule |
| --- | --- | --- |
| 0:30 | a meeting fills `tp-vav-tower-f4-east` and the afternoon sun warms it | R1 raises at about 8:25 |
| 0:30 | the same sun on `tp-vav-tower-f5-north`, **which nobody is in** | R1 does not raise: the gate |
| 2:00 | a crowded room on `tp-vav-tower-f2-west` | R2 raises at about 11 |
| 2:00 to 20:00 | the filter of `tp-ahu-tower-2` clogs | R3 raises at about 16 |
| 15:00 | `tp-chiller-annex` goes silent | R4 raises two minutes after its last reading |

Commands are answered by the scenario, not by the answer-at-once receiver (`cmdreceiver.Handler`):
**`boost-cooling` is `SUCCESSFUL` only once the zone reaches its setpoint**, and `FAILED` if its
minutes run out first; `restart` is `SUCCESSFUL` once the chiller is back and reporting;
`set-setpoint` and `set-mode` take effect at once. A redelivered command is not worked twice.
A presenter can drop a chiller out of turn (`DropChiller` on the Sim); a restart brings it back.
The silent chiller keeps its MQTT session in this process, so the online doughnut does not drop
here: a presence drop is the Unity device plane's business.

### Harborpulse (a waterfront district)

`harborpulse` drives more of the platform at once than any other scenario. One utility tenant
runs a fictional harbour district of three quays (North Quay, East Quay, the Basin): 9 tide
gauges, 3 stormwater pump controllers, 12 street lights, 6 EV chargers, 2 building meters (each
owned by a customer, one of whom gets a login limited to their building), a flood-response
truck, and 4 LoRaWAN tide gauges behind a network server this process plays: 37 named devices. It
plays one **compressed day** on a loop, 24 sim hours in 24 minutes, starting at 06:00:

| Sim time | What happens | What the platform does |
| --- | --- | --- |
| 08:30-09:45 | The Ferry Terminal's demand passes its cap | `hp-peak-demand` (1-minute sliding average) raises MINOR |
| 11:00-12:00 | One East Quay lamp's driver reports intermittent faults | `hp-lamp-flicker` (repeating, 3 in a minute) raises MINOR |
| 14:00-15:30 | A Basin charger's connector overheats | `hp-connector-hot` (sliding average) raises MAJOR and sends `derate {max_kw: 11}`; the charger obeys and cools |
| 17:00-18:30 | Storm surge: every gauge rises fast | `hp-surge-rising` (rate of change) raises WARNING |
| 17:00-21:00 | The king tide and the surge | three thresholds share the alarm key `flood-level`: each gauge's ONE alarm escalates MINOR to MAJOR, and to CRITICAL on East Quay only, then eases |
| 18:30-20:30 | Three East Quay gauges above 2.1 m together | `hp-quay-flooding` (area correlation) fires for East Quay. It raises nothing: the platform forbids actions on a correlation rule |
| 17:00-21:00 | Wet wells fill | `hp-wet-well-high` raises MAJOR and sends `start-pump {duty_pct: 100}`; the well drains and the alarm clears, more than once |
| 19:00-20:30 | The East Quay pump air-locks: it runs and moves nothing | `hp-pump-air-lock` (held 30 s, over two metrics in CEL) raises CRITICAL |
| 17:00-22:30 | The truck drives from the depot to the flood plain and back | location events against two geofences; `hp-truck-in-flood-plain` (`geo.inFence`) raises WARNING while it is in the flood plain and clears when it leaves |
| 17:00-21:00 | Gauges pass the major and critical levels | `hp-flood-bulletin` publishes a bulletin through the MQTT connector `hp-bulletin-mqtt`; `hp-flood-webhook` calls the town's emergency webhook, signed with the handle `harborpulse/flood-emergency` (East Quay only); the policy `hp-duty-escalation` mails the duty officer about every MAJOR or CRITICAL alarm and mails again 30 seconds later (on the escalation sweep's next pass) if nobody acknowledged it |
| 19:45-21:15 | The East Quay feeder fails | its lamps and chargers go silent: `hp-light-silent` / `hp-charger-offline` (absence) raise MAJOR for exactly those; the chargers buffer their readings and send them, with their original timestamps, when power returns |

Every reading is a function of the seed, the sim time and the device, except what a command
changes: a wet well, a charger's cap, a lamp's dim level. A command is answered only after the
device has applied it, so an unobeyed command shows in the telemetry (the well reaches the pump's
own float switch at 3.2 m; the charger stays hot). Midnight resets derates and dimming so the next
day's beats repeat. `harborpulse_test.go` replays the day against each rule's own published
threshold, window, hold, count and timeout, with the commands obeyed and, as a control, ignored.

Each pump's `well_fill_pct` is a **calculated metric**, its wet well level over the well's depth,
a constant (`well_depth_m`, 4 m) the pump carries. A calculated metric needs a tier that allows
one, so `dcctl sim create --manifest harborpulse` packages the tenant at `hp-governed`:
one calculated metric per profile, 32 ingest readings per second and a burst of 150.
Ordinary five-second ticks fit; the East Quay restore exceeds the burst and is retried.
A manually created sim must be packaged at this governed tier for a clean verifier verdict.
Tier convergence raises floors only: use a fresh tier/tenant if an existing tier has higher ceilings.

The **LoRaWAN gauges** (`hp-lora-gauge-north`, `-east`, `-basin` and `hp-legacy-gauge-basin`)
initially send one uplink each per tick through the ChirpStack v4 integration `hp-chirpstack`, a raw frame
with no decoded object, so the platform decodes it with the profile's tenant decoder. Their
firmware alternates two frame layouts. `hp-lora-gauge-profile` decodes both; the legacy gauge's
`hp-legacy-gauge-profile` carries a decoder that reads the newer layout's battery past the end of
the frame, so every other uplink of that gauge is recorded as DECODE_FAILED and shows in its
decoder health. `harborpulse_s3_test.go` runs both decoders, and the well fill formula, through the
platform's own compilers.

**Where it delivers.** The webhook, the MQTT bulletins and the duty officer's mail all go to one
in-cluster sink, `hp-sink.harborpulse-sinks.svc.cluster.local` (`cmd/scenario-sink`, which
`hack/harborpulse-rig.sh` deploys), so the rule definitions are constants. The platform's egress
guard refuses a private address unless the operator admits it, so an instance that should deliver
needs the sink's ClusterIP allowed (`dcctl bootstrap|upgrade --dev-allow-egress <ip>` on a local
cluster) and the `outbound-connectors` area (`--enable-area outbound-connectors`). Without them the
scenario still provisions and runs, and every delivery is refused rather than sent. A handshake
written by an older dcctl lacks the outbound-connectors, notification and integration endpoints and
is refused at bootstrap: re-create the sim.

**Fleet operations.** The district is also run as a fleet:

- **Groups.** Street lights and pumps carry SHARED attributes (`role`, `quay`, and `critical` on
  the pumps), and four dynamic groups select on them: `hp-all-lights`, `hp-east-quay-lights`,
  `hp-basin-pumps` and `hp-critical-pumps` (East Quay's and the Basin's). `hp-wet-well-watch`, an
  early warning at 2.0 m, is **scoped** to `hp-critical-pumps`: every pump's well crosses 2.0 m in
  the storm, and the North Quay pump's raises nothing.
- **Campaigns.** Two SOLAR campaigns over `hp-all-lights` (`hp-lights-dusk`, full output at
  sunset, and `hp-lights-dawn`, trimmed to 50 % at sunrise) are planned per lamp from each lamp's
  surveyed installed position, so the planned instants run east to west along the promenade, a
  second or more apart. The dusk campaign declares tomorrow (in the town's zone) a public holiday
  and plans nothing for it; the dawn campaign, with no holiday, plans that day. As each sim day
  runs, the sim creates two one-shot CALENDAR campaigns: at 21:30 East Quay's lights are dimmed to
  40 % (`hp-night-dim-*`), and at 14:00 the Basin pump is sent `flush-well` (`hp-basin-flush-*`).
- **Maintenance window.** `flush-well` is DISRUPTIVE, and the pump controllers' type carries the
  window `hp-pump-service-window`, so the 14:00 flush is held until the crew's slot opens at 15:00
  and runs inside it. The window gates when a disruptive command may START; it does not silence
  alarms (the platform has no alarm-suppression window).
- **Hierarchy and graph.** Assets `hp-waterfront` > one site per quay > its pump station, its
  feeder and its buildings (`hp-bldg-quayside-lofts` on North Quay, `hp-bldg-ferry-terminal` on the
  Basin). The customer meters are assigned to their buildings and the pumps to their stations. A
  typed relationship `hp-feeds` joins each quay's feeder to that quay's lights and chargers, so the
  graph names the East Quay outage's blast radius before it happens.

🔴 **Real time is not compressed.** A campaign trigger and a maintenance window are wall-clock
schedules, at a resolution of one minute. The CALENDAR jobs and the window's slot are written as
each sim day runs, at the first whole wall minute after their sim time (a sim hour is a wall minute
at the default day), and `/status` lists them under `scenario.jobs`. Real sunset and sunrise cannot
happen inside a 24-minute day: the SOLAR campaigns fire at the real dusk and dawn of wherever the
lamps stand, and are judged on what the platform PLANNED, never by watching a lamp switch at a
compressed dusk. Port Alder is fictional, and its coordinates are in open Atlantic water so that no
map puts it on a real town.

It publishes two boards: `hp-dashboard`, the operator's (a quay picker, alarms by severity and
devices online on that quay, every gauge of the quay against the three flood tiers, the response
truck on a map, the East Quay pump's wet well and the Basin charger's connector with their alarms
and commands drawn on, and the alarm table), and `hp-lofts-dashboard`, shared with the Quayside
Lofts customer only: its meter and its alarms, read-only.

**The scenario clock.** `GET /status` carries a `scenario` block for harborpulse (and nothing new
for any other scenario): the sim day, hour and `HH:MM`, the beats in progress, the day's length and
the wall time of the first tick, the planted defects, and the store-and-forward ledger (what each
charger buffered in each outage). `--day-length` (default 24m, the least it accepts) stretches the
day; the rules' windows are wall time, so a shorter day would make a beat shorter than the rule
that detects it.

**Verifying a day (`scenario-verify`).** `cmd/scenario-verify` watches the next whole sim day of a
running harborpulse sim and judges what the platform did, read back over the tenant GraphQL APIs as
the sim's own identity, and, with `--sink`, the in-cluster sink's record:

    go run ./cmd/scenario-verify --handshake ~/.devicechain/sims/<name>.json --sim http://127.0.0.1:8090 \
      --sink http://127.0.0.1:28182 --report day.json

It makes two writes of its own: it rotates the flood webhook's signing secret to one it holds (the
platform shows a secret once, to whoever minted it), and it acknowledges `hp-gauge-north-2`'s flood
alarm ten seconds after it reaches MAJOR, as the escalation's control.

| Invariant | Holds when | Planted defect that must turn it red |
| --- | --- | --- |
| `flood-escalates` | each gauge keeps ONE flood alarm, raised at MINOR, escalating in place (CRITICAL on East Quay only, MAJOR elsewhere) and clearing | `--scenario-defect flood-split-key` (the critical tier under a key of its own) |
| `pump-commanded` | a `start-pump` reaches SUCCESSFUL, the pump then runs and its well falls, and the well never reaches the pump's own float | `--no-command-far-end` |
| `derate-obeyed` | a `derate` reaches SUCCESSFUL, the charger reports its cap at 11 kW, its connector reads below the rule's level before the long session ends (so the derate cooled it, not the session ending), and `connector-hot` clears | `--scenario-defect refuse-derate` |
| `air-lock-only-east` | `pump-air-lock` for the East Quay pump only, inside its window | `--scenario-defect air-lock-basin` |
| `correlation-fires` | `previewRule` of `hp-quay-flooding` over the storm fires for East Quay only | the verifier previews the same rule with a count no quay reaches, in the same day (`correlation-control-is-red`) |
| `outage-blast-radius` | `light-silent` / `charger-offline` for exactly the East Quay lamps and chargers, inside the outage, all cleared after it | `--scenario-defect silence-north-lamp` |
| `backfill-stored` | every reading a charger buffered is stored at the time it was taken | `--scenario-defect drop-backfill` (every fifth buffered reading lost) |
| `truck-fence` | `truck-in-flood-plain` for the truck alone, raised as it enters the flood plain and cleared after it leaves | `--scenario-defect truck-stays-home` |
| `attribute-threshold-moves` | stored wet-well readings and alarms follow each pump's SHARED threshold, including the Basin change from 1.0 to 1.8 m | `--scenario-defect attribute-not-updated` |
| `downlink-acknowledged` (sink) | one typed command is SUCCESSFUL, one confirmed queue item carries fPort 12 and `000f`, and stored readings follow the new interval | `--scenario-defect downlink-nack` |
| `backfill-completes` | a SUCCEEDED job writes missing fill values beside every stored input in the gap | `--scenario-defect backfill-wrong-window` |
| `well-fill-calculated` | each pump's stored `well_fill_pct` is its wet well level over 4 m, as a percentage | `--scenario-defect wrong-well-depth` (the pumps carry 5 m) |
| `lora-decoded` | each current LoRaWAN gauge's stored levels are exactly what it encoded, and its decoder failed nothing | `--scenario-defect lora-wrong-scale` (millimetres where the decoder reads centimetres) |
| `decoder-failure-visible` | the legacy gauge's undecodable uplinks are recorded as DECODE_FAILED with the evaluation error, its decoder health counts failures and successes, and its readable uplinks are stored | `--scenario-defect legacy-decoder-fixed` |
| `webhook-signed` (sink) | the flood webhook reached the sink for exactly the East Quay gauges, signed with the tenant's secret | `--scenario-defect unsigned-webhook` |
| `webhook-signature-control-is-red` (sink) | the same deliveries checked against a secret the platform never had are judged red | none: it is `webhook-signed`'s control, run in the same day |
| `mqtt-bulletin` (sink) | a bulletin on the tenant's topic for every gauge | `--scenario-defect mqtt-unreachable` (the connector aimed at a port nothing listens on) |
| `notify-escalates` (sink) | every gauge's flood alarm paged the duty officer and, unacknowledged, paged again, and the mail reached the sink | the verifier acknowledges `hp-gauge-north-2` before its escalation (`notify-ack-control-is-red`) |
| `notify-ack-control-is-red` (sink) | `hp-gauge-north-2`, acknowledged by the verifier, was paged and NOT paged again | none: it is `notify-escalates`' control, run in the same day |
| `groups-resolve` | each group resolves, as a command target, to exactly the devices its selector should hold | `--scenario-defect east-lamp-mislabelled` (one East Quay lamp tagged North Quay) |
| `group-scoped-rule` | `wet-well-watch` for exactly the critical pumps, while the North Quay pump's well crossed the same level | `--scenario-defect unscoped-watch` (the rule published without its scope) |
| `night-dim-campaign` | the day's 21:30 campaign fired once, for exactly East Quay's lights, every command succeeded, those lamps reported 40 % and no other lamp did | `--scenario-defect night-dim-all-lights` |
| `solar-wave` | for the dusk and dawn campaigns, every lamp's planned instant is the sunset (sunrise) at the position the platform holds for it, and the lamps run east to west | `--scenario-defect lamp-unsited` (one lamp has no position: NO_SITE) |
| `holiday-skipped` | the dusk campaign declares the day after it was created a holiday, has planned past it, and planned no cycle for it, while the dawn campaign planned it | `--scenario-defect holiday-dropped` |
| `window-holds-flush` | the day's `flush-well` was queued at 14:00, carries the pump window, was sent only once the window opened and before it closed, and succeeded | `--scenario-defect flush-ungated` (the pump type has no window) |
| `asset-hierarchy` | each meter is assigned to its building and each pump to its station, each under its own quay's site under the waterfront | `--scenario-defect lofts-under-basin` |
| `feeder-blast-radius` | each feeder feeds exactly its quay's lights and chargers, and every device the failed feeder feeds went silent in the outage | `--scenario-defect feeder-miswired` (one East Quay lamp on the North Quay feeder) |

It exits 0 when every invariant holds, 1 when one fails and 2 with no verdict (the sim unreachable,
its day not started or restarted mid-watch, a gap in the alarm polls a flood tier could hide in, or
a failed read). Without `--sink` the six sink invariants are not judged and the report names them
as omitted. `hack/harborpulse-rig.sh` runs the clean day and every planted defect side by side
on a cluster of its own and fails if the clean day is not green or a control does not turn its own
invariant red. The defects exist only for that: never run a demonstration with one.

### Harborpulse signed rollout, governed restore and replacement (S5)

The handshake now includes optional `endpoints.updateMgmtGraphQL`. Harborpulse requires it
and asks you to re-create the sim with a current dcctl if it is absent. The default local
instance provides update-management, an object store and downloads. The scenario owns six
charger update clients; combining it with `--ota` fails bootstrap.

Before its first tick, harborpulse uploads a deterministic 48 KiB synthetic firmware payload,
signs it with a process-local ECDSA key, and waits for AVAILABLE/SIGNED and the clients' broker
subscriptions. This demonstrates a version change from 2.4.0 to 2.4.1, using synthetic bytes.
At day 0 12:30 it replaces the physical unit behind `hp-gauge-north-3` for water ingress:
the token and `PAW-GAUGE-NORTH-3` external ID survive, its old credential is retired, and the
once-returned new credential is installed before telemetry and its command subscription resume.
Reset keeps that credential in this process. A fresh process cannot recover it and fails that
gauge loudly. `/status` records credential tokens only, never access tokens or private keys.

At every 21:15 restore, a bounded queue sends the East chargers' original timestamps and values.
It keeps refused/ambiguous entries, honours Retry-After, waits for the next completed ordinary
tick, and records a loud dead letter on bounded retry exhaustion. At day 0 26:00 (02:00 the next
night), it starts one rollout over `hp-all-chargers`, with maxInFlight=2 and failureThreshold=2.
North-1 deliberately fails verification; the other five chargers update. This rollout and the
physical replacement happen once per run, rather than daily.

The verifier reads platform truth for eight S5 invariants:

| Invariant | Planted defect |
| --- | --- |
| `ota-artifact-trusted` | `ota-signature-omitted` |
| `ota-targets-exact` | `ota-group-missing-charger` |
| `ota-concurrency` | `ota-cap-three` |
| `ota-outcomes` | `ota-failure-disabled` |
| `burst-rate-limited` | `burst-unthrottled` (rig packages this control at `hp-showcase`) |
| `burst-caught-up` | `burst-drop-on-429` |
| `replacement-identity` | `replacement-new-device` |
| `replacement-credential` | `replacement-old-credential-left-live` |

The watch continues through the following night's rollout, sampling in-flight counts each
second. Historical completed rollouts use their complete platform dispatch/terminal timeline;
missing live coverage produces no verdict. Replacement checks read the operation history,
credential states and stored readings, and send a battery-only probe through the real ingress
with the original credential (401 required; 429 retried). The left-live control's accepted
probe is also read back from storage. An unsigned artifact is refused, so that control also
fails targets, concurrency and outcomes. Moving the gauge to a separate logical device also
fails flood escalation, MQTT bulletins and notification escalation, which require the original
gauge identity. Offline fixtures explicitly model those unavoidable collateral failures.

### Harborpulse shared thresholds, downlinks and calculated backfill (S3b)

#### Shared attribute thresholds

Every pump carries the numeric SHARED attribute `well_alarm_m`, provisioned with
`setEntityAttribute`. The separate `hp-attribute-well` rule authors
`when.thresholdAttr`, with no literal threshold and no pump command. North uses
1.1 m, East 1.3 m and Basin 1.0 m. At 18:30 the Basin threshold becomes 1.8 m.
`/status.scenario.thresholdChanges` gives the intended write instant and old/new
values. The oracle compares settled stored wet-well readings with platform alarm
samples, including readings between the two bounds before and after the change.
`attribute-not-updated` skips that write and must turn `attribute-threshold-moves` red.

#### A typed command through the network server

At 16:30 the sim issues one typed `sampling-interval {seconds:15}` command to
`hp-lora-gauge-north` through `createCommand`. A sim-issued beat gives exactly one
idempotent command per day; it does not depend on a tide rule's repeated crossings.
The published command encoder emits confirmed fPort 12, bytes `00 0f`.
The published `lorawan_downlink` connector links `hp-chirpstack` to the sink's
ChirpStack v4 REST queue. Its fixture API credential is sealed through the connector
`secret` field, with a separate credential and queue path for each tenant.

The instance needs `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled: true`.
The chart derives command-delivery's routing switch and event-sources' report feed
from it. Command-delivery stamps the integration source, the outbound adapter reads
the device's DevEUI and published encoder, claims the command, and POSTs with the
API key to `/chirpstack/<tenant>/api/devices/<devEui>/queue` in the sink.
The sink reuses `lorawan.Downlink` validation and records each accepted queue item.
Before a gauge uplink the sim snapshots pending items from the sink; after that
uplink it sends the existing network-server flow's `txack`, then `ack`, through the
integration using the issued integration token, queue item ID and fCntDown.
A queue item arriving during the uplink waits until the next uplink. Applying the
command changes actual uplink cadence before acknowledgement. `downlink-nack`
reports `acknowledged:false` and retains the old cadence.

`downlink-acknowledged` requires exactly one platform command to be `SUCCESSFUL`,
exactly one matching sink queue receipt with the expected confirmed port/bytes,
and stored gauge readings at the new interval after acknowledgement. It is omitted
without the verifier's `--sink`. The original tick cadence must be shorter than
15 seconds to demonstrate the change; the default five-second cadence works.
The host-side sim reaches the sink through `DC_SIM_HARBORPULSE_SINK_URL`, which the
rig points at its existing loopback port-forward. Inside the cluster the default
is the sink service URL. The platform only dials the admitted sink ClusterIP.

#### Calculated metric backfill

For backfill, the Basin pump's depth constant is withheld at 12:00 and restored at
15:00. The metric stays bound: the shipped formula evaluator skips values whose
constant is missing while storing the measured wet-well inputs. The oracle reads
the 14:00–14:30 span before the job starts and requires measured inputs with no
calculated values. At 16:00 the sim checks `calculatedMetricBackfillAvailability`
and starts `startCalculatedMetricBackfill` with explicit metric and device lists.
The window is in the past, within retention and the 90-day limit. The pump profile
already has the calculated-metric tier allowance supplied by `hp-showcase`.
The instance must run the calculated-backfill worker with a sufficient TSDB pool;
the rig's values overlay enables it and supplies 16 pool connections.

`backfill-completes` reads the job through GraphQL, requires `SUCCEEDED`, positive
sample and written counts, and matches every stored wet-well input in the span to
`well_fill_pct` at the same instant, using level / 4 m × 100. The job's token and
intended range appear in `/status.scenario.calculatedBackfills`; completion is
never inferred from status or simulator counters. `backfill-wrong-window` submits
a past window missing the gap and must turn this invariant red. Shared thresholds
and sampling cadence reset for each compressed day, so the watch also works on a
later day of a looping run.

### The command far end

A device that only POSTs telemetry cannot receive anything, so a scenario with a
control widget on its board needs a second connection: every device subscribed to
`{instance}/{tenant}/device-commands/{token}` on the NATS MQTT gateway, answering
on `{instance}/{tenant}/command-responses/{token}` (the `cmdreceiver` package, shared with
the load-test command harness). A scenario declares WHO answers with `CommandFarEnd`
on its manifest, which has three settings:

| mode | who answers | in-process receiver | command widget on a board |
| --- | --- | --- | --- |
| `none` (the default, and what an omitted field means) | nobody | no | **refused** by `Manifest.Validate` |
| `internal` | this process, per device | yes | allowed |
| `external` | a presentation client in another process — a Unity player that IS the device | no | allowed |

widgetlab, towerpulse, harborpulse and fleetpulse are `internal`; sitepulse is `external`; devicepulse and buildingpulse are `none`. Both far-end modes
must still declare a command vocabulary on a profile: for `external` that vocabulary
is the whole contract with the other process, since it is what that client subscribes
for and what the board's widget enqueues against.

sitepulse's board (`sp-dashboard`) carries no command widget yet; the console dispatch
button is the next slice. `Manifest.Validate` would accept one, because its gate refuses a
control widget only under `none`, so a test keeps it off until then.

sitepulse also provisions a geofence, `sp-geofence-pit` ("Pit"): a polygon round the pit rim,
derived from the scene's own rim outline through a documented site origin (39.0, -117.0;
the conversion is recorded in `sim/sitepulse_geofence.go`). The Unity player must convert its
location reports with the same origin and formula for the fence and the machine positions to
agree. The fence is a boundary of the pit, not of zone `sp-zone-cut` (the zone's crusher
plant is outside it), and the platform has no fence-to-area link, so the description is the
only tie. No rule uses it yet, and the dashboard map does not draw fences; the console's
Areas, Geofences page does.

`external` attaches nothing here on purpose. A Go receiver running alongside the real
device would answer `SUCCESSFUL` for a command only that client can act on — the
machine on screen never moves and every layer reports success, which is worse than
the expiring command this seam was built to remove, because it looks like it worked.
The cost is that the simulator cannot see whether anyone is out there: `GET /status`
reports the far end's `mode`, so `attached: false` can be read as "answered
elsewhere" rather than "answered by nobody", but it is a statement of intent, never
evidence.

It does **not** waive the broker address, and the polarity reads backwards until you
follow where that address goes: this process does not dial the broker in `external`
mode, but it is where the presentation client learns the URI, so an empty value here
is an empty value in the scene. An unreachable `internal` far end fails the bootstrap
in front of the operator; an unreachable `external` one fails in another process, as
a scene that never receives anything while this side's bootstrap, `/status` and board
are all green. Both modes therefore refuse to bootstrap without `endpoints.mqttBroker`.

`dcctl sim create` resolves the gateway address into the handshake
(`endpoints.mqttBroker`, default `ssl://<server>:1883` — the broker terminates TLS
independently of the HTTP ingress) and records whether to verify its certificate
(`mqttTLSInsecure`, on by default without `--tls`, since a local bring-up's gateway
cert is self-signed). Override either with `--mqtt-broker` / `--mqtt-insecure`.

**Bootstrap FAILS if an `internal` far end cannot be brought up** — no broker
configured, or any device that does not come back subscribed. Degrading to "run
without it" is the failure the seam exists to remove: the scenario would come up
green and its Send button would enqueue commands that expire unanswered. On a host
that genuinely cannot reach the gateway, `--no-command-far-end` accepts that
knowingly; it logs a warning at startup and `GET /status` reports the channel as
`declared` but `disabled`, alongside per-device receive/respond evidence when one
is attached.

The flag means one thing in every mode that has a far end: **the operator accepts
that this scenario's commands will not be answered.** For `internal` it skips the
attach; for `external` there was never an attach to skip, but the acceptance is the
same — and in both it relaxes the broker requirement, which is what makes it an
escape hatch on a host that cannot reach the gateway rather than a flag that helps
only one mode. `/status` reports `disabled: true` for both.

Against a `none` scenario it accepts nothing, so `/status` stays silent — but startup
still warns that the flag changed nothing, because a flag that silently does nothing
is read as one that worked.

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

### Binary uplinks for a payload decoder (`--lorawan-binary`)

Integration mode (`--lorawan-ns chirpstack|tts`) normally plays a network server with a codec: each
uplink carries the decoded object. `--lorawan-binary` plays one **without** a codec: the uplink
carries only the raw six-byte application frame, on port 2, and no decoded object, so what the
platform stores is whatever the device profile's payload decoder makes of the frame. Without a
published decoder nothing is stored from such an uplink.

```sh
dc-simulator --lorawan-print-decoder          # the decoder document that reads the frame
dc-simulator --lorawan-ns chirpstack --lorawan-binary   --integration-url https://<host>/api/integrations/<tenant>/<integration>   --deveui 70b3d57ed0012345 --uplinks 5       # token in DC_SIM_INTEGRATION_TOKEN
```

The frame is a version byte (1), the temperature as a signed 16-bit number in tenths of a degree, the
humidity in percent, the battery voltage in tenths of a volt, and the door as the low bit of the last
byte. The printed decoder declares one saved vector; set it on a profile that declares `temperature`,
`humidity` and `battery.voltage` as doubles and `door` as a boolean (the same metrics default mode
stores), publish, and the stored values are the ones default mode would have stored.

### Downlinks and the acknowledgement feed (`--lorawan-downlink-listen`)

In `--lorawan-ns chirpstack` mode, `--lorawan-downlink-listen :8085` also plays
ChirpStack v4's REST queue. The equivalent env var is
`DC_SIM_LORAWAN_DOWNLINK_LISTEN`. Set the queue's API key in
`DC_SIM_LORAWAN_API_KEY`; it has no command-line flag. Configure the platform's
outbound connector with this base URL and API key. The listener starts before
the first uplink and shuts down when the run ends. TTS mode refuses this listener.

The endpoint is `POST /api/devices/{devEui}/queue`; a base URL path prefix is
also accepted. It takes ChirpStack's envelope:

```json
{"queueItem":{"devEui":"70B3D57ED0050004","fPort":12,"confirmed":true,"data":"ANcD","expiresAt":"2030-01-01T00:00:00Z"}}
```

It answers `200 {"id":"<uuid>"}`. `expiresAt` is optional and logged; the
simulator does not expire queued items. Empty data is allowed. Requests are
limited to 64 KiB. Missing or wrong bearer keys get HTTP 401 with gRPC code 16;
devices outside `--deveui` get 404 with code 5; malformed bodies, mismatched
DevEUIs, ports outside 1..223 or invalid standard padded base64 get 400 with
code 3. Other paths get 404 with code 5; other methods get 405. Refusals carry
JSON `{"code":N,"message":"..."}` and never queue an item.
Once the run has stopped taking downlinks, the queue refuses with HTTP 409 (gRPC code 9).

Confirmed items follow class A timing. Before each device uplink POST, the
simulator snapshots that device's pending items. After that POST, it sends a
`?event=txack` report followed by `?event=ack` for each item in the snapshot.
An item queued during an uplink POST waits for the next uplink. Both reports
carry the queue response's ID and the same per-device `fCntDown`, starting at 0
and increasing for each reported item. Reports use the integration bearer token
and honor `--integration-host`, just like uplinks.

`--lorawan-ack ack|nack|none` (or `DC_SIM_LORAWAN_ACK`, default `ack`) controls
these reports. `ack` reports `acknowledged:true`; `nack` sends txack then an ack
with `acknowledged:false`, as when ChirpStack gives up waiting. `none` accepts
items but never reports them, to exercise the platform's deadline path.
Unconfirmed items are accepted and logged but never reported in any mode.
An explicit `--lorawan-ack` requires the listener.

After `--uplinks` rounds, the run stays up until every pending confirmed item
has been answered in ack or nack mode. It sends additional real uplinks at
`--emit-interval`, keeping their counters increasing; a zero interval uses 1s
while waiting. `--uplinks 0` is allowed with the listener and keeps uplinking
until interrupted. With a listener, SIGINT or SIGTERM prints the summary and exits 0;
an uplink-only run keeps its previous behaviour, returning the cancellation error.
Transport errors still make the run fail.

The listener writes JSON lines to stdout, followed by the usual run summary (a real run against an
instance whose rule sent one confirmed command; a refusal is logged as
`{"event":"downlink_refused","reason":"invalid bearer key","status":401}`):

```json
{"ack":"ack","addr":"127.0.0.1:8085","event":"downlink_listening"}
{"event":"downlink_queued","devEui":"70B3D57ED0050004","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","fPort":12,"hex":"00d703","confirmed":true,"expiresAt":"2026-11-11T17:30:19Z"}
{"devEui":"70B3D57ED0050004","event":"downlink_reported","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","report":"txack","status":202}
{"acknowledged":true,"devEui":"70B3D57ED0050004","event":"downlink_reported","id":"3ffed47b-dfa3-4d57-a4a9-4ce59dc03d31","report":"ack","status":202}
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"queued":1,"reported":{"ack":{"202":1},"txack":{"202":1}},"runId":"1794418086190782000"}
```

Queued lines include `expiresAt` when supplied. Refusals never log the presented
key. The summary adds `"queued":1` and
`"reported":{"txack":{"202":1},"ack":{"202":1}}` when a listener is used.
Report transport failures use status `"0"` in `reported`, increment `errors`,
and set `firstError`, as uplink failures do. `statuses` continues to count only
uplink responses. Without a listener the summary has no new fields.

For example, with `DC_SIM_INTEGRATION_TOKEN` and `DC_SIM_LORAWAN_API_KEY` set:

```sh
dc-simulator --lorawan-ns chirpstack \
  --integration-url https://host/api/integrations/acme/lora \
  --deveui 70B3D57ED0050004 --lorawan-downlink-listen :8085 \
  --lorawan-ack ack --uplinks 0 --emit-interval 1s
```

### X.509 (mTLS) device mode (`--x509`)

`--x509` plays devices that authenticate with a client certificate instead of a token. It is a
mode of its own, like `--lorawan-ns`: it needs `--handshake` (tenant, instance id, the MQTT
broker, the device-management endpoint) and does not run a scenario.

```sh
go run . --handshake hs.json --x509 --x509-dir ./x509-state \
  --x509-pinned 20 --x509-jit 5 \
  --x509-register --x509-device-type my-device-type
```

It does three things, each of which can be used alone:

1. **Mints** a throwaway test CA and the device certificates into `--x509-dir`. Every device
   certificate is ECDSA P-256, has the `clientAuth` extended key usage, is not a CA, and has
   `CN` = the device's externalId (`<prefix>pin-0001`, `<prefix>jit-0001`, ...). An existing
   state dir is reused and topped up, never re-keyed. The CA private key is the single file
   `ca.key`, written with mode `0600` (on Windows the mode bits are advisory); no manifest holds
   key material. This is test PKI: never register its CA anywhere that matters.
2. **Registers** (`--x509-register`, needs `--x509-device-type`) through the tenant GraphQL API, as
   the sim identity does for everything else: the CA as a trust anchor, then for each *pinned*
   certificate a device and an `X509_CERTIFICATE` credential. It is idempotent by token. The
   `--x509-jit` certificates are minted and **never registered**: they are for devices the
   platform has not seen, so a registration path has certificates to present.
3. **Connects** (omit with `--x509-mint-only`) to the handshake's `mqttBroker`, which must be a
   TLS scheme, one session per certificate: username `{tenant}:@x509`, no password, and the
   usual device client id `{instance}:{tenant}:{token}`. The token of every minted device, pinned
   or not, is `core.DeriveDeviceToken(CN, "pv-")`, so a device's client id is a pure function of
   its CN. Verify the broker with `--x509-server-ca <pem>` (or the handshake's
   `mqttTLSInsecure`). One JSON line on stdout reports connected/refused per kind and per device;
   a refusal is a result, not a failure (a JIT certificate is refused until registration
   exists). The sessions are held until interrupted, or for `--x509-hold`.

| Flag | Env | Purpose |
|------|-----|---------|
| `--x509-dir` | `DC_SIM_X509_DIR` | State dir (default `x509-state`). |
| `--x509-pinned` / `--x509-jit` | `DC_SIM_X509_PINNED` / `_JIT` | How many of each (defaults 1 and 0). |
| `--x509-prefix` | `DC_SIM_X509_PREFIX` | CN prefix (default `sim-`). |
| `--x509-anchor` | `DC_SIM_X509_ANCHOR` | Trust anchor token (default `dc-sim-x509-ca`). |
| `--x509-validity` | `DC_SIM_X509_VALIDITY` | Device certificate lifetime (default one year). |
| `--x509-hold` | `DC_SIM_X509_HOLD` | Disconnect after this long. |

The unit tests prove the minted chain passes `x509trust.VerifyClientChain` and that what goes
over a TLS handshake (certificate, username, client id) is what the callout expects. They do not
run a real broker and callout; the rig does.

### Over-the-air update devices (`--ota`)

`--ota` gives **every device of the scenario** an update client that runs the real device-side update
protocol against the instance. It is additive: a sim started without it is exactly what it was.

Each device holds its own MQTT session (the device's client id plus the `:ota` discriminator, so the
command far end keeps the plain one), subscribes to its `device-desired` topic, and on every connect
sends `sync` and `update.inventory`. When the platform assigns an update it:

1. receives `update.assignment` and reports `RECEIVED`;
2. asks for a grant (`download.request`) and fetches the image from the capability URL in `Range`
   windows (`--ota-chunk` bytes each), resuming from the bytes it holds after any failure;
3. checks the SHA-256 of what it holds against `artifact.digest`, then the detached signature over the
   raw digest against its trust set (`--ota-trusted-key`), and only then reports `VERIFIED`;
4. "installs" (swaps its version string; nothing is executed), reboots (new boot id) and reports
   `RUNNING` with the new version.

A device that cannot verify reports `FAILED` with `VERIFICATION_FAILED`, `SIGNATURE_INVALID` or
`UNKNOWN_SIGNER`, keeps its old version, and never reports `RUNNING`. The protocol logic is the
`updater` package, which speaks the wire from its own structs and is held to the core contract's
golden fixtures; `otafleet` is the broker wiring and the fault injection around it.

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--ota` | `DC_SIM_OTA` | Turn the update devices on. |
| `--ota-trusted-key` | `DC_SIM_OTA_TRUSTED_KEY` | Comma-separated PEM public key files (ECDSA P-256 or Ed25519) the devices trust. Required unless `--ota-allow-unsigned`. |
| `--ota-allow-unsigned` | `DC_SIM_OTA_ALLOW_UNSIGNED` | Install an `UNVERIFIED` update. Off by default, as on the reference device. |
| `--ota-version` | `DC_SIM_OTA_VERSION` | Version a fresh device runs (default `1.0.0`). |
| `--ota-component` | `DC_SIM_OTA_COMPONENT` | Component each device manages (default `firmware`). |
| `--ota-state-dir` | `DC_SIM_OTA_STATE_DIR` | Keep per-device update state so a restarted simulator continues the same attempts (default: a temp dir, removed on exit). |
| `--ota-chunk` | `DC_SIM_OTA_CHUNK` | Bytes per ranged request (default 32768). |
| `--ota-step-delay` | `DC_SIM_OTA_STEP_DELAY` | Pause between a device's steps, to make a rollout watchable. |
| `--ota-bad-hash` | `DC_SIM_OTA_BAD_HASH` | N devices are served an image with one flipped byte: `FAILED VERIFICATION_FAILED`. |
| `--ota-interrupted` | `DC_SIM_OTA_INTERRUPTED` | N devices lose the download part-way once, then resume from the bytes they hold and finish. |
| `--ota-bad-signature` | `DC_SIM_OTA_BAD_SIGNATURE` | N devices see a corrupted signature: `FAILED SIGNATURE_INVALID`. |
| `--ota-offline` | `DC_SIM_OTA_OFFLINE` | N devices never connect to the broker. |
| `--ota-offline-for` | `DC_SIM_OTA_OFFLINE_FOR` | Bring the offline devices online after this long; they sync and the platform resends what they missed. |

Faulty devices are taken in the order the scenario provisioned them: the first `--ota-bad-hash`, then
`--ota-interrupted`, `--ota-bad-signature`, `--ota-offline`; every device after those is healthy. A
fault flag without `--ota`, or more faults than devices, is an error and not a quietly smaller demo.

`GET /status` gains an `updates` object: devices, how many are on the broker, counts by injected mode,
by running version and by latest stage, the fault tallies (`corruptedDownloads`, `interruptions`,
`resumes`) and the first failed devices with their last report.

**A demo fleet**, against an instance that runs the update service (`default` or `full` profile). The
`dcctl sim run` child inherits the environment, so the flags can be set either way:

```sh
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out ota-signer.pem
openssl pkey -in ota-signer.pem -pubout -out ota-signer.pub.pem

dcctl sim create fleet --manifest devicepulse          # your usual create flags
export DC_SIM_OTA=1 DC_SIM_DEVICES=8 DC_SIM_OTA_TRUSTED_KEY=ota-signer.pub.pem \
       DC_SIM_OTA_BAD_HASH=1 DC_SIM_OTA_INTERRUPTED=2 DC_SIM_OTA_OFFLINE=1 \
       DC_SIM_OTA_STEP_DELAY=300ms DC_SIM_OTA_CHUNK=8192
dcctl sim run fleet
```

or directly: `dc-simulator --handshake <handshake.json> --devices 8 --ota --ota-trusted-key ota-signer.pub.pem
--ota-bad-hash 1 --ota-interrupted 2 --ota-offline 1`.

On the platform side (the tenant GraphQL of the update service; `hack/ota-rig.sh` has the same calls
as shell): `registerArtifactSigner` with `ota-signer.pub.pem`; `createArtifact` (`kind: FIRMWARE`,
`component: "firmware"`, size and `sha256:` digest of any file of a few hundred KB, since the
simulator never runs the image); `PUT /updates/artifacts/<token>/content` with the bytes;
`addArtifactSignature` with `openssl dgst -sha256 -sign ota-signer.pem -binary <file> | base64`;
put the sim's devices in an entity group; `createUpdateRollout(request: {token, name, artifactToken,
groupToken, maxInFlight: 2})` and `startUpdateRollout`. Watch `/status` on the sim and the rollout's
progress in the console: the healthy devices go `UPDATED`, the bad-hash and offline ones fail or
time out, the interrupted ones finish after a resume, and `maxInFlight` is never exceeded.

What a live run needs:

- The scenario must have at least as many devices as faults (`devicepulse` has one unless you pass
  `--devices` / `DC_SIM_DEVICES`); the sim refuses to start otherwise.
- An instance whose update service has an object store and a `downloadBaseUrl` (the update service
  reports `downloadsConfigured`); without them there is nowhere to download from.
- On a kind cluster whose ports are remapped, create the sim with `--server 127.0.0.1:<port>
  --host-header localhost`. The simulator downloads through the address the sim was created with:
  with a host header it dials that address and presents the instance host; without one, a capability
  URL naming the same host on another port is redirected to the address's port.
- Leave `failureThreshold` out of the rollout (or set it above the number of faulty devices): a
  bad-hash failure can otherwise pause it, which is correct platform behaviour but not the demo.
- `--ota-offline-for` is measured from the simulator's bootstrap, not from the rollout's start.
  Start the rollout first if the offline devices should miss the assignment and catch up on sync.

The tests run the real devices over a real socket against an in-process broker (`internal/fakemqtt`)
with the real update reducer as the platform, including a group rollout with `maxInFlight`, each
fault mode, and a forged signature that must be refused. They cannot prove the real broker's ACLs or
the real download endpoint; `hack/ota-rig.sh` and a live instance do.

### `GET /config.json`

The presentation page — and, for an `external` far end, the out-of-process client
that IS the device — fetches this on load. It is the only channel between this
process and a client that is not this process:

```json
{
  "tenant": "acme",
  "manifestId": "sitepulse",
  "wsUrl": "ws://localhost/api/event-management/graphql",
  "apiOrigin": "http://localhost",
  "token": "<tenant-admin access token>",
  "instanceId": "dc",
  "mqttBroker": "ssl://localhost:1883",
  "mqttTLSInsecure": true
}
```

`apiOrigin` is the origin (`scheme://host[:port]`, no path) a client uses for the
platform's GraphQL HTTP and WS endpoints, with `ws`/`wss` mapped to `http`/`https`. It
is derived from the same endpoint `wsUrl` comes from, so the two cannot disagree; if no
origin can be derived the request fails with a 502 rather than returning a guess.

The token is minted per request, so a page opened long after startup still gets a
live one. The last three fields are what an `external` far end needs to hold its own
MQTT session: the gateway's auth callout accepts only the client id
`{instanceId}:{tenant}:{deviceToken}`, `mqttBroker` is the address `dcctl sim create`
resolved, and `mqttTLSInsecure` is the trust decision it recorded. Two of the three
are also visible on `GET /status` — `instanceId` at the top level and `mqttBroker` as
`commandFarEnd.broker` — so they are here for a client's convenience, not because
this is the only way to reach them; only `mqttTLSInsecure` is served nowhere else.
`mqttTLSInsecure` is always present, including when it is `false` — an absent key
would be indistinguishable from a build that does not send one, and that is not a
distinction to leave to a client's default.

**This endpoint is unauthenticated, and the token in it is tenant-admin.** The sim
identity is provisioned with the `tenant-admin` role, so `token` is not a scoped
reader — it carries full create/read/delete over the tenant's devices, credentials
and dashboards. Together with `mqttBroker` and `instanceId` in the same body, anyone
who can GET it has what they need to impersonate any device on the MQTT plane. The
only control is the loopback `--bind` default. Widen it on a trusted host or not at
all.

**Device credentials are deliberately not served here**, and the reason is not that
the caller cannot get them — given the above, it can set its own. It is that they are
the one thing in reach that does not expire: a device's `ACCESS_TOKEN` bearer is derived
from the manifest seed and never rotates, so writing it into this body puts a permanent
secret somewhere an ephemeral one leaves on its own. The platform also stores an access
token only as a digest: reading a credential returns a fingerprint, and the token comes
back once, in the response to the create or update that sets it. So a client does not
read the sim's credential; it sets one of its own, using the token already in this body:
`devicesByExternalId` maps the scene's business id to a device token, and
`createDeviceCredential` (or `updateDeviceCredential`, to rotate an existing one) on a
credential named `{deviceToken}-<client>` with a token the client generated returns that
token in its response. It must not rotate the sim's own `{deviceToken}-cred`, which the
sim emitter presents. That also survives `POST /reset` re-provisioning the fleet, which
a snapshot taken at page load would not.

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
- `sim/emit.go` also holds `EventWire`, the seam that carries a scenario's events over a transport
  other than the HTTP ingress (`Runtime.Wire`), and the MQTT event body (which carries no
  credential); `sim/wire.go` has the payload builder both transports share.
- `otafleet/` — the `--ota` mode: the broker wiring and fault injection around the `updater` package (a fake device-side firmware updater), one update session per device.
- `x509sim/` — the `--x509` mode: test CA and device certificate minting, trust-anchor and credential registration, and the certificate-presenting MQTT connect.
- `mqttload/` — the MQTT fleet behind `dc-loadtest-mqtt`: one session per device, PUBACK-true
  accounting, jittered reconnect, observed TLS. `loadtest/mqtt.go` is its storage verdict.
  `mqttload/target.go` (the bring-your-own credential list) and `mqttload/tap.go` (the read-back
  subscriber) aim it at a broker that is not DeviceChain; `loadtest/target.go` is that run.
- `internal/dropproxy/` — the rehearsal's negative control: an MQTT proxy that PUBACKs every Nth
  publish and never forwards it (`cmd/loadtest-dropproxy`).
- `sim/towerpulse*.go` — the building-management scenario: the manifest and rules
  (`towerpulse.go`), the seeded thermal model and the command engine that answers a command
  only when its job is done (`towerpulse_model.go`), and the boards (`towerpulse_dashboard.go`, and the annex owner's `towerpulse_annex_dashboard.go`).
  `sim/windowrule.go` builds its windowed and absence rules, and `sim/customeruser.go`
  provisions a member limited to one customer.
- `sim/harborpulse*.go` — the waterfront scenario: the manifest and rules (`harborpulse.go`) and
  the compressed day, its planted faults and the command effects (`harborpulse_model.go`), and
  its boards (`harborpulse_dashboard.go`), and the LoRaWAN gauges, their frames and decoders
  (`harborpulse_lora.go`), and the fleet operations (`harborpulse_s4.go`; their provisioning is
  `sim/fleetops.go`). `sim/rulekinds.go` builds its duration, rate-of-change, repeating,
  correlation, geofence and delivery rules; `sim/outbound.go` provisions connectors, webhook
  signing handles, notification channels and policies and inbound integrations; `sim/scenario.go`
  is the scenario clock and options a scenario with a day takes.
- `scenarioverify/` and `cmd/scenario-verify` — the harborpulse oracle: the watch, the read-back
  and the invariants, each unit-tested against the defect that must turn it red.
- `scenariosink/` and `cmd/scenario-sink` — the in-cluster receiver of a scenario's webhooks, MQTT
  publishes and mail, which records them for the verifier.
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
