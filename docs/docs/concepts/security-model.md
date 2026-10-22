---
title: Security Model
---

# Security Model {#security-model}

This page states where DeviceChain draws its trust boundaries, what enforces each one today, and what it
does not yet enforce. The accepted gaps are listed in their own section so that you can plan around them.

To report a vulnerability, follow the process in the
[security policy](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md).

## Trust boundaries at a glance {#boundaries}

| Boundary | What separates the two sides | Enforced by |
| --- | --- | --- |
| Tenant from tenant | Every tenant-owned row and every message subject carries the tenant | A database scope on every query, and per-tenant subjects at the broker |
| Device from device | A device connection can use only its own subjects | The broker, through a platform authorization callback |
| Person or program from the API | A signed token, checked in every service | Token signature, tenant claim and permissions |
| Secret from reader | Secrets are sealed and never returned | Envelope encryption under the instance root key |
| Tenant from the network | A tenant-supplied destination cannot reach internal addresses | A check at connection time |
| Platform services from each other | Separate broker and database logins | **Partly.** See [what is not enforced yet](#accepted-gaps) |

## Tenants {#tenants}

One set of services serves every tenant, so isolation is enforced in the data and the messages, not by separate pods.

- **Storage.** Tenant-owned tables carry the tenant, and a central database scope adds the tenant condition to every read and
  stamps it on every write. A tenant-scoped query that has no tenant in its context is rejected, not run unfiltered.
- **Messaging.** Subjects are scoped per tenant, and a malformed tenant segment is rejected where a service publishes or subscribes.
- **API.** The tenant of a request is taken from the tenant claim of the caller's verified token. The one exception is a
  service calling another service: it names the tenant in a header, which is honored only after the
  service token's signature is verified.

See [Multi-Tenancy](./multi-tenancy.md).

## Devices {#devices}

- **Transport.** The MQTT and NATS listeners use TLS.
- **Identity is asserted by the broker.** The broker asks the platform to approve each connection, and the platform
  binds the connection to that one device's subjects. The device identity the platform trusts for an event is the
  subject the broker delivered it on, not a claim in the payload. A device cannot publish into another device's or
  another tenant's subjects, or read their commands. The MQTT client id must also begin with the device's own
  `{instanceId}:{tenant}:{deviceToken}`, so one device cannot take over another's session.
- **Credentials.** The default device-auth mode is `required`: an event must present a valid credential, and an
  authenticated device cannot send events as a different device. Disabling or deleting a credential stops new
  connections at once. See [Device credentials](../guides/device-credentials.md), including how quickly a
  revocation reaches events already in flight.
- **Credentials at rest.** The password of an `MQTT_BASIC` credential is stored only as a keyed one-way digest, shown once
  when you set it, and never returned. An `ACCESS_TOKEN` credential is different today: the token is the
  credential's identifier and is stored as a value the platform can read back. A leak of the database or of a
  backup would expose the tokens of every token-authenticated device. Treat such backups accordingly, and
  prefer `MQTT_BASIC` where you can.
- **Credentials in transit.** The password or token that arrives with an event is sealed before the event is put
  on the platform's internal stream, and the copy of an undecodable message that is kept for diagnosis has its
  credentials removed.

## People and programs {#api-access}

- **Tokens.** Sign-in issues RS256-signed access and refresh tokens. Services validate them locally against a published
  key set, so no request waits on a call to the sign-in service. Signing keys are held by `user-management` alone, sealed under the
  instance root key.
- **Permissions.** Roles carry permissions, and the API checks them on each call. A membership can be limited to a
  customer's data, and members, invitations and roles a tenant manages for itself cannot grant more than the person
  granting them holds. See [Governance & Quotas](./governance.md).
- **Single sign-on.** A tenant can require sign-in through its own identity provider. See
  [Single sign-on](./single-sign-on.md).
- **AI agents.** The optional [MCP server](./mcp.md) calls the platform with the caller's own token. It holds no token of
  its own, so an agent can read only what its user can read.
- **Abuse limits.** Ingest has a per-tenant rate ceiling, outbound connectors have per-tenant ceilings, the MCP server
  limits each caller, GraphQL requests have bounded execution and body size, and failed sign-in attempts are rate limited.
  There is no per-tenant request-rate limit on the GraphQL API itself.

## Secrets {#secrets}

Credentials a tenant stores, such as connector settings and identity-provider client secrets, are kept in an
encrypted secret store. Each value is sealed with its own AES-256-GCM data key, and that key is wrapped by the
instance **root key**, which lives in the instance's Kubernetes Secret. A value is write-only over the API: it is
resolved inside the server when it is used and never returned. Changes are audited by who, when and which handle, never by value.

The root key is the one thing the store cannot recover from. Keep an escrow copy: without it, a restored instance
cannot decrypt its secrets and no one can sign in. See [Disaster Recovery](../deployment/disaster-recovery.md).
The store takes a pluggable key provider, but the instance root key is the only provider that ships today.

## Outbound traffic {#egress}

Webhooks, mail relays and outbound connectors send to destinations a tenant supplies. Every such connection is checked at the moment it is made,
against the address the name actually resolved to, so a name that changes its answer between a check and a
connection cannot get past it. Loopback, private, carrier-grade NAT, link-local and cloud-metadata addresses are refused, and a
refused destination is a permanent failure, not a retry. An operator can permit specific private
addresses. The chart's `networkPolicy` is a second layer, off by default. See
[Outbound Connectors](./outbound-connectors.md).

## Audit {#audit}

The platform records a journal entry for every create, update and delete it makes to a tenant's registry, with the
acting identity, and for sign-in events such as logins, failed logins and single sign-on outcomes. If a registry
entry cannot be written, the change fails. Entries are never edited, and a tenant sees only its own. Changes to
connectors, dashboards, rules and updates are not in this journal, and the journal is not pruned
automatically. See [Audit](../user-guide/audit.md).

## What is not enforced yet {#accepted-gaps}

These are known and accepted for the current release. Plan around them.

1. **The platform services share one broker login today.** Every service connects to the broker with the same login. The login is
   refused the subjects only a device may write, which stops a service from publishing a device event by
   mistake, but it can also administer the broker's streams, and a stream can be configured to write into
   those subjects. A compromised service could therefore forge device events, command responses and device
   reports for any tenant. Giving each service its own login, limited to the subjects and streams it needs, is
   being built, and this entry will change when it ships. Until then, treat the services that accept device
   traffic as part of the trusted core of the platform.
2. **The database login is shared by all services in an instance.** Every service reaches the instance's relational
   database with one login, so a compromised service could read and write another area's tables. Tenant isolation
   in the database rests on the application's tenant scope, not on database permissions. Separate roles per
   service are planned for a release after 1.0.
3. **Access tokens are stored readable**, as described under [Devices](#devices).
4. **MQTT acknowledgements across a broker leader change.** By default the broker can acknowledge a QoS 1 message and
   then lose it if the capture stream changes leader. With `infrastructure.nats.mqttFailClosed` on, an
   acknowledgement means the platform holds the message. The setting is off by default, and
   [Observability](../deployment/observability.md) describes the trade-off.
5. **The root key is readable by anyone who can read the instance's Kubernetes Secret.** Restrict access to that
   Secret and to the cluster's etcd accordingly.
6. **No external penetration test has been published.**

Nothing on this page replaces your own review. If an entry here is a problem for your deployment, the matching
section above says where the control sits.
