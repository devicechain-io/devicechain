---
sidebar_position: 1
title: Local Development
---

# Local Development

DeviceChain is designed to run locally with only two dependencies: **NATS** and **TimescaleDB**. No Java, Kafka, ZooKeeper, Redis, Keycloak, or Mosquitto.

:::note Status
DeviceChain is pre-release. This guide covers working on the source tree — building the Go
workspace and running a single service against dependencies you started yourself. If you
want a **complete running instance** instead, use `dcctl` and the
[Quickstart](../quickstart/first-device.md); it stands up everything in one command.
:::

## Prerequisites

- **Go** 1.26 or newer — the workspace declares `go 1.26.6`, and CI builds with the version `go.work` names
- **Node** 22 or newer (for the frontend and these docs; CI builds on 26)
- **Docker** (to run TimescaleDB)
- **nats-server** (a single ~10 MB binary)

## 1. Start the infrastructure

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17
```

NATS needs JetStream, and — if you want to connect a device over MQTT — the broker's
built-in MQTT gateway. There is **no command-line switch for MQTT**: it is a configuration
block, and it requires JetStream (a clustered broker must also set `server_name`). So write
a small config file rather than passing flags:

```bash
cat > nats.conf <<'EOF'
server_name: dc-local
jetstream: enabled
http_port: 8222
mqtt { port: 1883 }
EOF

nats-server -c nats.conf
```

The server logs `Listening for MQTT clients on mqtt://0.0.0.0:1883` when the gateway is up.
If you only need core messaging and JetStream, `nats-server -js -m 8222` is enough — but it
starts **no** MQTT listener.

## 2. Build the workspace

The backend is a Go workspace (`go.work`) spanning the core library, the operator, the CLI,
and the services. Builds are **module-scoped**: the repository root is not itself a Go
module, so a `./...` pattern anchored there matches nothing —

```
pattern ./...: directory prefix . does not contain modules listed in go.work or their selected dependencies
```

— and neither `go build ./...` nor `go build ./backend/...` works from the top of the tree.
Build from inside the module you are working on, which is also what CI does:

```bash
cd backend/core     # ...or whichever module you touched
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./... -count=1
```

To sweep the whole workspace, let `go.work` enumerate its own modules instead of listing
them by hand:

```bash
rc=0
for m in $(go list -m -f '{{.Dir}}'); do
  ( cd "$m" || exit 1
    fmt="$(gofmt -l .)"; [ -z "$fmt" ] || { echo "not gofmt-clean:"; echo "$fmt"; exit 1; }
    go build ./... && go vet ./... && go test ./... -count=1
  ) || { echo "FAILED: $m"; rc=1; }
done
echo "sweep exit status: $rc"
```

Three details in that loop are load-bearing, and each is a check that would otherwise pass
without looking at anything:

- **`gofmt -l` is captured, not just run.** It exits 0 *even when it names files*, so its
  **output** is what has to be tested — `[ -z "$fmt" ]`. Calling it for its exit status
  would make it a gate that can never fail.
- **`-count=1` is not belt-and-braces.** A few tests read files **outside** their own
  module, which Go's test cache does not track, so a cached PASS can survive a change that
  ought to fail it.
- **`rc` is recorded rather than just printed.** `… || echo "FAILED: $m"` alone would leave
  the loop's exit status that of the last `echo` — every module could fail and the sweep
  would still look green.

## 3. Run a service

Each service is a single binary. Configuration is supplied via environment / config; see each service's `config` package for the available settings.

```bash
go run ./backend/services/event-sources
```

(`go run` takes a path to one package, so it resolves inside that module and works from the
repository root — unlike the `./...` patterns above.)

## Repository layout

```
backend/
  core/                 shared library (lifecycle, NATS, GORM, GraphQL, config, auth, secrets)
  k8s/                  operator + CRD types
  services/             one module per microservice — user-management, device-management,
                        event-sources, event-management, device-state, command-delivery,
                        dashboard-management, notification-management, event-processing,
                        outbound-connectors, ai-inference, mcp, and the edge ingest areas
  edge/                 the edge agent
  sims/                 the device simulator
  cli/                  dcctl
  tools/                maintainer-only tools (not shipped)
frontend/               npm workspace: the console and dashboard apps plus the shared packages
docs/                   this documentation site
deploy/                 Helm chart + OpenTofu modules
sdks/                   client SDKs
```

## Next steps

- [Connecting a Device](./connecting-a-device.md)
- [Architecture](../concepts/architecture.md)
