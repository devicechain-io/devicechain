---
title: Tenant settings
sidebar_position: 14
---

import GuideShot from '@site/src/components/GuideShot';

# Tenant settings

The **Settings** group in the sidebar holds three pages that shape how the console looks and opens
for everyone in your tenant: **Branding**, **Map** and **Language**. This chapter is for tenant
administrators.

Each page follows the same pattern. A field you leave empty inherits a default: the one your
instance operator has set, or the built-in one when the operator has set nothing. Each page needs
its own permission. Without it the page is not listed in the sidebar, and if you open it by its
address it shows only which permission to ask for.

## Branding

**Branding** white-labels the console for your tenant: the title, the colours and the logo.

<GuideShot id="settings-branding" alt="The Branding page. Fields for App title, Primary, Accent, Sidebar background and Sidebar foreground colours (all showing inherit), Logo max height (px) set to 28, and Logo, with an https URL box, Apply URL and Upload buttons. A Preview card on the right shows the DeviceChain title, a Primary button and sample content. A Save branding button sits at the top right." />

The fields:

- **App title** is shown in the browser tab and the console. Empty inherits the default, which is
  *DeviceChain* unless your operator changed it.
- **Primary** colours buttons, links and focus rings. **Accent** colours secondary highlights.
- **Sidebar background** and **Sidebar foreground** colour the sidebar and the text and icons on
  it. They do not change the page itself, which keeps its light or dark theme.
- **Logo max height (px)** sets how tall the logo appears in the sidebar, from 16 to 200. Empty
  inherits the default, which is 28 unless your operator changed it.

Type a colour as a six-digit hex code, such as `#1f9fb7`, or use the swatch to pick one. Clear a
field to go back to inheriting. The **Preview** card shows the title (or the logo, once one is set)
and a primary button as you change them. If a colour pairing is hard to read, the page warns you, but still lets you save.
Choose **Save branding** to apply the title, colours and logo height.

The **Logo** is handled separately and takes effect immediately, without **Save branding**:

- **Upload** a PNG, JPEG or WebP file of up to 1 MB, or
- paste an `https://` address of an image you host (SVG works here) and choose **Apply URL**.

**Remove logo** clears it. The sign-in page always shows the standard DeviceChain brand, because no
tenant is known until someone signs in. See [White-labeling and branding](/concepts/white-labeling)
for how the defaults combine.

## Map

**Map** sets the map tiles that this tenant's geofence editor and dashboard map widgets draw on, and
where a new geofence opens. The bar at the top of the page reads *Basemap*.

<GuideShot id="settings-basemap" alt="The Map page. Under Tile source, a Provider list set to Custom…, a Tile URL template field, an Attribution field, and a line reading Inheriting the instance default: https://tile.openstreetmap.org/{z}/{x}/{y}.png. A note warns that an API key in a tile URL is not a secret. Under Default view are empty Latitude, Longitude and Zoom fields, with a Save basemap button below." />

**Tile source.** Leave it empty to inherit the instance default; the page shows which one that is.
To use your own provider:

1. Pick it from **Provider**. The console fills in the **Tile URL template** and the
   **Attribution** that the provider's licence requires. Some providers also ask for an **API key**.
   For a provider that is not listed, choose **Custom…** and fill in both fields yourself.
2. Check the two fields. The tile URL must start with `https://` and contain the `{z}`, `{x}` and
   `{y}` placeholders (or `{bbox-epsg-3857}` or `{quadkey}`).
3. Choose **Save basemap**.

The tile URL and its attribution are saved together or not at all, because showing a provider's
tiles without its credit line breaks its licence.

:::warning A key in a tile URL is not secret
The browser fetches tiles with the key in the URL, so anyone using this tenant can read it. Protect
the key with your provider's HTTP-referrer restrictions instead.
:::

**Default view.** **Latitude**, **Longitude** and **Zoom** set where the geofence editor opens when
you start a new fence. Latitude and longitude go together: set both or neither. A fence that already
has a shape opens on that shape, and dashboard map widgets always fit what they are showing, so
neither uses this view.

See [Basemaps](/concepts/basemaps) for choosing a provider and the exact rules.

## Language

**Language** sets the language the console opens in for members of this tenant who have not chosen
one for themselves. The bar at the top of the page reads *Locale*.

<GuideShot id="settings-locale" alt="The Language page. Default language offers Inherit (selected), English and Español. A note explains that this is a default, not an override, and a Save language button sits below." />

Choose **Inherit** to follow the instance-wide default your operator has set, or pick a language,
then choose **Save language**.

This is a default, not an override. Anyone who has picked a language from the user menu keeps it.
The console chooses a language in this order:

1. the language the person picked;
2. this tenant's default;
3. the instance default;
4. the languages the person's browser asks for;
5. English.

See [Default language](/concepts/system-settings#locale-default) for the instance setting.

## Related

- [White-labeling and branding](/concepts/white-labeling)
- [Basemaps](/concepts/basemaps)
- [System settings](/concepts/system-settings), for the instance-wide defaults an operator sets
