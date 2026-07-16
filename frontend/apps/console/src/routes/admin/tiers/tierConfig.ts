// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tier settings blob, read and written safely (ADR-065).
//
// This is pure and separate from the form because it is the dangerous part: a tier's
// config is its packaging, and clearing it silently re-prices every tenant at that
// tier within a minute. The form renders; this decides what to send.

// A dimension, reduced to the two config keys it owns. Structural on purpose — the
// caller passes the server's dimension list, and this needs nothing else from it.
export type ConfigDimension = { rateField: string; burstField: string };

// parseTierConfig reads a tier's stored config blob into the numeric values the
// editor renders, as strings.
//
// A tier that declares nothing (the seeded standard tier) carries null, which is a
// valid and meaningful state — "inherit the platform default everywhere" — not an
// error. Non-numeric values are skipped here because the editor has no field for
// them; they are NOT lost, see buildTierConfigPatch.
export function parseTierConfig(raw: string | null | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(parseObject(raw))) {
    if (typeof v === 'number') out[k] = String(v);
  }
  return out;
}

// parseObject decodes a JSON object string, tolerating null/garbage as "empty".
function parseObject(raw: string | null | undefined): Record<string, unknown> {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return parsed as Record<string, unknown>;
  } catch {
    return {};
  }
}

// buildTierConfigPatch decides what to send as a tier's `config` on save, returning
// UNDEFINED to mean "leave the tier's settings exactly as they are".
//
// Undefined is the whole point of this function. The server makes config a PATCH —
// omitted means untouched, explicit "{}" means CLEAR — precisely so that a rename
// cannot wipe a tier's packaging. A form that always sends config defeats that from
// the client: the editor's fields are built from the dimensions query, so when that
// query is in flight or has failed there are no fields, and an unconditional save
// would send "{}" — not because the operator cleared anything, but because the form
// never showed them anything to clear. That would drop every tenant at the tier to
// the platform default off a query failure nobody saw.
//
// So: no dimensions, no claim about settings.
//
// When the editor IS live, unrecognized keys are preserved verbatim rather than
// dropped. Today every registered key is a numeric rate/burst, so there are none —
// but the tier's config is where a model menu lands (ADR-065 S5, the tier↔provider
// join), and that key is not a number the editor renders. Rebuilding config from only
// the rendered fields would silently delete it on the next rename. Preserving is the
// behavior that stays correct when that key arrives, instead of the one that quietly
// starts destroying it.
export function buildTierConfigPatch(
  dimensions: readonly ConfigDimension[] | null | undefined,
  settings: Record<string, string>,
  existingConfig: string | null | undefined,
): string | undefined {
  // The editor never rendered — say nothing about settings.
  if (dimensions == null || dimensions.length === 0) return undefined;

  const rendered: Record<string, unknown> = {};
  const known = new Set<string>();
  for (const d of dimensions) {
    for (const key of [d.rateField, d.burstField]) {
      known.add(key);
      const raw = (settings[key] ?? '').trim();
      // An empty field OMITS its key — that is how a tier says "inherit the platform
      // default", and it is the one legitimate way to clear a ceiling. It is not the
      // same as zero, which is not writable at all (a zero ceiling admits nothing).
      if (raw === '') continue;
      const n = Number(raw);
      if (Number.isFinite(n)) rendered[key] = n;
    }
  }

  // Anything the editor has no field for survives untouched.
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(parseObject(existingConfig))) {
    if (!known.has(k)) out[k] = v;
  }
  return JSON.stringify({ ...out, ...rendered });
}
