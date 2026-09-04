---
title: AI-Assisted Authoring
---

# AI-Assisted Authoring

A detection rule can be authored three ways in DeviceChain — a typed **form**, a visual **automation canvas**, and, with the AI service enabled, a plain-English **"Describe" door**. You type *"raise a high alarm when a freezer's temperature stays above -15°C for more than ten minutes"* and the platform drafts a rule you can review, adjust, and publish.

All three doors lower to the **same structured rule schema** and pass through the **same compiler**. That is the whole design: the AI is one more front door onto a single, deterministic back end — never a second engine, and never part of the live event path.

:::note Status
**Available today:** an opt-in `ai-inference` service (in the `full` deployment profile); an operator-registered **AI provider** registry with write-only key handles; a natural-language **"Describe"** door on the device-profile rule authoring surface — the `draftDetectionRuleFromText` mutation, which calls the AI service and runs a bounded compile-and-repair loop (it lives on the API that owns the rule **compiler**, not the one that stores rules — drafting is a compile-time operation, and the draft it returns is saved through the ordinary rule-creation API afterwards); per-tenant opt-in consent; and per-tenant AI rate limiting with spend metrics. **Planned:** a durable per-tenant AI **spend budget** (a hard cost ceiling — rate limiting and spend observability ship today). This repository is the source of truth for what currently builds.
:::

## AI proposes, the compiler disposes

Every authoring surface produces a candidate rule; the **CEL compiler** then parses it, type-checks it, and cost-gates it before it can be saved — and a rule that is malformed, mistyped, or over a tenant's cost ceiling is **rejected at publish, before it ever runs**. The AI door is no exception: the model *proposes* a candidate, and the compiler *disposes* — accepts or rejects — exactly as it does for a hand-drawn canvas rule.

This is the **determinism boundary**, and it is a hard line:

- The AI (and the canvas) sit **only** on the authoring side. They help you write a rule.
- The **compiled rule** — deterministic CEL over the keyed-streaming engine — is what runs, and it is [replay-correct](./event-processing.md) by construction.
- **Neither the model nor the canvas ever sits in the replay-correct detection path.** A restart re-derives identical firings from the compiled rule; the model that helped draft it is nowhere in that loop.

When you use the Describe door, the service runs a **bounded compile-and-repair loop**: it drafts a candidate, compiles it, and — if the compiler rejects it — feeds the error back for a limited number of repair attempts. What you are handed is a candidate that already compiles. You still review and publish it yourself; nothing is armed on your behalf.

## AI providers are operator configuration

AI is **operator-registered, instance-scoped** configuration — not something a tenant brings. An operator registers one or more **AI providers** on the admin plane (`/admin/ai-providers`), each with a kind, an endpoint, a model, and an **API key**.

The API key is a **write-only secret handle** ([secret store](./architecture.md)): it is sealed on write, resolved server-internally at inference time, and **never returned** — the read side of a provider exposes only whether a key is set (`hasSecret`), never the value. The provider detail view is organized as **Basic / Connection / Test** tabs, and a **Test** action probes connectivity without exposing the key.

External-model use is **per-tenant opt-in and fail-closed**: a tenant must consent before any external inference runs on its behalf, and inference **fails closed** on any gap in the chain — no consent, no provider, a disabled provider, or no key all resolve to "no inference," never to a silent fallback.

## AI is a tiered entitlement

Which model a tenant actually runs is governed by its [**tenant tier**](./tenant-tiers.md), and the rules are deliberately strict:

- An operator grants providers/models to **tiers**, and (optionally) to individual tenants.
- The model in use for a given capability is a **`(tenant, function) → model` assignment** that falls back to the **tier's default**.
- The server **never infers** a default. A grant is not a default; there is no "make default" flag. If a tier packages no model, the tenant has **no model** — *no menu means no model*.
- An assignment that points **off** the current menu resolves to **NONE** — never a silent substitution.

Users do **not** pick a model per task. Model choice is operator configuration set once per function on the tenant's settings, not a parameter on any request. The one AI function in the GA vocabulary is **rule drafting**; the mechanism generalizes to future functions without changing the contract.

## Where it lives in the console

- **Describe door** — on the device profile's detection-rule authoring surface, alongside the form builder and the automation canvas, and offered when you **create** a new rule (an existing rule is edited in the form or on the canvas). Type a description, review the drafted rule, publish.
- **AI providers** — `/admin/ai-providers` (admin plane): register providers, set keys, test connectivity.
- **AI packaging** — the cross-tier grant matrix that maps which models each tier may use.
- **Per-tenant model** — set on the tenant detail page, per function, from the tier-derived menu.

## Limits and boundaries {#limits-and-boundaries}

### What the AI never touches

- It never runs in the live [detection/REACT](./event-processing.md) path — that path is deterministic CEL, replay-correct, model-free.
- It never sees another tenant's data, and it is not a privileged backdoor. (Distinct from the [MCP surface](./mcp.md), where an AI *agent* operates the platform under a user's own tenant-scoped token.)
- Tenant business data (device names, attribute values) and secrets are not the model's to expose; keys stay write-only in the secret store.
- **It writes nothing.** A drafted rule is returned for a person to review and save through the ordinary authoring path, under their own token. Nothing is persisted by the drafting call itself.

### Bring-your-own-key is not supported, and will not be

A tenant cannot supply its own provider key. Providers are **instance-level operator configuration**: an operator registers them, holds the keys, and decides which tiers and tenants may use which models. The one per-tenant lever is the consent flag for external inference, and that is not self-service either — a tenant can read it, but only an operator can set it.

This is a decision, not a gap. A customer that needs to run on its own key and its own account needs a dedicated instance, which is where every other request for per-tenant infrastructure also lands — and a per-tenant key inside a shared instance would be the one piece of that isolation offered without the rest of it.

### Bounds

- **One provider kind ships today: Anthropic.** The provider entity is built around a seam for others, and other kinds are refused at write until their implementation lands rather than accepted and left inert.
- **The repair loop is bounded.** A candidate the compiler rejects is fed back with the compiler's own error a fixed, small number of times, and then the request fails. It never loosens the compiler to make a draft fit.
- **Every tenant call is capped server-side** — prompt size, output length, timeout, and a per-tenant request rate. None of these is caller-supplied, and none of them is unlimited.
- **One function.** The GA vocabulary has exactly one AI function, rule drafting, and the calling service names it — a caller cannot choose its own function, because choosing a function would be choosing an entitlement.

### One deliberate exception to the consent gate

An operator testing a provider's connectivity from the admin console reaches the provider without a tenant's consent flag, and **without the per-tenant rate limit** — that path resolves the provider by token, so neither gate applies to it. The call is an operator's own prompt against an operator's own configuration, no tenant data crosses the boundary, and the key must still resolve. Every path that carries tenant input goes through both gates.

## See also

- [Event Processing & Alarms](./event-processing.md) — the compiler and the engine the AI drafts against.
- [Tenant Tiers & Packaging](./tenant-tiers.md) — how AI model entitlement is packaged.
- [AI Access (MCP)](./mcp.md) — the separate, read-only surface for AI agents operating the platform.
