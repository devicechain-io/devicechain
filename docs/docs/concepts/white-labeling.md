---
sidebar_position: 10
title: White-Labeling & Branding
---

# White-Labeling & Branding

DeviceChain lets a tenant present the console under its own brand: a **logo**, a **color palette**, and a **product title** replace the DeviceChain defaults throughout the tenant's console session. White-labeling is part of the open-source core — there is no separate edition for it — so an operator can run one instance and let each customer tenant see *their* brand (ADR-038).

:::note Status
Available: the branding cascade (tenant → operator default → built-in floor), the console **Branding** editor, per-field inheritance, and logo storage via the object store or an inline/external reference. Planned (Phase 3): a per-tenant **login-screen skin**, **favicon**, and **custom-domain → tenant branding** resolution — until then the login page shows the operator's brand, since no tenant is known before sign-in.
:::

White-labeling here means **branding** — look and feel. It is not a per-tenant fork of the application: menus, copy, and translations are the same for every tenant.

## The cascade

Branding is resolved **field by field** through a fallback chain, most specific first:

1. **Tenant override** — the tenant's own stored branding fields.
2. **Operator default** — an instance-wide default the operator sets (a system setting), applied to every tenant that hasn't overridden a field.
3. **Built-in floor** — the stock DeviceChain look, compiled into the platform so the cascade always resolves without any configuration.

A tenant that sets nothing inherits the operator default; an operator that sets nothing gets the built-in floor. Clearing a tenant field re-inherits it — the editor shows, per field, whether the value is set or inherited. The cascade is resolved **server-side**, so every client (console, embedders) sees the same effective branding.

## What is customizable

| Surface | Fields |
|---|---|
| **Title** | the product name shown in the browser tab and console header |
| **Logo** | an image (with a max-height knob) swapped into the console header |
| **Palette** | four colors — primary, background, foreground, accent — applied as CSS custom properties at the app root |

Because the console themes entirely through design tokens, the palette is one write point: the four colors restyle the whole application without custom CSS. (Arbitrary CSS injection is deliberately not offered — it is an XSS and maintenance surface for marginal gain over a proper palette.)

## Logo storage

A logo is an opaque **reference**, resolved three ways:

- **Uploaded** — stored in the [object store](./object-storage.md) (ADR-058) and streamed back through an authorizing per-tenant proxy path, never a public URL.
- **Inline** — a bounded `data:` URI (≤ 256 KB) kept directly on the branding record, for zero-infrastructure installs.
- **External URL** — an `https://` asset the tenant hosts itself.

Uploads and inline images are validated server-side (raster image types only, size ceilings enforced).

## Where branding lives

Branding is a set of typed, nullable columns on the **tenant control-plane record** — not a JSON blob, and **never in the JWT**. Tokens stay auth-only; the console reads the resolved branding through the self-scoped `tenant` query (its regular boot query) and caches it stale-while-revalidate, keyed on an `updatedAt` that bumps when *either* the tenant override or the operator default changes — so a rebrand propagates promptly.

## Editing

Branding is edited in the console's **Branding** page (tenant plane), gated on the `branding:write` authority. The theme fields (title, palette, logo height) commit together as the raw override; the logo is managed separately with immediate actions, so replacing the theme never wipes an uploaded logo.

The corresponding GraphQL surface:

- **`setTenantBranding`** — writes the caller's own tenant's theme override; a null field clears that field (re-inherits).
- **`setTenantLogo`** — sets or clears the logo reference; uploads go through a dedicated endpoint that writes to the object store.

Both are self-scoped to the tenant in the caller's token and validated fail-closed before anything is stored.

See also [Multi-Tenancy](./multi-tenancy.md) for the tenant model this record hangs off, and [Object Storage](./object-storage.md) for where uploaded assets live.
