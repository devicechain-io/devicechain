---
title: Tenant Tiers & Packaging
---

# Tenant Tiers & Packaging

A **tenant tier** is how you, as an operator, package what a tenant gets. You define tiers yourself and name them to match what you sell: *gold / silver / bronze*, or anything else. A tier answers the question "how much?": the [governance ceilings](./governance.md) a tenant inherits and the [AI models](./ai-authoring.md) a tenant may use. The tier is a first-class entity, and other parts of the platform **read** it but never redefine it.

A tier is a product decision, not an operational knob. Rate limits and contention behavior are dials you turn to keep a system healthy. A tier is a named package a customer is on. In short: a shed dial is tuned; a tier is sold. Modeling the tier as its own entity keeps that product concept out of the low-level operational machinery, where it would otherwise get re-invented inconsistently.

:::note Status
**Available today:** the `TenantTier` entity in `user-management`, a config-key registry, tier administration on the instance admin plane, effective-settings resolution through the tier, AI model entitlement (assignments and tier defaults), an **AI packaging** grant matrix, tier presentation, and [preferential shedding under contention](#preferential-shedding). **Planned:** an automatic contention signal that raises the shed level on its own.
:::

## What a tier packages

A tier is a named bundle of settings drawn from a **config-key registry**: the platform's list of the dials a tier is allowed to set. Two consumers read a tenant's tier today.

- **Governance ceilings.** A tenant's per-tenant quotas resolve through its tier: ingest rate, egress rate, AI inference rate, the [undelivered-command ceiling](./commands.md#held-command-ceiling), and the three [geofence limits](./geofencing.md#what-a-boundary-may-be). See [Governance & Quotas](./governance.md). The fail-safe rule still holds end to end: a missing or zero limit resolves to the **platform default, never to unlimited**. Ceilings are enforced per replica; see [Ceilings are per replica](./governance.md#per-replica).
- **AI model entitlement.** The tier packages which [AI models](./ai-authoring.md) a tenant may use; an operator can also grant an individual tenant a model on top of its tier. The model a tenant runs for a function is a `(tenant, function) → model` assignment that falls back to the **tier's default**, if the tier marks one. If neither the tier nor a per-tenant grant puts a model on the tenant's menu, the tenant has no model: no menu means no model.

Many subsystems read a tier, but only one owns it. So the pattern is always the same: subsystems **read** the tier, and none stores its own copy of "what this tenant is entitled to."

## Preferential shedding under contention {#preferential-shedding}

A tenant's tier carries a shed priority. When the platform is under load, that priority governs which tenants degrade last. An operator can also store a shed priority on an individual tenant as a per-tenant operational override; when set, it takes precedence over the tier's.

Today an operator sets the contention floor. An automatic contention signal that raises the shed level on its own is planned.

## Tiers are operator-owned, never client-settable

A tier, and the priority and limits it carries, is **operator configuration**. It is:

- **Never client-settable.** A tenant cannot raise its own ceilings or change its own tier.
- **Never a token claim.** Tier is not encoded in a JWT and is not an authorization input. The platform resolves it server-side from the control-plane tenant record.

One exemption is deliberate: **identity tokens and service tokens are not bound to an authority tier.** An authority tier is a different idea from a tenant tier: it says whether a permission belongs to the instance-wide operator plane or to a single tenant. Binding them would silently collapse every per-tenant governance ceiling for those privileged paths. They are exempt by design, not by omission.

## Tier presentation {#presentation-a-shelf-not-a-ladder}

Tiers carry a **display order** and a **color**, so you can present them coherently: colored pills, a drag-to-reorder list, and a tabbed tier detail view. The display order is a shelf, not a ladder. It arranges tiers for presentation and is not an implied ranking that any subsystem computes against. Ordering is cosmetic; entitlement comes from what a tier actually packages.

## Where it lives in the console

- **Tiers**: `/admin/tiers` (admin plane). Create, edit, color, and reorder tiers; open a tier for its packaged settings.
- **AI packaging**: the cross-tier matrix mapping which AI models each tier may use.
- **Per-tenant**: set a tenant's tier on its admin detail page. Its per-function AI model is set there too, from the tenant's menu: its tier's models plus any per-tenant grants.

The two AI surfaces above need the optional [`ai-inference` service](./ai-authoring.md), which ships in the `full` deployment profile. Without it, the tier itself works normally and governance ceilings are unaffected. The AI packaging matrix and the per-tenant model menu report that this instance does not run the area.

## Why a tier is its own entity {#a-packaging-concept-in-exactly-one-place}

Tenant tiers are a familiar, expected capability for anyone packaging a multi-tenant IoT platform: table stakes, done cleanly. Modeling a tier as its own first-class entity means "what a tenant is entitled to" lives in **exactly one place**, instead of being scattered across the services that consume it. Governance reads it, AI entitlement reads it, and neither keeps its own copy.

## See also

- [Governance & Quotas](./governance.md): the ceilings a tier supplies.
- [AI-Assisted Authoring](./ai-authoring.md): the AI model entitlement a tier packages.
- [Multi-Tenancy](./multi-tenancy.md): how tenants are modeled and isolated.
