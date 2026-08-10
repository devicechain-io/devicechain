---
sidebar_position: 17
title: Basemaps
---

# Basemaps

Every map surface in DeviceChain — the geofence editor, the dashboard map widget, and a board embedded through the standalone viewer — draws its positions on a **basemap**: raster map tiles fetched from a provider you choose.

DeviceChain ships **no default tile provider**, and that is a decision rather than an omission. Map tiles carry licence terms, usage policies and often a bill, and the platform cannot accept any of those on your behalf. An instance nobody has configured shows every position it was asked to, on a plain panel, and says why there is no map behind them.

## The basemap belongs to the tenant {#the-basemap-belongs-to-the-tenant}

A basemap is configured **per tenant**, in the console under **Basemap**, by someone holding the `basemap:write` authority.

That placement is the point. A tile URL usually carries an API key, and a key that belongs to the tenant means the tenant's own map account is separately billed, separately rate-limited, separately restricted, and revocable without touching anyone else on the instance. One tenant exhausting its quota cannot blank another tenant's maps, and a tenant that already has a contract with a provider can bring it.

`basemap:write` is deliberately **separate from `branding:write`**, even though both shape how a tenant's console looks. Bundling them would make each grant imply the other in both directions: whoever restyles the logo could read the map key, and whoever configures maps could restyle the console.

## Where a value comes from {#where-a-value-comes-from}

Three tiers, most specific first:

| Tier | Set by | Where |
| --- | --- | --- |
| Per-surface override | Anyone editing that surface | A map widget's own options; the geofence editor's basemap fields, remembered in your browser |
| **Tenant** | A tenant admin (`basemap:write`) | Console → **Basemap** |
| Instance default | An operator (`settings:write`) | Admin console → **Settings** → `basemap.default` |

Each tier fills in what the one above it leaves blank, so a single-tenant or appliance deployment can set the value once at the instance level and never think about it again.

The per-surface tiers are not going away: they are how you try a provider on one board, or in your own browser, before committing it for everyone.

### The tile source moves as one value {#the-tile-source-moves-as-one-value}

A tile URL and the attribution its licence requires are **one value, not two**. They are validated together — neither can be saved without the other — and they inherit together.

That second part is the one worth knowing. If your tenant sets its own tile URL and leaves the attribution blank, it does **not** quietly keep the instance default's credit line: it resolves to no attribution, and the save is refused before it gets that far. Showing one provider's tiles under another provider's credit is a licence violation, so the cascade will not manufacture one.

The starting view (`centerLat` / `centerLon` / `zoom`) carries no such constraint and inherits field by field, so a tenant can move the zoom without restating a provider to keep it.

### The starting view is a fallback, never an override {#the-starting-view-is-a-fallback}

The centre and zoom apply only when a map has **nothing of its own to fit to**. A geofence that already has a shape opens on that shape; a map widget with markers fits its markers. Editing a fence in Rome from a tenant centred on Atlanta opens on Rome.

## What a tile URL must look like {#what-a-tile-url-must-look-like}

Saving is fail-closed, and each rule refuses a value that would otherwise fail silently later:

- **`https` only.** A console served over HTTPS blocks tiles fetched over HTTP as mixed content, so an `http://` source is stored-but-unrenderable. If you run an internal tile server on plain HTTP, put it behind TLS.
- **It must be a template.** The URL needs `{z}`, `{x}` and `{y}` — or `{bbox-epsg-3857}`, or `{quadkey}`. Without a placeholder, every tile on the map requests the same image, which is the shape of the two common paste errors: a single tile's URL, and a style JSON URL.
- **Attribution is required, and its markup is limited** to plain text plus links written exactly as `<a href="https://…">text</a>`. Links are allowed because several providers' licences require the credit to link to their copyright page; everything else is refused.

Only **raster** tiles are supported today. A vector style URL is not accepted.

## The API key in the tile URL is not a secret {#the-api-key-is-not-a-secret}

:::warning It is visible to anyone using the tenant
If your provider's tile URL carries an API key, **that key reaches the browser** — it has to, because the browser is what fetches the tiles. It is stored as ordinary configuration, not in the secret store, because a value the client must read cannot be kept from the client.

Protect it the way map providers expect: with **HTTP-referrer restrictions** (and, where offered, per-key quotas and API restrictions) in the provider's own console, scoped to the hostname your console is served from. That is the control that actually limits abuse of a key like this. Treat rotation as routine, and use a separate key per tenant so revoking one affects nobody else.
:::

## Embedded dashboards {#embedded-dashboards}

The standalone dashboard viewer signs in as its own user and reads the same tenant basemap, so a board embedded there draws on the same tiles it does in the console. If a map is blank when embedded but fine in the console, check that the viewer signed in **as a member of the same tenant** — the basemap follows the tenant, not the board.
