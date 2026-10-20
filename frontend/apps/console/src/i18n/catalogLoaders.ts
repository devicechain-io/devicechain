// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/** A locale's catalogs as one module: `{ [namespace]: { [key]: string } }`. */
export interface CatalogModule {
  default: Record<string, Record<string, string>>;
}

/**
 * On-demand catalogs, one dynamic import (one chunk) per non-default locale. Adding a locale is
 * a `locales/<code>/index.ts` that default-exports `{ [namespace]: catalog }` plus a line here.
 *
 * The result is typed as possibly `undefined` because it can be, in production: when a chunk
 * fails and chunkReload claims the failure (preventDefault, then a page reload), Vite's preload
 * wrapper resolves the import with nothing rather than rejecting it.
 *
 * Its own module, rather than a constant inside config.ts, so a test can stand in for a chunk
 * that fails. Mocking the locale module itself does not work: vitest answers concurrent first
 * imports of a mocked module with the real one, and i18next reads up to ten namespaces at once.
 */
export const LOADERS: Record<string, () => Promise<CatalogModule | undefined>> = {
  es: () => import('./locales/es'),
};
