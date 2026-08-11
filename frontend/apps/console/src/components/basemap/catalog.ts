// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provider catalog's pure half: the data, and the two functions that move a value
// between "a provider and a key" and "a stored tile source".
//
// Split from the component for the usual reason — composing a key into a template and
// recognising a stored URL as a catalog entry are the parts that can be wrong, and
// they are worth much more asserted directly than inferred from a rendered <select>.
//
// 🔴 THE CATALOG IS A LICENCE FEATURE WEARING A CONVENIENCE FEATURE'S CLOTHES. The
// form it sits above has always validated the SHAPE of an attribution and never its
// CORRECTNESS: an operator could paste OpenStreetMap's tile URL, type "© Acme", and
// the platform would accept it. Prefilling the credit line the provider's own licence
// requires is what makes the correct value the default path — which is why every entry
// carries its provenance and why a wrong entry is worse than an absent one.
// See PROVENANCE.md beside this file.

import catalogData from './catalog.json';

/** One selectable map from a provider — a template plus the credit line it requires. */
export interface CatalogSource {
  id: string;
  name: string;
  /** May contain {apiKey}; see composeTileUrl. */
  tileUrl: string;
  attribution: string;
}

export interface CatalogProvider {
  id: string;
  name: string;
  /** The provider's own terms/pricing page. We link it and never paraphrase it. */
  termsUrl: string;
  templateSource: string;
  attributionSource: string;
  /** Where to get a key, for the providers that need one. */
  keyUrl: string | null;
  sources: CatalogSource[];
}

export const PROVIDERS: readonly CatalogProvider[] = catalogData.providers;

// 🔴 OUR token, not MapLibre's — so a stored URL still carrying this one would have the
// literal text "{apiKey}" sent to the provider on every tile request.
//
// The set MapLibre DOES substitute is deliberately not written out here. An earlier
// draft enumerated it from memory, omitted {prefix}, and put that false list into the
// server's error message and the published docs. The authority is MapLibre's own
// source, compared against the server's allow-list by placeholders.test.ts. That is why composeTileUrl leaves it INTACT rather than substituting a
// blank: an intact placeholder is refused by both this form and the server, whereas a
// blank would compose to `?apikey=` and sail through every check into storage.
export const API_KEY_TOKEN = '{apiKey}';

/** The sentinel for "not a catalog provider" — a custom URL, or an inherited one. */
export const CUSTOM_PROVIDER_ID = 'custom';

/** Whether this source composes a key into its template. Derived, never declared. */
export function needsApiKey(source: CatalogSource): boolean {
  return source.tileUrl.includes(API_KEY_TOKEN);
}

export function providerNeedsApiKey(provider: CatalogProvider): boolean {
  return provider.sources.some(needsApiKey);
}

/**
 * Build the tile URL that will actually be stored.
 *
 * The key is a FIELD rather than something pasted inside a URL, so it can be rotated
 * without re-pasting a template — and so the catalog owns the exact placement the
 * provider documents, which is the part people get wrong.
 *
 * 🔴 It is URL-ENCODED. A provider key is opaque text, and one containing `&` or `#`
 * pasted raw would terminate the query string and silently produce a different
 * request than the one shown in the field.
 *
 * 🔴 A BLANK KEY LEAVES THE PLACEHOLDER IN PLACE. See API_KEY_TOKEN — this is the
 * fail-closed half, and substituting an empty string here would be the bug.
 */
export function composeTileUrl(template: string, apiKey: string): string {
  const key = apiKey.trim();
  if (key === '') return template;
  return template.split(API_KEY_TOKEN).join(encodeURIComponent(key));
}

export interface Recognized {
  provider: CatalogProvider;
  source: CatalogSource;
  /** '' when the template's key placeholder is still unsubstituted. */
  apiKey: string;
}

/**
 * Read a stored tile URL back as a catalog entry, recovering the API key.
 *
 * Without this the picker would be write-only: reopening the page on a saved
 * Thunderforest URL would show "Custom…" with an empty key box, and switching style
 * would compose a template with the key the operator had already supplied thrown
 * away. Deriving the selection from the URL on every render — rather than holding it
 * in state — also means editing the raw Tile URL field keeps the dropdown honest
 * instead of leaving it pointing at a provider the URL no longer describes.
 *
 * Returns null for anything the catalog does not describe, which is the correct answer
 * for a private tile server and for an inherited blank alike.
 */
export function recognize(tileUrl: string): Recognized | null {
  const url = tileUrl.trim();
  if (url === '') return null;

  for (const provider of PROVIDERS) {
    for (const source of provider.sources) {
      const idx = source.tileUrl.indexOf(API_KEY_TOKEN);
      if (idx < 0) {
        if (url === source.tileUrl) return { provider, source, apiKey: '' };
        continue;
      }
      const prefix = source.tileUrl.slice(0, idx);
      const suffix = source.tileUrl.slice(idx + API_KEY_TOKEN.length);
      if (!url.startsWith(prefix) || !url.endsWith(suffix)) continue;
      // Guard the degenerate overlap: a URL shorter than prefix+suffix can satisfy
      // both tests using the same characters, yielding a negative-length middle.
      //
      // 🔴 Unreachable with the CURRENT catalog and kept deliberately: every keyed
      // template today ends in {apiKey}, so the suffix is empty and this reduces to
      // the startsWith check above. It becomes live the first time a provider puts
      // anything after its key — `?key={apiKey}&format=png` — which is a template
      // shape several providers use. Noted rather than deleted, and noted rather than
      // claimed as tested, because nothing in the suite can currently reach it.
      if (url.length < prefix.length + suffix.length) continue;
      const middle = url.slice(prefix.length, url.length - suffix.length);
      if (middle === API_KEY_TOKEN) return { provider, source, apiKey: '' };
      return { provider, source, apiKey: safeDecode(middle) };
    }
  }
  return null;
}

// decodeURIComponent throws on a malformed escape ("%zz"). A stored URL is operator
// input and may predate this picker, so a bad escape must degrade to showing the raw
// text rather than taking the settings page down.
function safeDecode(v: string): string {
  try {
    return decodeURIComponent(v);
  } catch {
    return v;
  }
}

export function findProvider(id: string): CatalogProvider | undefined {
  return PROVIDERS.find((p) => p.id === id);
}
