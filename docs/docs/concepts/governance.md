---
title: Governance & Quotas
---

# Governance & Quotas

DeviceChain runs [one shared set of services for all tenants](./multi-tenancy.md), and tenant isolation there is about **correctness** — one tenant can never see another's data. Governance is the other half of that bet: **fairness**. Per-tenant quotas ensure that one tenant's burst, reconnect storm, or misconfigured rule cannot exhaust the capacity every tenant shares. Data isolation without resource fairness still lets one fleet degrade everyone; governance closes that gap.

Limits are enforced **at the edges**, before traffic reaches shared infrastructure:

- **Ingest** — a per-tenant rate limit in the event-sources service, applied as device traffic is decoded, before it is published onto the internal pipeline. An over-limit tenant's excess is shed at the front door instead of backing up the shared stream.
- **Egress** — outbound volume from [REACT actions](./outbound-connectors.md#governance) is rate-limited per tenant at both ends of the hop: the detection engine sheds over-budget emissions before dispatch, and the outbound-connectors service admits sink traffic within a bounded budget. Both ends meter an action on the time the telemetry that triggered it reached the platform, so a backlog drained after a restart is charged as it happened.
- **AI inference** — the opt-in AI service applies a per-tenant rate limit, so one tenant's authoring sessions cannot monopolize the shared inference path. Inference spend is observable — the input and output tokens the provider reports are counted as instance-wide metrics an operator can watch and alert on — but it is not tracked per tenant, and no budget is enforced against it: the rate ceiling is the only thing that bounds a tenant here.
- **Undelivered commands** — a per-tenant ceiling on how many commands may be waiting to go out at once, enforced as a command is enqueued. It is the one limit here that **refuses** rather than sheds: the three above drop a tenant's excess traffic, while this one returns a rejection the caller can see and retry — a command is a physical actuation, so quietly dropping one is not available. See [how much backlog a tenant may hold](./commands.md#held-command-ceiling).

All enforcement points resolve limits through one shared **governance library** in the platform core — a single per-tenant limit fetcher/resolver — so every dimension answers the "what is this tenant allowed?" question the same way.

## The fail-safe rule

The load-bearing safety property, stated exactly:

> A missing or zero limit resolves to the **platform default** — never to unlimited.

There is no configuration state, and no failure mode, in which a tenant becomes ungoverned. A tenant with no explicit limit gets the platform-default ceiling; a limit set to zero means the same, not "no limit". Fail-*open* governance — where a typo or an absent row quietly removes a ceiling — is exactly the failure this design forbids, and it is the same fail-closed posture the [tenant data scope](./multi-tenancy.md#isolation) takes on the correctness side.

### Before a tenant's ceiling is known {#unresolved-ceilings}

A service learns a tenant's ceilings from the control plane and caches them. Until it has read them (the first time it sees a tenant after it starts, or for as long as the control plane is unreachable), it meters that tenant at its **platform default**. That default is still a ceiling, never unlimited, but for a tenant whose tier sets a lower one it admits more than the tier allows until the read succeeds. A tenant whose ceilings were already read keeps its last-known values through an outage.

Each enforcing service counts the traffic it admitted this way on `devicechain_<service>_governance_unresolved_admissions_total`, labelled by dimension and cause:

- `unreachable`: the control plane could not be asked;
- `unknown-tenant`: it answered that no such tenant exists;
- `pending`: no answer yet.

The `TenantsMeteredAtPlatformDefault` alert fires only when `unreachable` keeps rising for 15 minutes.

### Tenant names that cannot be confirmed {#unconfirmed-tenants}

The HTTP ingest endpoint takes the tenant from the request path, before any device credential is checked. A tenant name arriving there gets an allowance of its own only if the control plane has confirmed it, or from a fixed set of 1024. Past that set, all such names share one allowance at the platform default, and the `RateLimiterOverflowInUse` alert fires. MQTT, NATS and LwM2M traffic comes from a source that is authenticated or that the operator chose to trust: the platform broker authenticates each device, LwM2M checks the device's key, and an external MQTT broker source is trusted because the operator configured it. That traffic always gets its own allowance.

The set bounds the service's memory, not the total admitted across invented names: up to 1024 times the platform default can be admitted across them.

## Where limits live

Governance limits are **operator and tenant configuration**, not client input:

- They are declared on the tenant's **control-plane record** and edited through the admin console and control-plane API.
- They are **never a token claim**. A caller's JWT identifies the tenant; the enforcing service then resolves that tenant's limits from configuration. Nothing a client sends — headers, claims, payloads — can raise its own ceiling.

## Tiers supply the ceilings

A tenant's governance ceilings come from its **[tier](./tenant-tiers.md)** — the operator-defined packaging entity that answers "what kind of customer is this?". The tier is where an operator packages *how much*: the default ceilings a class of tenants inherits. Resolution follows a three-level cascade:

**per-tenant override → tier setting → platform default**

Per-tenant overrides are audited exceptions, not the mechanism — the tier carries the packaged answer, and the platform default is the floor the fail-safe rule guarantees. The same cascade governs AI model entitlement — a per-tenant model assignment, then the model the tenant's tier marks as its default — so "which tier is this tenant on?" answers one consistent question across the governance and AI subsystems. Tenant branding cascades too, but with no tier level in it: a tenant's override, then the operator's `branding.default` system setting, then the shipped default.

## Ceilings are per replica {#per-replica}

Every rate ceiling on this page is enforced by each running copy of the service that enforces it, with no coordination between copies. If you run two replicas of `event-sources`, `outbound-connectors` or `ai-inference` and they share a tenant's traffic, that tenant can be admitted at up to twice its ceiling, and N replicas allow up to N times. The default install runs one replica of each, and there the ceiling is exact. Two ceilings are not multiplied: the undelivered-command ceiling is a count kept in the database, and the detection engine's outbound ceiling is charged only on the replica that detects. If you scale a service out, set tier ceilings for the number of replicas you run.

## Seeing it work

Shed volume is surfaced as an operational metric, so a tenant that has hit a ceiling — or a rule that has started to over-emit — is visible to an operator before it becomes a support ticket. Governance is meant to be observable pressure, not silent loss.

:::note Status
**Enforced today:** per-tenant ingest rate limiting (event-sources), outbound egress governance at both ends of the REACT hop (event-processing + outbound-connectors), and per-tenant AI-inference rate limiting with spend observability — all through the shared core governance resolver, all subject to the fail-safe platform-default rule. Also enforced, on the same cascade and the same fail-safe rule but by refusing rather than shedding: the per-tenant undelivered-command ceiling (command-delivery). **Planned behind the same model:** per-tenant API/query governance, per-tenant stream bounds on the internal bus, and a relationship fan-out ceiling.
:::

## Related

- **[Multi-Tenancy](./multi-tenancy.md)** — the correctness half: fail-closed data isolation on the same shared instance.
- **[Outbound Connectors](./outbound-connectors.md#governance)** — how egress governance applies to webhooks and broker publishes.
- **[Tenant Tiers](./tenant-tiers.md)** — the packaging entity that supplies a tenant's default ceilings.
