---
title: Governance & Quotas
---

# Governance & Quotas

DeviceChain runs [one shared set of services for all tenants](./multi-tenancy.md). Tenant isolation on that shared instance is about **correctness**: one tenant can never see another's data. Governance is about **fairness**. Per-tenant quotas stop one tenant's burst, reconnect storm or misconfigured rule from exhausting the capacity every tenant shares. Isolating data without sharing resources fairly would still let one fleet degrade everyone, and governance closes that gap.

Limits are enforced at the edges, before traffic reaches shared infrastructure:

- **Ingest.** The event-sources service applies a per-tenant rate limit as device traffic is decoded, before it is published onto the internal pipeline. An over-limit tenant's excess is shed at the front door instead of backing up the shared stream.
- **Egress.** Outbound volume from [actions](./outbound-connectors.md#governance) is rate-limited per tenant at both ends of the hop. The detection engine sheds over-budget emissions before dispatch, and the outbound-connectors service admits sink traffic within a bounded budget. Both ends meter an action on the time the telemetry that triggered it reached the platform, so a backlog drained after a restart is charged as it happened.
- **AI inference.** The opt-in AI service applies a per-tenant rate limit, so one tenant's authoring sessions cannot monopolize the shared inference path. You can observe inference spend: the input and output tokens the provider reports are counted as instance-wide metrics an operator can watch and alert on. Spend is not tracked per tenant, and no budget is enforced against it. The rate ceiling is the only thing that bounds a tenant here.
- **Undelivered commands.** A per-tenant ceiling caps how many commands may be waiting to go out at once, enforced as a command is enqueued. This is the one limit here that **refuses** rather than sheds. The three above drop a tenant's excess traffic; this one returns a rejection the caller can see and retry, because a command is a physical actuation and quietly dropping one is not an option. See [how much backlog a tenant may hold](./commands.md#held-command-ceiling).

Every enforcement point resolves limits through one shared governance library in the platform core, a single per-tenant limit fetcher and resolver. So every dimension answers "what is this tenant allowed?" the same way.

## The fail-safe rule

This is the safety property the rest of governance depends on:

> A missing or zero limit resolves to the **platform default** — never to unlimited.

No configuration state and no failure mode leaves a tenant ungoverned. A tenant with no explicit limit gets the platform-default ceiling, and a limit set to zero means the same thing, not "no limit". The design forbids governance that fails open, where a typo or an absent row quietly removes a ceiling. The [tenant data scope](./multi-tenancy.md#isolation) takes the same stance on the correctness side: it refuses a query when the tenant is missing.

### Before a tenant's ceiling is known {#unresolved-ceilings}

A service learns a tenant's ceilings from the control plane and caches them. Until it has read them, it meters that tenant at its **platform default**. That happens the first time it sees a tenant after it starts, and for as long as the control plane is unreachable. The default is still a ceiling, never unlimited. But for a tenant whose tier sets a lower one, it admits more than the tier allows until the read succeeds. A tenant whose ceilings were already read keeps its last-known values through an outage.

Each enforcing service counts the traffic it admitted this way on `devicechain_<service>_governance_unresolved_admissions_total`, labelled by dimension and cause:

- `unreachable`: the control plane could not be asked;
- `unknown-tenant`: it answered that no such tenant exists;
- `pending`: no answer yet.

The `TenantsMeteredAtPlatformDefault` alert fires only when `unreachable` keeps rising for 15 minutes.

### Tenant names that cannot be confirmed {#unconfirmed-tenants}

The HTTP ingest endpoint takes the tenant from the request path, before any device credential is checked. So HTTP ingest has an allowance of its own for each tenant, separate from the one the tenant's MQTT, NATS and broker presence traffic spends. HTTP requests naming a tenant cannot use up that tenant's device traffic.

Anyone who can reach the HTTP port and knows a tenant's name can still use up that tenant's HTTP allowance, because the device credential is checked only after the request is admitted. Within the HTTP allowance, a tenant name gets an allowance of its own only if the control plane has confirmed it, or from a fixed set of 1024. Past that set, all such names share one allowance at the platform default, and the `RateLimiterOverflowInUse` alert fires.

The set bounds the service's memory, not the total admitted across invented names: up to 1024 times the platform default can be admitted across them.

MQTT, NATS and LwM2M traffic comes from a source that is authenticated or that the operator chose to trust, and it always gets its own allowance:

- the platform broker authenticates each device;
- LwM2M checks the device's key;
- an external MQTT broker source is trusted because the operator configured it.

## Where limits live

Governance limits are operator and tenant configuration, not client input:

- They are declared on the tenant's **control-plane record** and edited through the admin console and control-plane API.
- They are **never a token claim**. A caller's JWT identifies the tenant, and the enforcing service then resolves that tenant's limits from configuration. Nothing a client sends — headers, claims, payloads — can raise its own ceiling.

## Tiers supply the ceilings

A tenant's governance ceilings come from its **[tier](./tenant-tiers.md)**, the operator-defined packaging entity that answers "what kind of customer is this?". The tier is where an operator packages *how much*: the default ceilings a class of tenants inherits. Resolution follows a three-level cascade:

**per-tenant override → tier setting → platform default**

Per-tenant overrides are audited exceptions, not the main mechanism. The tier carries the packaged answer, and the platform default is the floor the fail-safe rule guarantees.

The same cascade governs AI model entitlement: a per-tenant model assignment, then the model the tenant's tier marks as its default. So "which tier is this tenant on?" answers one consistent question across governance and AI. Tenant branding cascades too, but with no tier level: a tenant's override, then the operator's `branding.default` system setting, then the shipped default.

## Ceilings are per replica {#per-replica}

Each running copy of a service enforces every rate ceiling on this page on its own, with no coordination between copies. If you run two replicas of `event-sources`, `outbound-connectors` or `ai-inference` and they share a tenant's traffic, that tenant can be admitted at up to twice its ceiling, and N replicas allow up to N times. The default install runs one replica of each, and there the ceiling is exact.

Two ceilings are not multiplied:

- the undelivered-command ceiling is a count kept in the database;
- the detection engine's outbound ceiling is charged only on the replica that detects.

If you scale a service out, set tier ceilings for the number of replicas you run.

### When ingest can admit a tenant above its ceiling {#ingest-above-ceiling}

Within one `event-sources` replica, a tenant's ingest ceiling applies to each of three allowances separately, not to the tenant as a whole:

- **Live traffic**: what the tenant's devices send now over MQTT and NATS, including broker presence.
- **Backlog**: messages the platform broker stored while `event-sources` was down or behind, metered by when they were sent as they drain. A tenant draining a backlog after an outage while also sending live can be admitted up to twice its ceiling until the drain catches up.
- **HTTP ingest**: metered separately, so a tenant sending over HTTP and MQTT at once can be admitted up to its ceiling on each.

In the worst case a tenant can be admitted at three times its ceiling on each replica, multiplied by the number of replicas as above.

## Observing governance {#seeing-it-work}

Shed volume is reported as an operational metric. An operator sees a tenant that has hit a ceiling, or a rule that has started to over-emit, before it becomes a support ticket. Governance is meant to show up as observable pressure, not silent loss.

:::note Status
**Enforced today:** per-tenant ingest rate limiting (event-sources), outbound egress governance at both ends of the action hop (event-processing and outbound-connectors), per-tenant AI-inference rate limiting with spend observability, and the per-tenant undelivered-command ceiling (command-delivery). All of them follow the fail-safe rule above.

**Planned on the same model:** per-tenant API/query governance, per-tenant stream bounds on the internal bus, and a relationship fan-out ceiling.
:::

## Related

- **[Multi-Tenancy](./multi-tenancy.md)**: the correctness half, data isolation that refuses unscoped queries on the same shared instance.
- **[Outbound Connectors](./outbound-connectors.md#governance)**: how egress governance applies to webhooks and broker publishes.
- **[Tenant Tiers](./tenant-tiers.md)**: the packaging entity that supplies a tenant's default ceilings.
