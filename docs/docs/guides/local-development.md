---
sidebar_position: 1
title: Local Development
---

# Local Development

DeviceChain is designed to run locally with only two dependencies: **NATS** and **TimescaleDB**. No Java, Kafka, ZooKeeper, Redis, Keycloak, or Mosquitto.

:::note Status
DeviceChain is pre-release. This guide describes the intended local workflow; check the [repository](https://github.com/devicechain-io/devicechain) README for the current, authoritative setup steps.
:::

## Prerequisites

- **Go** 1.25+
- **Node** 22 LTS (for the frontend and these docs)
- **Docker** (to run TimescaleDB)
- **nats-server** (a single ~10 MB binary)

## 1. Start the infrastructure

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17

# NATS with JetStream and the built-in MQTT server enabled
nats-server -js -m 8222
```

## 2. Build the workspace

The backend is a Go workspace (`go.work`) spanning the core library, the operator, and the services.

```bash
go build ./backend/...
```

## 3. Run a service

Each service is a single binary. Configuration is supplied via environment / config; see each service's `config` package for the available settings.

```bash
go run ./backend/services/event-sources
```

## Repository layout

```
backend/
  core/                 shared library (lifecycle, NATS, GORM, GraphQL, config, auth)
  k8s/                  operator + CRD types
  services/             event-sources, device-management, event-management, user-management, ...
  cli/                  dcctl
frontend/               React + Vite management UI
docs/                   this documentation site
deploy/                 Helm charts + OpenTofu modules
```

## Next steps

- [Connecting a Device](./connecting-a-device.md)
- [Architecture](../concepts/architecture.md)
