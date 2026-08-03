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
// Every failure is a returned message, never a throw: this is the one place untrusted
// text enters the viewer, and a white screen would be a worse report than any wording.

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

// Matches dashboard-management's server-side definition cap (1 MiB).
export const MAX_PASTE_BYTES = 1 << 20;

export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'Unexpected error';
}

// loadDashboard parses a pasted definition and an optional pasted binding manifest.
// The manifest it returns is the host's OVERRIDE map, not the bindings the board
// renders against: merging it over the definition's slot defaults is effectiveBindings,
// which View does, so what the user typed survives as its own thing.
export function loadDashboard(
  definitionText: string,
  manifestText: string,
): { loaded: Loaded } | { error: string } {
  // Bound the paste on the main thread (parse is synchronous) — matches the
  // server's definition cap; a giant paste would only freeze this tab.
  if (definitionText.length > MAX_PASTE_BYTES) {
    return { error: 'Definition is too large (over 1 MiB).' };
  }

  // The definition (required).
  let definition: DashboardDefinition;
  try {
    definition = migrateToSlots(parseDashboardDefinition(JSON.parse(definitionText)));
  } catch (err) {
    return { error: `Definition: ${errorMessage(err)}` };
  }

  // The manifest (optional — empty → no overrides).
  if (manifestText.trim() === '') return { loaded: { definition, manifest: {} } };

  let rawManifest: unknown;
  try {
    rawManifest = JSON.parse(manifestText);
  } catch (err) {
    return { error: `Binding manifest: ${errorMessage(err)}` };
  }
  if (rawManifest === null || typeof rawManifest !== 'object' || Array.isArray(rawManifest)) {
    return { error: 'Binding manifest must be a JSON object of slot → binding.' };
  }

  // Surface dropped entries (a typo'd shape) rather than silently binding the
  // wrong entity — or, for a stripped template, an unexplained blank widget. The
  // parser reports which slots it refused, so this names them.
  const { bindings: manifest, dropped } = parseBindingManifest(rawManifest);
  if (dropped.length > 0) {
    return {
      error:
        `Binding manifest: ${dropped.map((slot) => `"${slot}"`).join(', ')} ` +
        `${dropped.length === 1 ? 'was' : 'were'} ignored — ` +
        'check the shape (kind + deviceToken, or anchor with a targetToken).',
    };
  }

  return { loaded: { definition, manifest } };
}
