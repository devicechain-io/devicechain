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
4. **Emits** a `speed_kph` measurement every ~5s over the real device-plane
   HTTP ingress (`POST /{instanceId}/{tenant}/events`), authenticated by the
   provisioned credential — exactly the path a physical device uses.
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
