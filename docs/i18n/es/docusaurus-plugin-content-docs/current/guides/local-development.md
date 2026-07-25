---
sidebar_position: 1
title: Desarrollo local
---

# Desarrollo local

DeviceChain está diseñado para ejecutarse localmente con solo dos dependencias: **NATS** y **TimescaleDB**. Sin Java, Kafka, ZooKeeper, Redis, Keycloak ni Mosquitto.

:::note Estado
DeviceChain es pre-release. Esta guía describe el flujo de trabajo local previsto; consulta el README del [repositorio](https://github.com/devicechain-io/devicechain) para los pasos de configuración actuales y autoritativos.
:::

## Requisitos previos

- **Go** 1.25+
- **Node** 22 LTS (para el frontend y esta documentación)
- **Docker** (para ejecutar TimescaleDB)
- **nats-server** (un único binario de ~10 MB)

## 1. Iniciar la infraestructura

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17

# NATS with JetStream and the built-in MQTT server enabled
nats-server -js -m 8222
```

## 2. Compilar el workspace

El backend es un workspace de Go (`go.work`) que abarca la biblioteca principal (core), el operador y los servicios.

```bash
go build ./backend/...
```

## 3. Ejecutar un servicio

Cada servicio es un único binario. La configuración se suministra mediante variables de entorno / configuración; consulta el paquete `config` de cada servicio para ver los ajustes disponibles.

```bash
go run ./backend/services/event-sources
```

## Estructura del repositorio

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

## Próximos pasos

- [Conexión de un dispositivo](./connecting-a-device.md)
- [Arquitectura](../concepts/architecture.md)
