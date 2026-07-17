---
sidebar_position: 12
title: Governance & Quotas
---

# Governance & Quotas

DeviceChain runs [one shared set of services for all tenants](./multi-tenancy.md), and tenant isolation there is about **correctness** — one tenant can never see another's data. Governance is the other half of that bet: **fairness**. Per-tenant quotas ensure that one tenant's burst, reconnect storm, or misconfigured rule cannot exhaust the capacity every tenant shares. Data isolation without resource fairness still lets one fleet degrade everyone; governance closes that gap.

Limits are enforced **at the edges**, before traffic reaches shared infrastructure:

- **Ingest** — a per-tenant rate limit in the event-sources service, applied as device traffic is decoded, before it is published onto the internal pipeline. An over-limit tenant's excess is shed at the front door instead of backing up the shared stream.
- **Egress** — outbound volume from [REACT actions](./outbound-connectors.md#governance) is rate-limited per tenant at both ends of the hop: the detection engine sheds over-budget emissions before dispatch, and the outbound-connectors service admits sink traffic within a bounded budget.
- **AI inference** — the opt-in AI service applies a per-tenant rate limit and tracks per-tenant spend, so one tenant's authoring sessions cannot monopolize (or silently run up) the shared inference path.

All enforcement points resolve limits through one shared **governance library** in the platform core — a single per-tenant limit fetcher/resolver — so every dimension answers the "what is this tenant allowed?" question the same way.

## The fail-safe rule

The load-bearing safety property, stated exactly:

> A missing or zero limit resolves to the **platform default** — never to unlimited.

There is no configuration state, and no failure mode, in which a tenant becomes ungoverned. A tenant with no explicit limit gets the platform-default ceiling; a limit set to zero means the same, not "no limit". Fail-*open* governance — where a typo or an absent row quietly removes a ceiling — is exactly the failure this design forbids, and it is the same fail-closed posture the [tenant data scope](./multi-tenancy.md#isolation) takes on the correctness side.

## Where limits live

Governance limits are **operator and tenant configuration**, not client input:

- They are declared on the tenant's **control-plane record** and edited through the admin console and control-plane API.
- They are **never a token claim**. A caller's JWT identifies the tenant; the enforcing service then resolves that tenant's limits from configuration. Nothing a client sends — headers, claims, payloads — can raise its own ceiling.

## Tiers supply the ceilings

A tenant's governance ceilings come from its **[tier](./tenant-tiers.md)** — the operator-defined packaging entity that answers "what kind of customer is this?". The tier is where an operator packages *how much*: the default ceilings a class of tenants inherits. Resolution follows a three-level cascade:

**per-tenant override → tier setting → platform default**

Per-tenant overrides are audited exceptions, not the mechanism — the tier carries the packaged answer, and the platform default is the floor the fail-safe rule guarantees. The same cascade shape governs branding and AI model entitlement, so "which tier is this tenant on?" answers one consistent question across subsystems.

## Seeing it work

Shed volume is surfaced as an operational metric, so a tenant that has hit a ceiling — or a rule that has started to over-emit — is visible to an operator before it becomes a support ticket. Governance is meant to be observable pressure, not silent loss.

:::note Status
**Enforced today:** per-tenant ingest rate limiting (event-sources), outbound egress governance at both ends of the REACT hop (event-processing + outbound-connectors), and per-tenant AI-inference rate limiting with spend observability — all through the shared core governance resolver, all subject to the fail-safe platform-default rule. **Planned behind the same model:** per-tenant API/query governance, per-tenant stream bounds on the internal bus, and a relationship fan-out ceiling.
:::

## Related

- **[Multi-Tenancy](./multi-tenancy.md)** — the correctness half: fail-closed data isolation on the same shared instance.
- **[Outbound Connectors](./outbound-connectors.md#governance)** — how egress governance applies to webhooks and broker publishes.
- **[Tenant Tiers](./tenant-tiers.md)** — the packaging entity that supplies a tenant's default ceilings.
