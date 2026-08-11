# Where `catalog.json` came from

JSON carries no comments, and this is the file where a wrong value is most expensive, so the
evidence lives here.

## The rule this file exists to enforce

> **A catalog entry with a WRONG attribution is worse than no entry at all.** An absent provider
> costs someone five minutes of copy-paste. A present one with a stale or incorrect credit line
> ships a licence violation — prefilled, trusted, and replicated across every instance that picks
> it — in the one feature whose entire premise is getting attribution right.

So breadth is not the goal; **verified** breadth is. An entry ships only with:

1. an attribution quoted from **that provider's own documentation**, with the URL in
   `attributionSource`;
2. a template from the provider's own docs (`templateSource`), and where possible a tile actually
   fetched;
3. a `termsUrl` for the UI to link — we never paraphrase a provider's terms, because
   "free", "no key needed" and every tier description rot silently and we would not notice.

Verified 2026-08-10. Re-check before adding an entry, not on a schedule — a stale claim here is
the failure mode, and a date in a file does not expire on its own.

## What was measured, not just read

Every keyless template was fetched (same tile, `5/9/12`) and returned a real PNG:

| URL | Result |
| --- | --- |
| `tile.openstreetmap.org/5/9/12.png` | 200 `image/png`, 13913 B |
| `tile.opentopomap.org/5/9/12.png` | 200 `image/png`, 23355 B |
| `a.basemaps.cartocdn.com/light_all/5/9/12.png` | 200 `image/png`, 5113 B |
| `a.basemaps.cartocdn.com/dark_all/5/9/12.png` | 200 `image/png`, 3787 B |
| `a.basemaps.cartocdn.com/rastertiles/voyager/5/9/12.png` | 200 `image/png`, 10257 B |
| `tile.thunderforest.com/{cycle,transport,landscape,outdoors,atlas}/5/9/12.png` | 200 `image/png` (all five) |

🔴 **With a negative control, because a 200 from a server that answers everything means nothing.**
`tile.openstreetmap.org/5/9/99999.png` returns **400 with no image**, so the 200s above are a
statement about those URLs rather than about that host's willingness to reply.

## Why MapTiler, Stadia Maps and Esri are NOT here

All three were candidates. Their **attributions** are well documented — MapTiler's and Stadia's were
read and quoted successfully. What could not be established is their **style slugs**, and the
negative control is the whole reason we know that:

| Probe | Real style | Style that certainly does not exist |
| --- | --- | --- |
| Thunderforest | `200 image/png` | **`404`** ← the probe discriminates |
| MapTiler | `403` | **`403`** ← it does not |
| Stadia Maps | `401` | **`401`** ← it does not |

MapTiler and Stadia reject an unkeyed request **before** resolving the style, so an unauthenticated
probe returns the identical status for `streets-v2` and for `definitely-not-a-real-style-xyz`.
Without that control the reading would have been "I probed `streets-v2` and the path exists" — and
a guessed slug would have shipped wearing the word *verified*.

Esri's basemap-service documentation returned 404 at the URL tried; nothing about it was
established either way.

**What would unblock them, corrected after review:**

- **Stadia Maps is addable today, and the sentence that used to sit here was wrong.** It claimed a
  provider-published style list was "simply absent"; in fact
  `docs.stadiamaps.com/map-styles/alidade-smooth/` publishes the exact raster template *including*
  its style ID, and the attribution page publishes the credit line. One wrinkle to handle when
  adding it: Stadia's published template carries **`{r}`** for retina, which MapLibre does not
  substitute (its token is `{ratio}`) and which the validator therefore rejects. That is a template
  adjustment, not a wording change, so it stays inside the rules above.
- **MapTiler still cannot be added.** Its style list genuinely lives behind the account console; the
  docs give `streets-v4` only as an example.
- **Esri** was recorded as "documentation 404'd", which is honest but thin. Its tile endpoints do
  answer; the substantive obstacle is that Esri's attribution is *dynamic* — it varies by layer and
  view — which does not fit a single static credit line.

An **absence claim is the kind that expires silently**, which is exactly what happened to the Stadia
one within a day of being written. Re-check before repeating it.

## Why CyclOSM and Humanitarian OSM (HOT) are NOT here

Different reason, and a more interesting one. Both serve tiles happily and both are legitimate
choices — but **neither publishes a required credit line**. CyclOSM's own website configures its
main layer with nothing but the OpenStreetMap data credit; HOT's style attribution could not be
found in any HOT- or OSM-France-published source. The widely-copied strings for both come from
third-party layer directories, not from the provider.

Composing a credit line ourselves is exactly what the rule at the top forbids: it would be **our**
guess at **their** terms, prefilled and trusted. So they wait for a provider-published string.

## Adjustments made to provider-published text, and why

Two mechanical changes, applied so a provider's own wording passes our validator unchanged in
meaning:

- **`&copy;` → `©`.** CARTO and Stadia publish the HTML entity. The literal character renders
  identically, matches the shipped instance default, and does not depend on the consumer decoding
  entities.
- **`http://` → `https://` in hrefs.** CARTO's published snippet links to
  `http://www.openstreetmap.org/copyright`. The server's attribution validator permits only
  `https://` hrefs, and the same page is served over TLS.

One shape worth knowing about for future entries: **Stadia publishes its anchors with
`target="_blank"`**, and the validator's allow-list permits an anchor with an `href` and nothing
else. A provider's markup therefore sometimes has to be reduced to its link and its text. That is a
formatting change, never a wording one — if an entry ever needs the *words* altered to pass
validation, that is a signal to drop the entry, not to edit the credit.

## The keyed template placeholder

Keyed providers carry `{apiKey}` in `tileUrl`. It is **our** token, not MapLibre's, so an
unsubstituted `{apiKey}` would be sent to the provider literally.

🔴 An earlier draft of this paragraph enumerated MapLibre's tokens from memory and **got it wrong**,
omitting `{prefix}` — which put a false list into the validator, its error message, and the
published documentation in two languages. The set is now read out of MapLibre's own source by
`placeholders.test.ts`, which compares it against the Go allow-list directly. Do not restate the
set here; that restatement is what broke.

The picker composes `{apiKey}` away before the value is ever stored, and refuses to save while it is
still present — see `catalog.ts`.

## One subdomain, deliberately

Several providers publish Leaflet-style templates with an `{s}` (or `{a|b|c}`) subdomain
placeholder — `{s}.tile.opentopomap.org`, `{s}.basemaps.cartocdn.com`. **MapLibre does not
substitute `{s}`**; it would request that host literally and render nothing. Each entry therefore
pins a single subdomain, which is also current practice — subdomain sharding existed to work around
HTTP/1.1 connection limits that HTTP/2 removed.

This is not only a catalog concern: it is the reason `basemap.Validate` now rejects unknown
placeholders outright, so a hand-pasted Leaflet URL fails at save with a message that names the
problem instead of storing a value that cannot draw.
