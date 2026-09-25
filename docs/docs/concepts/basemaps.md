---
title: Basemaps
---

# Basemaps

Every map surface in DeviceChain draws its positions on a **basemap**: raster map tiles fetched from a provider you choose. That covers the geofence editor, the dashboard map widget, and a board embedded through the standalone viewer.

A new instance draws maps out of the box. The shipped default is the [OpenStreetMap](https://www.openstreetmap.org/) standard tile layer, which needs no account. Nothing is adopted silently: the default is named, visible in **Settings**, and replaceable in one edit. Every tier that can set a tile source must supply the credit line that provider's licence requires.

Before you go to production, decide whether the default is the right provider for you. See [Choosing a provider](#choosing-a-provider).

## The basemap belongs to the tenant {#the-basemap-belongs-to-the-tenant}

You configure a basemap **per tenant**, in the console under **Settings → Map**. You need the `basemap:write` authority.

Pick a provider from the list and DeviceChain fills in its tile template and the credit line its licence requires. Both fields stay editable. For a provider that is not on the list, such as an internal tile server, you type the two fields in directly; see [Choosing a provider](#choosing-a-provider).

The tenant owns the basemap because a tile URL usually carries an API key. A key that belongs to the tenant means the tenant's own map account is separately billed, separately rate-limited, separately restricted, and revocable without touching anyone else on the instance. One tenant exhausting its quota cannot blank another tenant's maps, and a tenant that already has a contract with a provider can bring it.

`basemap:write` is deliberately separate from `branding:write`, even though both shape how a tenant's console looks. Bundling them would make each grant imply the other: whoever restyles the logo could read the map key, and whoever configures maps could restyle the console.

## Where a value comes from {#where-a-value-comes-from}

A value comes from one of three tiers, most specific first:

| Tier | Set by | Where |
| --- | --- | --- |
| Per-surface override | Anyone editing that surface | A map widget's own options; the geofence editor's basemap fields, remembered in your browser |
| **Tenant** | A tenant admin (`basemap:write`) | Console → **Settings** → **Map** |
| Instance default | An operator (`settings:write`) | Admin console → **Settings** → `basemap.default` |

Each tier fills in what the one above it leaves blank. A single-tenant or appliance deployment can set the value once at the instance level and leave it.

The per-surface tiers are not going away. Use them to try a provider on one board, or in your own browser, before committing it for everyone.

### The tile source moves as one value {#the-tile-source-moves-as-one-value}

A tile URL and the attribution its licence requires are **one value, not two**. They are validated together, so neither can be saved without the other, and they inherit together.

Inheriting together matters most. If your tenant sets its own tile URL and leaves the attribution blank, it does **not** keep the instance default's credit line. Showing one provider's tiles under another provider's credit is a licence violation, so the cascade will not manufacture one. At the tenant and instance tiers, the save is refused before it gets that far.

The per-surface tiers are set in the browser and never reach that validation, so they apply the same rule at the point of use. A widget option or a geofence-editor field naming a tile URL with no credit line is **ignored entirely**, and the map falls back to the tenant's properly-credited basemap. The geofence editor tells you when this happens. A half-filled override is discarded rather than half-applied, because the alternative is drawing a provider's tiles with no credit at all.

The starting view has fewer constraints, but not none:

- `zoom` inherits on its own, so a tenant can change the zoom without restating a provider.
- `centerLat` and `centerLon` are a **pair**, because half a coordinate names no point. Setting one without the other is refused.
- Overriding only one of the pair at a lower level does not borrow the other from above. It clears the inherited centre, leaving that surface with no starting view at all.

### The starting view is a fallback, never an override {#the-starting-view-is-a-fallback}

The centre and zoom apply only when a map has **nothing of its own to fit to**. A geofence that already has a shape opens on that shape; a map widget with markers fits its markers. Editing a fence in Rome from a tenant centred on Atlanta opens on Rome.

## What a tile URL must look like {#what-a-tile-url-must-look-like}

Saving refuses any value that breaks these rules, because each one would otherwise fail silently later.

**These rules apply at the tenant and instance tiers only.** A per-surface override (a map widget's tile URL, or the geofence editor's personal field) is held in your browser and never reaches the server. Nothing checks it beyond the credit-line rule above. A URL that is `http://`, that carries a Leaflet-style `{s}`, or that points at a style JSON is accepted there and handed straight to the renderer. The renderer then draws blank tiles with no message and no fallback. If a personal override shows an empty map, walk the rules below as a checklist.

- **`https` only.** A console served over HTTPS blocks tiles fetched over HTTP as mixed content, so an `http://` source would be stored but never render. If you run an internal tile server on plain HTTP, put it behind TLS.
- **It must be a template.** The URL needs `{z}`, `{x}` and `{y}`, or `{bbox-epsg-3857}`, or `{quadkey}`. Without a placeholder, every tile on the map requests the same image. That is the shape of the two common paste errors: a single tile's URL, and a style JSON URL.
- **Only placeholders the renderer knows are allowed:** `{prefix}`, `{z}`, `{x}`, `{y}`, `{ratio}`, `{bbox-epsg-3857}` and `{quadkey}`. Anything else in braces is sent to the provider as literal text. This catches the most common copy of all, a URL written for Leaflet, which carries an `{s}` subdomain placeholder that DeviceChain's renderer does not substitute. Replace it with a single subdomain (`a.tile.example.com` rather than `{s}.tile.example.com`), which is what current practice recommends anyway.
- **Attribution is required, and its markup is limited** to plain text plus links written exactly as `<a href="https://…">text</a>`. Links are allowed because several providers' licences require the credit to link to their copyright page. Everything else is refused.

Only **raster** tiles are supported today. A vector style URL is not accepted, and tiles are requested under the standard 256-pixel `{z}/{x}/{y}` addressing. A retina endpoint serving 512-pixel images at those same coordinates works and renders sharper; that is what `{ratio}` is for. A tile server using a genuine 512-tile *scheme*, where the coordinates themselves mean something different, is not supported.

### The numeric and length bounds {#the-numeric-and-length-bounds}

These values are also refused on save at the tenant and instance tiers:

| Field | Bound |
| --- | --- |
| `tileUrl` | 2048 characters |
| `attribution` | 512 characters, and no control characters |
| `zoom` | 0 to 24 |
| `centerLat` | −90 to 90 |
| `centerLon` | −180 to 180 |

The server names the bound it refused, so you learn it at Save. The console form only checks that the camera fields *are numbers*, not that they are in range. A zoom of 30 passes the form and comes back as a server error rather than being caught as you type.

## Choosing a provider {#choosing-a-provider}

The default gets you a working map on day one. It is not automatically the right answer for a production deployment, and the deciding factor is usually **who is expected to serve your traffic**.

The **Provider** list carries providers whose tile template and required credit line have each been checked against that provider's own documentation. Choosing one fills in both fields. Where a provider needs an API key, the key gets its own field and is composed into the URL for you, so you can rotate the key later without re-pasting the template.

The list deliberately does not do two things:

- **It does not describe anyone's terms.** Each entry links to the provider's own terms and pricing page instead. Whether a tier is free, needs an account, or has a rate limit can change on someone else's website without us noticing, so the list points at the source rather than summarising it. Read it before you rely on a provider.
- **It is not exhaustive, and that is a deliberate bar rather than a backlog.** A provider is listed only where the provider itself publishes its required credit line. An entry with a *wrong* credit line is worse than a missing one: it would ship a licence violation prefilled and trusted, in the one place you are entitled to assume we got it right. If your provider is missing, choose **Custom…** and enter the two fields yourself.

Choosing **Custom…** never alters what is already in the fields. It means "I am typing this myself", which is exactly when overwriting would be most destructive.

### The OpenStreetMap default {#the-openstreetmap-default}

OpenStreetMap's tile servers are run by a non-profit and funded by donations. Their [tile usage policy](https://operations.osmfoundation.org/policies/tiles/) sets out what they ask of you, and DeviceChain is built to meet it: tiles are fetched only as you browse, never pre-fetched or archived, and the credit line is always shown. Two things stay your responsibility:

- **Do not put a restrictive `Referrer-Policy` in front of the console.** The policy asks browser clients for a valid `Referer`. Stripping it can get an instance blocked with no warning, and with no local symptom other than a map that stopped drawing.
- **Read the policy before scaling up.** It reserves the right to block access without prior notice where usage degrades the service. That is reasonable for donated infrastructure to say, and a poor thing to discover during a customer demo.

If your maps matter to your operation, point a tenant, or the instance default, at a provider you have a relationship with. The per-tenant tier exists for this case. Read [the section on API keys](#the-api-key-is-not-a-secret) next.

### With no tile source you get a schematic world, not a blank one {#no-tile-source}

If an operator sets the instance default to `{}` and a tenant sets nothing, there is no tile source at all.

:::tip Switching maps off is `{}`, not a reset
**Reset to default** does the opposite of switching maps off: it restores the shipped provider. To switch maps off, set an explicit `{}`.
:::

Map surfaces then fall back to a **bundled world basemap**: public-domain Natural Earth land and country outlines, compiled into the app itself. It requests nothing from any outside host; everything it needs is served from DeviceChain itself. That makes it the right answer for an air-gapped install, and for an operator who has deliberately switched providers off.

It is honestly schematic, with continents and borders and nothing at street zoom, so it reads as "configure a provider" rather than "this is broken". Everything else works exactly as it does on tiles. You can still draw a geofence, and the coordinates you place are still exact, because the projection is the same one a tiled map uses.

## The API key in the tile URL is not a secret {#the-api-key-is-not-a-secret}

:::warning It is visible to anyone using the tenant
If your provider's tile URL carries an API key, **that key reaches the browser**. It has to, because the browser is what fetches the tiles. Protect it with the provider's own controls, described below.
:::

The key is stored as ordinary configuration, not in the secret store, because a value the client must read cannot be kept from the client. The **API key** field exists to put the key in the right place in the template, not to protect it.

Protect the key the way map providers expect: with **HTTP-referrer restrictions** in the provider's own console, scoped to the hostname your console is served from, and, where offered, per-key quotas and API restrictions. That is the control that actually limits abuse of a key like this. Treat rotation as routine, and use a separate key per tenant so revoking one affects nobody else.

## Embedded dashboards {#embedded-dashboards}

The standalone dashboard viewer signs in as its own user and reads the same tenant basemap, so a board embedded there draws on the same tiles it does in the console.

If an embedded board shows a different basemap from the console, check that the viewer signed in **as a member of the same tenant**. The most telling case is the schematic bundled world where you expected tiles. The basemap follows the tenant, not the board.
