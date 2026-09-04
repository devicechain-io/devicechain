// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Step 2 of the viewer: turn two pasted strings into something renderable.
//
// This is the whole of /dash's own logic — everything else in the app is auth, layout
// and a hub the packages own — and it lives here rather than inside the Load component
// so a test can drive the REAL path the Render button takes. Reconstructing
// `migrateToSlots(parseDashboardDefinition(JSON.parse(...)))` in a test would prove the
// parsers work, which they already do on their own side; what is untested without this
// is the sequence /dash puts them in, which is the part that IS the embed contract.
//
// Every failure is a returned VALUE, never a throw: this is the one place untrusted
// text enters the viewer, and a white screen would be a worse report than any wording.
//
// 🔴 THE FAILURES ARE CODES, NOT SENTENCES, AND THAT IS THE WHOLE POINT OF THE TYPE.
// This module used to return six English strings. They survived the console's
// localization untouched, and they would have survived it again: the i18n lint gate
// runs in `jsx-only` mode, so it sees hard-coded prose in JSX and is STRUCTURALLY BLIND
// to a string returned from a plain .ts function. Adding /dash to that lint therefore
// does not protect this file — nothing lints it. What protects it is that `LoadError`
// has no field prose can live in: a `code` from a closed set, plus a `detail` that is
// only ever a parser's own diagnostic. Rendering happens in loadErrorMessage, in the
// i18n layer, where the catalogs and the parity gate can see it.

import {
  migrateToSlots,
  parseBindingManifest,
  parseDashboardDefinition,
  type DashboardDefinition,
  type SlotBinding,
} from '@devicechain/dashboards';

// A loaded, parsed dashboard ready to render: the definition plus the binding manifest
// (the host's override map) it renders against.
export interface Loaded {
  definition: DashboardDefinition;
  manifest: Record<string, SlotBinding>;
}

/**
 * Why a paste failed, in a form that carries no language.
 *
 * `detail` is a caught exception's own message — a JSON syntax error or a parser
 * diagnostic. It is NOT translated and must not be: it names a position or a field in
 * the pasted document, so it is the same technical text an operator would paste into a
 * bug report, and inventing a Spanish rendering of "Unexpected token } in JSON at
 * position 41" would make it harder to search, not easier to read. It is null when the
 * thrown value was not an Error at all, which the renderer fills with a localized
 * "unexpected error" rather than with a bare "null".
 */
export type LoadError =
  | { code: 'definitionTooLarge' }
  | { code: 'definitionInvalid'; detail: string | null }
  | { code: 'manifestInvalid'; detail: string | null }
  | { code: 'manifestNotObject' }
  | { code: 'manifestDropped'; slots: string[] };

// Matches dashboard-management's server-side definition cap (1 MiB).
export const MAX_PASTE_BYTES = 1 << 20;

/**
 * A thrown value's own message, or null when it was not an Error. Null rather than a
 * default sentence: the default is user-facing copy and so belongs in a catalog, and
 * returning one here is exactly how this module grew its English in the first place.
 */
export function errorDetail(err: unknown): string | null {
  return err instanceof Error ? err.message : null;
}

// loadDashboard parses a pasted definition and an optional pasted binding manifest.
// The manifest it returns is the host's OVERRIDE map, not the bindings the board
// renders against: merging it over the definition's slot defaults is effectiveBindings,
// which View does, so what the user typed survives as its own thing.
export function loadDashboard(
  definitionText: string,
  manifestText: string,
): { loaded: Loaded } | { error: LoadError } {
  // Bound the paste on the main thread (parse is synchronous) — matches the
  // server's definition cap; a giant paste would only freeze this tab.
  if (definitionText.length > MAX_PASTE_BYTES) {
    return { error: { code: 'definitionTooLarge' } };
  }

  // The definition (required).
  let definition: DashboardDefinition;
  try {
    definition = migrateToSlots(parseDashboardDefinition(JSON.parse(definitionText)));
  } catch (err) {
    return { error: { code: 'definitionInvalid', detail: errorDetail(err) } };
  }

  // The manifest (optional — empty → no overrides).
  if (manifestText.trim() === '') return { loaded: { definition, manifest: {} } };

  let rawManifest: unknown;
  try {
    rawManifest = JSON.parse(manifestText);
  } catch (err) {
    return { error: { code: 'manifestInvalid', detail: errorDetail(err) } };
  }
  if (rawManifest === null || typeof rawManifest !== 'object' || Array.isArray(rawManifest)) {
    return { error: { code: 'manifestNotObject' } };
  }

  // Surface dropped entries (a typo'd shape) rather than silently binding the
  // wrong entity — or, for a stripped template, an unexplained blank widget. The
  // parser reports which slots it refused, so this names them.
  const { bindings: manifest, dropped } = parseBindingManifest(rawManifest);
  if (dropped.length > 0) {
    return { error: { code: 'manifestDropped', slots: dropped } };
  }

  return { loaded: { definition, manifest } };
}
