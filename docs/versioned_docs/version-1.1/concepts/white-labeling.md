---
title: White-Labeling & Branding
---

# White-Labeling & Branding

A tenant can present the console under its own brand. A logo, a color palette and a product title replace the DeviceChain defaults throughout the tenant's console session. White-labeling is part of the open-source core, with no separate edition, so you can run one instance and let each customer tenant see *their* brand.

:::note Status
Available: the branding cascade (tenant → operator default → built-in floor), the console **Branding** editor, per-field inheritance, and logo storage via the object store or an inline/external reference.
Planned: a per-tenant login-screen skin, favicon, and custom-domain → tenant branding resolution. Until then the login page shows the built-in DeviceChain brand, not the operator default. See [Branding cascade](#the-cascade).
:::

White-labeling here means branding: look and feel. It is not a per-tenant fork of the application. Menus, copy and translations are the same for every tenant.

## Branding cascade {#the-cascade}

Branding is resolved **field by field** through a fallback chain, most specific first:

1. **Tenant override**: the tenant's own stored branding fields.
2. **Operator default**: an instance-wide default the operator sets as a system setting. It applies to every tenant that hasn't overridden a field.
3. **Built-in floor**: the stock DeviceChain look, compiled into the platform so the cascade always resolves without any configuration.

A tenant that sets nothing inherits the operator default. An operator that sets nothing gets the built-in floor. Clearing a tenant field re-inherits it, and the editor shows, per field, whether the value is set or inherited.

The server resolves the cascade, so every client (the console and embedders) sees the same effective branding.

The login page cannot use the cascade. No tenant is known before sign-in, and branding is applied only once a tenant is selected, so the login page shows the built-in DeviceChain brand.

## Customizable fields {#what-is-customizable}

| Surface | Fields |
|---|---|
| **Title** | the product name shown in the browser tab (the document title) |
| **Logo** | an image (with a max-height knob) swapped into the console header |
| **Palette** | four colors — primary, background, foreground, accent — applied as CSS custom properties at the app root |

The console themes entirely through design tokens, so the palette is a single write point and needs no custom CSS:

- **Primary** and **accent** restyle the application's design tokens (buttons, focus rings, accents).
- **Background** and **foreground** recolor the branded sidebar chrome only. The page base keeps its light/dark theme.

Arbitrary CSS injection is deliberately not offered. It would be an XSS and maintenance risk for little gain over a proper palette.

## Logo storage

A logo is an opaque reference, resolved one of three ways:

- **Uploaded**: stored in the [object store](./object-storage.md) and streamed back through an authorizing per-tenant proxy path, never a public URL.
- **Inline**: a bounded `data:` URI (≤ 256 KB) kept directly on the branding record, for installs with no extra infrastructure.
- **External URL**: an `https://` asset the tenant hosts itself.

The server validates uploads and inline images before storing them: raster image types only, with size ceilings enforced.

## Where branding lives

Branding is a set of typed, nullable columns on the tenant control-plane record. It is not a JSON blob, and it is **never in the JWT**: tokens carry authentication only.

The console reads the resolved branding through the self-scoped `tenant` query, its regular boot query. It caches the result per tenant, stale-while-revalidate: the cached value paints first, then a fresh fetch replaces it on every load. A rebrand therefore shows up promptly.

The resolved branding also carries an `updatedAt`. It changes when *either* the tenant override or the operator default changes, so clients that keep their own cache can key it on this value.

## Edit branding {#editing}

You edit branding on the console's **Branding** page (tenant plane), which requires the `branding:write` authority.

The theme fields (title, palette, logo height) are saved together as the raw override. The logo is managed separately, with actions that take effect immediately, so replacing the theme never wipes an uploaded logo.

The matching GraphQL mutations:

- **`setTenantBranding`** writes the caller's own tenant's theme override. A null field clears that field, so it re-inherits.
- **`setTenantLogo`** sets or clears the logo reference. Uploads go through a dedicated endpoint that writes to the object store.

Both act only on the tenant in the caller's token. Both validate their input and reject anything invalid before storing it.

See also [Multi-Tenancy](./multi-tenancy.md) for the tenant model this record hangs off, and [Object Storage](./object-storage.md) for where uploaded assets live.
