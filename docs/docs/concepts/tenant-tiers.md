---
sidebar_position: 9
title: Tenant Tiers & Packaging
---

# Tenant Tiers & Packaging

A **tenant tier** is how an operator packages what a tenant gets. It is a first-class, operator-defined entity — *gold / silver / bronze*, or whatever an operator chooses to name and sell — that other parts of the platform **read** but never redefine. A tier answers the question **"how much?"**: the [governance ceilings](./governance.md) a tenant inherits and the [AI models](./ai-authoring.md) a tenant may use.

The guiding distinction: **a shed dial is tuned; a tier is sold.** Operational knobs (rate limits, contention behavior) are things you turn to keep a system healthy. A tier is a product decision — a named package a customer is on. Modeling it as its own entity keeps that product concept out of the low-level operational machinery, where it would otherwise get re-invented inconsistently.

:::note Status
**Available today:** the `TenantTier` entity in `user-management`; a config-key registry that defines what a tier packages; tier administration on the instance admin plane; effective-settings resolution through the tier; the AI model-entitlement model (assignments + tier defaults); an **AI packaging** grant matrix; tier presentation (display order, color, drag-reorder); and **preferential shedding under contention** — a tenant's tier carries a shed priority that governs who degrades last when the platform is under load, with the tier's stored priority available as a per-tenant operational override. **Planned:** an automatic contention signal that raises the shed level on its own; today an operator sets the contention floor.
:::

## What a tier packages

A tier is a named bundle of settings drawn from a **config-key registry** — the platform's list of the dials a tier is allowed to set. Two consumers read a tenant's tier today:

- **Governance ceilings.** A tenant's per-tenant quotas (ingest rate, egress rate, AI inference rate) resolve through its tier. See [Governance & Quotas](./governance.md). The fail-safe rule still holds end to end: a missing or zero limit resolves to the **platform default, never to unlimited**.
- **AI model entitlement.** Which [AI models](./ai-authoring.md) a tenant may use is packaged on the tier. The model a tenant runs for a function is a `(tenant, function) → model` assignment that falls back to the **tier's default**; if a tier packages no model, the tenant has no model. *No menu means no model.*

Because a tier is read by many subsystems but owned by one, the pattern is always the same: subsystems **read** the tier; they never store their own copy of "what this tenant is entitled to."

## Tiers are operator-owned, never client-settable

A tier — and the priority and limits it carries — is **operator configuration**. It is:

- **Never client-settable.** A tenant cannot raise its own ceilings or change its own tier.
- **Never a token claim.** Tier is not encoded in a JWT and is not an authorization input; it is resolved server-side from the control-plane tenant record.

There is one deliberate exemption: **identity tokens and service tokens are not bound to an authority tier.** Binding them would silently collapse every per-tenant governance ceiling for those privileged paths, so they are exempt by design rather than by omission.

## Presentation: a shelf, not a ladder

Tiers carry a **display order** and a **color** so an operator can present them coherently (colored pills, a drag-to-reorder list, a tabbed tier detail view). The display order is a **shelf, not a ladder** — it is how tiers are arranged for presentation, not an implied ranking that any subsystem computes against. Ordering is cosmetic; entitlement comes from what a tier actually packages.

## Where it lives in the console

- **Tiers** — `/admin/tiers` (admin plane): create, edit, color, and reorder tiers; open a tier for its packaged settings.
- **AI packaging** — the cross-tier matrix mapping which AI models each tier may use.
- **Per-tenant** — a tenant's tier is set on its admin detail page; its per-function AI model is set there too, from the tier-derived menu.

## A packaging concept in exactly one place

Tenant tiers are a familiar, expected capability for anyone packaging a multi-tenant IoT platform — table stakes, done cleanly. The point of modeling a tier as its own first-class entity is that the "what a tenant is entitled to" concept lives in **exactly one place** instead of being scattered across the services that consume it: governance reads it, AI entitlement reads it, and neither keeps its own copy.

## See also

- [Governance & Quotas](./governance.md) — the ceilings a tier supplies.
- [AI-Assisted Authoring](./ai-authoring.md) — the AI model entitlement a tier packages.
- [Multi-Tenancy](./multi-tenancy.md) — how tenants are modeled and isolated.
