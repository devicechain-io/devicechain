---
title: Basemaps
---

# Basemaps

Every map surface in DeviceChain — the geofence editor, the dashboard map widget, and a board embedded through the standalone viewer — draws its positions on a **basemap**: raster map tiles fetched from a provider you choose.

A new instance draws maps out of the box: the shipped default is the [OpenStreetMap](https://www.openstreetmap.org/) standard tile layer, which needs no account. Nothing is adopted silently — the default is named, visible in **Settings**, and replaceable in one edit — and every tier that can set a tile source must supply the credit line that provider's licence requires.

Whether it is the right provider for you is a separate question, and one worth asking before you go to production. See [Choosing a provider](#choosing-a-provider).

## The basemap belongs to the tenant {#the-basemap-belongs-to-the-tenant}

A basemap is configured **per tenant**, in the console under **Settings → Map**, by someone holding the `basemap:write` authority.

Pick a provider from the list and DeviceChain fills in its tile template and the credit line its licence requires. Both stay editable underneath, so a provider that is not on the list — an internal tile server, say — is still a matter of typing the two fields in directly; see [Choosing a provider](#choosing-a-provider).

That placement is the point. A tile URL usually carries an API key, and a key that belongs to the tenant means the tenant's own map account is separately billed, separately rate-limited, separately restricted, and revocable without touching anyone else on the instance. One tenant exhausting its quota cannot blank another tenant's maps, and a tenant that already has a contract with a provider can bring it.

`basemap:write` is deliberately **separate from `branding:write`**, even though both shape how a tenant's console looks. Bundling them would make each grant imply the other in both directions: whoever restyles the logo could read the map key, and whoever configures maps could restyle the console.

## Where a value comes from {#where-a-value-comes-from}

Three tiers, most specific first:

| Tier | Set by | Where |
| --- | --- | --- |
| Per-surface override | Anyone editing that surface | A map widget's own options; the geofence editor's basemap fields, remembered in your browser |
| **Tenant** | A tenant admin (`basemap:write`) | Console → **Settings** → **Map** |
| Instance default | An operator (`settings:write`) | Admin console → **Settings** → `basemap.default` |

Each tier fills in what the one above it leaves blank, so a single-tenant or appliance deployment can set the value once at the instance level and never think about it again.

The per-surface tiers are not going away: they are how you try a provider on one board, or in your own browser, before committing it for everyone.

### The tile source moves as one value {#the-tile-source-moves-as-one-value}

A tile URL and the attribution its licence requires are **one value, not two**. They are validated together — neither can be saved without the other — and they inherit together.

That second part is the one worth knowing. If your tenant sets its own tile URL and leaves the attribution blank, it does **not** quietly keep the instance default's credit line: showing one provider's tiles under another provider's credit is a licence violation, so the cascade will not manufacture one. At the tenant and instance tiers the save is refused before it gets that far.

The **per-surface** tiers are set in the browser and never reach that validation, so they apply the same rule at the point of use: a widget option or a geofence-editor field naming a tile URL with no credit line is **ignored entirely**, and the map falls back to the tenant's properly-credited basemap. The geofence editor says so when it happens. A half-filled override is discarded rather than half-applied, because the alternative is drawing a provider's tiles with no credit at all.

The starting view is looser, but not entirely free of constraint. `zoom` inherits on its own, so a tenant can move the zoom without restating a provider to keep it. The centre does not: `centerLat` and `centerLon` are a **pair**, because half a coordinate names no point. Setting one without the other is refused, and overriding only one at a lower level does not borrow the other from above — it clears the inherited centre, leaving that surface with no starting view at all.

### The starting view is a fallback, never an override {#the-starting-view-is-a-fallback}

The centre and zoom apply only when a map has **nothing of its own to fit to**. A geofence that already has a shape opens on that shape; a map widget with markers fits its markers. Editing a fence in Rome from a tenant centred on Atlanta opens on Rome.

## What a tile URL must look like {#what-a-tile-url-must-look-like}

Saving is fail-closed, and each rule refuses a value that would otherwise fail silently later.

**These rules apply at the tenant and instance tiers only.** A per-surface override — a map
widget's tile URL, or the geofence editor's personal field — is held in your browser and never
reaches the server, so nothing checks it beyond the credit-line rule above. A URL that is `http://`,
that carries a Leaflet-style `{s}`, or that points at a style JSON is accepted there and handed
straight to the renderer, which draws blank tiles with no message and no fallback. If a personal
override shows an empty map, the rules below are the checklist to walk.

- **`https` only.** A console served over HTTPS blocks tiles fetched over HTTP as mixed content, so an `http://` source is stored-but-unrenderable. If you run an internal tile server on plain HTTP, put it behind TLS.
- **It must be a template.** The URL needs `{z}`, `{x}` and `{y}` — or `{bbox-epsg-3857}`, or `{quadkey}`. Without a placeholder, every tile on the map requests the same image, which is the shape of the two common paste errors: a single tile's URL, and a style JSON URL.
- **Only placeholders the renderer knows are allowed** — `{prefix}`, `{z}`, `{x}`, `{y}`, `{ratio}`, `{bbox-epsg-3857}` and `{quadkey}`. Anything else in braces is sent to the provider as literal text. This catches the most common copy of all: a URL written for Leaflet, which carries an `{s}` subdomain placeholder DeviceChain's renderer does not substitute. Replace it with a single subdomain — `a.tile.example.com` rather than `{s}.tile.example.com` — which is what current practice recommends anyway.
- **Attribution is required, and its markup is limited** to plain text plus links written exactly as `<a href="https://…">text</a>`. Links are allowed because several providers' licences require the credit to link to their copyright page; everything else is refused.

Only **raster** tiles are supported today. A vector style URL is not accepted, and tiles are
requested under the standard 256-pixel `{z}/{x}/{y}` addressing. A retina endpoint serving
512-pixel images at those same coordinates works and simply renders sharper — that is what
`{ratio}` is for. A tile server using a genuine 512-tile *scheme*, where the coordinates
themselves mean something different, is not supported.

### The numeric and length bounds {#the-numeric-and-length-bounds}

Also refused, on save at the tenant and instance tiers:

| Field | Bound |
| --- | --- |
| `tileUrl` | 2048 characters |
| `attribution` | 512 characters, and no control characters |
| `zoom` | 0 to 24 |
| `centerLat` | −90 to 90 |
| `centerLon` | −180 to 180 |

The server names the bound it refused, so you learn it at Save — but the console only checks that
the camera fields *are numbers*, not that they are in range, so a zoom of 30 passes the form and
comes back as a server error rather than being caught as you type.

## Choosing a provider {#choosing-a-provider}

The default gets you a working map on day one. It is not automatically the right answer for a production deployment, and the deciding factor is usually **who is expected to serve your traffic**.

The **Provider** list carries a set of providers whose tile template and required credit line have each been checked against that provider's own documentation. Choosing one fills both fields in; where a provider needs an API key, it gets its own field and is composed into the URL for you, so the key can be rotated later without re-pasting the template.

Two things the list deliberately does not do:

- **It does not describe anyone's terms.** Each entry links to the provider's own terms and pricing page instead. Whether a tier is free, or needs an account, or has a rate limit, is a claim that can change on someone else's website without us noticing — so the list points at the source rather than summarising it. Read it before you rely on a provider.
- **It is not exhaustive, and that is a deliberate bar rather than a backlog.** A provider is listed only where its required credit line is published by the provider itself. An entry with a *wrong* credit line is worse than a missing one: it would ship a licence violation prefilled and trusted, in the one place a user is entitled to assume we got it right. If your provider is missing, choose **Custom…** and enter the two fields yourself.

Choosing **Custom…** never alters what is already in the fields — it means "I am typing this myself", which is exactly when overwriting would be most destructive.

OpenStreetMap's tile servers are run by a non-profit and funded by donations. Their [tile usage policy](https://operations.osmfoundation.org/policies/tiles/) sets out what they ask of you, and DeviceChain is built to meet it: tiles are fetched only as you browse, never pre-fetched or archived, and the credit line is always shown. Two things stay your responsibility:

- **Do not put a restrictive `Referrer-Policy` in front of the console.** The policy asks browser clients for a valid `Referer`, and stripping it can get an instance blocked with no warning and no local symptom other than a map that stopped drawing.
- **Read the policy before scaling up.** It reserves the right to block access without prior notice where usage degrades the service — a reasonable thing for donated infrastructure to say, and a poor thing to discover during a customer demo.

If your maps matter to your operation, point a tenant — or the instance default — at a provider you have a relationship with. That is the case the per-tenant tier exists for, and the section below on API keys is the one to read next.

:::tip With no tile source you get a schematic world, not a blank one
If an operator sets the instance default to `{}` and a tenant sets nothing, there is no tile source at all. Note that **Reset to default** does the opposite — it restores the shipped provider — so switching maps off is an explicit `{}`, not a reset.

Map surfaces then fall back to a **bundled world basemap**: public-domain Natural Earth land and country outlines, compiled into the app itself. It requests nothing from any outside host — everything it needs is served from DeviceChain itself — which is what makes it the right answer for an air-gapped install as well as for an operator who has deliberately switched providers off.

It is honestly schematic — continents and borders, nothing at street zoom — so it reads as "configure a provider", not as "this is broken". Everything else keeps working exactly as it does on tiles: drawing a geofence still works, and the coordinates you place are still exact, because the projection is the same one a tiled map uses.
:::

## The API key in the tile URL is not a secret {#the-api-key-is-not-a-secret}

:::warning It is visible to anyone using the tenant
If your provider's tile URL carries an API key, **that key reaches the browser** — it has to, because the browser is what fetches the tiles. It is stored as ordinary configuration, not in the secret store, because a value the client must read cannot be kept from the client. Its own **API key** field exists to put the key in the right place in the template, not to protect it.

Protect it the way map providers expect: with **HTTP-referrer restrictions** (and, where offered, per-key quotas and API restrictions) in the provider's own console, scoped to the hostname your console is served from. That is the control that actually limits abuse of a key like this. Treat rotation as routine, and use a separate key per tenant so revoking one affects nobody else.
:::

## Embedded dashboards {#embedded-dashboards}

The standalone dashboard viewer signs in as its own user and reads the same tenant basemap, so a board embedded there draws on the same tiles it does in the console. If an embedded board shows a different basemap from the console — most tellingly the schematic bundled world where you expected tiles — check that the viewer signed in **as a member of the same tenant**. The basemap follows the tenant, not the board.
