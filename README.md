<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branding/logos/logo.svg">
    <img alt="DeviceChain" src="branding/logos/logo-light.svg" width="300">
  </picture>
</p>

**An open-source, self-hosted IoT platform, written in Go and React, that runs on Kubernetes.**

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/devicechain)](https://artifacthub.io/packages/search?repo=devicechain)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/devicechain-io/devicechain/badge)](https://scorecard.dev/viewer/?uri=github.com/devicechain-io/devicechain)

DeviceChain™ takes in telemetry from device fleets, keeps a model of the devices and what they
are attached to, detects conditions in the data as it arrives, and sends commands back. One
installation serves many tenants. It is meant for teams that build an IoT product or run a
fleet and want to host the platform themselves, in their own cluster, with their own data.

DeviceChain is a rebuild of the [SiteWhere](https://github.com/sitewhere/sitewhere) IoT platform.
It keeps SiteWhere's domain model and replaces the Java and Spring stack with Go services.

## What it does

Devices send data over any of these transports:

- MQTT, through the broker built into NATS (port 1883)
- HTTP, as a `POST` of the same JSON event body
- [Eclipse Sparkplug B](https://docs.devicechain.io/concepts/sparkplug), with DeviceChain joining
  your existing Sparkplug broker as a Host Application
- [OMA LwM2M](https://docs.devicechain.io/concepts/lwm2m) over CoAP/UDP with DTLS

Not every transport carries every direction. HTTP is ingest only, Sparkplug devices cannot be
commanded, and LwM2M telemetry is decoded from SenML-JSON only. The
[transport matrix](https://docs.devicechain.io/reference/transport-matrix) lists what each one
does and what it lacks.

Each device has a device type, and each type points to a versioned device profile that defines
its metrics, commands and detection rules. Profiles go through draft, publish and rollback, so a
change to a fleet's behaviour is published in one step and can be undone. Devices, assets,
areas and customers are joined by typed relationships, and the relationships you mark as
tracked are written onto each event, so a query for everything in one building needs no join.
See the [domain model](https://docs.devicechain.io/concepts/domain-model).

Detection rules run in the event-processing service as events arrive. There are eight condition
types, among them thresholds, conditions held for a duration, rate of change, silence, windowed
aggregates and conditions shared by several devices in one area. A rule can raise an alarm, send
a command to a device, call a webhook, or publish to MQTT, Kafka, AWS SNS or AWS SQS. Rules are
authored as forms, on a visual canvas that can replay history against a draft, or from a plain
language description when the optional AI service is enabled. Alarms reach people by email or
webhook, with escalation. See [event processing and alarms](https://docs.devicechain.io/concepts/event-processing).

Commands are validated against the device's profile, then tracked until the device reports the
result or the command's time-to-live runs out
([commands](https://docs.devicechain.io/concepts/commands)). Dashboards are versioned per tenant
and render with React widget packages published to npm, which you can embed in your own
application ([dashboards](https://docs.devicechain.io/concepts/dashboards)).

Every external API is GraphQL. The schemas are published with the docs
([index](https://docs.devicechain.io/schema/index.json), [`llms.txt`](https://docs.devicechain.io/llms.txt)),
because introspection is off by default. Client libraries are the `@devicechain/client`
TypeScript package on npm and the `DeviceChain.Sdk` .NET package on NuGet, which also targets
Unity. Telemetry lives in TimescaleDB, so BI tools and `psql` can read it through a read-only
analytics schema ([SQL and BI access](https://docs.devicechain.io/guides/sql-and-bi-access)). An
optional [MCP server](https://docs.devicechain.io/concepts/mcp) gives AI assistants read-only
access to a tenant, under the signed-in user's own token.

## How it runs

DeviceChain is a set of Go services on Kubernetes. NATS JetStream carries messaging, device MQTT
connections and key-value state. PostgreSQL holds entity data, and a second PostgreSQL database
with the TimescaleDB extension holds events. Each tenant's data is separated by a tenant column
that every query is scoped to, and by per-tenant messaging subjects; tenants share one set of
services and get no pods of their own
([multi-tenancy](https://docs.devicechain.io/concepts/multi-tenancy)).

A default install is sized to keep up with each tenant's default ingest limit of 1,000 messages a
second. The `--ha` flag on `dcctl install` runs the message broker as a three-node cluster with
every stream replicated, and both databases as three-instance clusters. Such an install survives
the loss of one node. The [architecture](https://docs.devicechain.io/concepts/architecture),
[bootstrap](https://docs.devicechain.io/deployment/bootstrap) and
[disaster recovery](https://docs.devicechain.io/deployment/disaster-recovery) pages cover sizing,
high availability and restore, including what has not been through a restore drill yet.

## Status

The current release is v0.18.0. DeviceChain has not reached 1.0, and until it does any release
may change APIs, schemas or behaviour without a compatibility period. Each release lists its
breaking changes first, in the
[release notes](https://github.com/devicechain-io/devicechain/releases) and the
[upgrade guide](https://docs.devicechain.io/deployment/releases-and-upgrades). Read them before
you upgrade.

`dcctl install local` and `dcctl bootstrap local` are tested end to end on kind. On other
clusters, `dcctl install` works against an existing kube-context. A `gcp` provider is planned
and not available yet.

## Install

`dcctl` is the command-line tool that installs and runs DeviceChain. Download it for Linux, macOS
or Windows from the [releases page](https://github.com/devicechain-io/devicechain/releases). It
carries the Helm chart, the operator manifests and the OpenTofu configuration inside it, so you
need no source checkout. It does drive these tools, which must be on your `PATH`:

- `docker`
- `kubectl`
- `helm`
- OpenTofu (`tofu`) or `terraform`
- `kind`, for the local provider

The cluster must run Kubernetes 1.29 or newer. `dcctl preflight local` checks all of this without
changing anything.

```bash
# Prepare the cluster once. This creates a kind cluster named "devicechain" if there is none,
# and installs the operator, the relational database, CloudNativePG, cert-manager, monitoring
# and ingress.
dcctl install local

# Create an instance served at http://localhost/.
dcctl bootstrap local devicechain --host localhost --no-tls
```

Bootstrap prints the console URL and a generated password for `superuser@devicechain.local`.
[Your first device](https://docs.devicechain.io/quickstart/first-device) continues from here:
it creates a tenant, sends a reading with `curl` and shows it in the console.

To remove the instance, run `dcctl destroy local devicechain`. The cluster and what
`dcctl install` put on it stay; `kind delete cluster --name devicechain` removes them.

To build `dcctl` from source, run `make build` in `backend/cli`. A binary you build yourself has
no default image version, so pass `--build` to build the images from source or `--version vX.Y.Z`
to deploy a published release.

## Documentation

The documentation is at [docs.devicechain.io](https://docs.devicechain.io). Its source is in
[`docs/`](docs/). Good places to start:

- [Your first device](https://docs.devicechain.io/quickstart/first-device)
- [Connecting a device](https://docs.devicechain.io/guides/connecting-a-device)
- [GraphQL API](https://docs.devicechain.io/reference/graphql-api)
- [Releases and upgrades](https://docs.devicechain.io/deployment/releases-and-upgrades)
- [Local development](https://docs.devicechain.io/guides/local-development), for working on the
  source

## Getting help and contributing

Ask questions and propose ideas in
[Discussions](https://github.com/devicechain-io/devicechain/discussions). Report bugs through
[issues](https://github.com/devicechain-io/devicechain/issues/new/choose), which have short forms
for install failures, missing telemetry and docs problems. A report of where you got stuck is
useful, however rough. Report security vulnerabilities privately to **admin@devicechain.io**
(see [SECURITY.md](SECURITY.md)).

To contribute code, read [CONTRIBUTING.md](CONTRIBUTING.md). Contributors sign a CLA before a
change can be merged.

## License

DeviceChain is licensed under the [Apache License 2.0](LICENSE) (see also [NOTICE](NOTICE)).
There is no separate commercial edition: high availability, multi-tenancy, command delivery and
the OAuth 2.1 authorization server are all in this repository. Copyright is held by The
DeviceChain Authors.

"DeviceChain" and the DeviceChain logo are trademarks of IoT Innovations, LLC (U.S. application
pending, USPTO Serial No. 99910096). The Apache 2.0 license covers the code, not the marks. See
[TRADEMARK.md](TRADEMARK.md) for the trademark policy.
