// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// English catalogs that ship WITH a lazily loaded route instead of in the first download.
//
// config.ts bundles every English catalog so the first paint needs no request, which is right for
// the screens everyone meets and wrong for an administrative corner few operators open: its
// catalog would be charged to everyone's first download. A namespace that belongs to such a
// corner is listed in NAMESPACES (so other locales load it on demand like any other) but is NOT
// imported by config.ts. It has a small catalog module beside its screens that calls
// `registerEnglish` and is imported, for its side effect, by every module that uses the
// namespace; it therefore lands in the same chunk as the screens and registers before any of
// them renders.
//
// 🔴 EVERY MODULE THAT CALLS t('<ns>:…') OR useTranslation('<ns>') MUST IMPORT THE CATALOG
// MODULE. A module that does not renders raw key names whenever it is reached without the
// others (a different route, a test). Imports are deduplicated, so importing it everywhere costs
// nothing. Worked example: routes/trust-anchors/catalog.ts.

import i18n from 'i18next';

export function registerEnglish(namespace: string, catalog: Record<string, string>): void {
  i18n.addResourceBundle('en', namespace, catalog, true, true);
}
