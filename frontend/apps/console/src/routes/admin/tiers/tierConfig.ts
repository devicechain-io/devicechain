// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tier settings blob, read and written safely (ADR-065).
//
// This is pure and separate from the form because it is the dangerous part: a tier's
// config is its packaging, and clearing it silently re-prices every tenant at that
// tier within a minute. The form renders; this decides what to send.
import { parseJsonObject } from '@/lib/utils';

// A dimension, reduced to the two config keys it owns. Structural on purpose — the
// caller passes the server's dimension list, and this needs nothing else from it.
export type ConfigDimension = { rateField: string; burstField: string };

// The scalar keys a tier carries beside its rate+burst dimensions. They are not dimensions
// (no burst, no per-second unit), so the server's dimension query does not list them and
// this editor declares them itself. Ranges mirror the server's validators in
// iam/tierconfig.go: a whole number from min (1 unless stated) to max. The geofence maxima are the platform
// constants in core/governance (geofence_caps.go); the held ceiling's is the
// representational int32 wall. A value outside them is refused at write by the server, so
// checking here only moves the same refusal in front of the operator.
//
// The numbers are copied, not served: no query exposes them. tierConfig.test.ts reads the
// Go source and fails when a maximum here stops matching it, so a raised platform constant
// cannot leave this form refusing a value the server would accept.
export type TierScalar = {
  key: string;
  // The smallest legal value. Omitted means 1: every ceiling but one refuses 0, because 0
  // would admit nothing. calculatedMetricsPerProfile is that one: 0 is how a tier says
  // calculated metrics are off, and it is the platform default too. integrationsPerTenant
  // also takes 0 (a tier that holds no integrations), though its platform default is 5.
  min?: number;
  max: number;
  labelKey: string;
  descriptionKey: string;
};

export const TIER_SCALARS: readonly TierScalar[] = [
  { key: 'heldCommandCeiling', max: 2147483647, labelKey: 'heldCommandCeilingLabel', descriptionKey: 'heldCommandCeilingDescription' },
  { key: 'shedPriority', max: 100, labelKey: 'shedPriorityLabel', descriptionKey: 'shedPriorityDescription' },
  { key: 'geoFenceCeiling', max: 4000, labelKey: 'geoFenceCeilingLabel', descriptionKey: 'geoFenceCeilingDescription' },
  { key: 'geoFencePositionCeiling', max: 1024, labelKey: 'geoFencePositionCeilingLabel', descriptionKey: 'geoFencePositionCeilingDescription' },
  { key: 'geoFencePositionBudget', max: 128000, labelKey: 'geoFencePositionBudgetLabel', descriptionKey: 'geoFencePositionBudgetDescription' },
  { key: 'calculatedMetricsPerProfile', min: 0, max: 32, labelKey: 'calculatedMetricsPerProfileLabel', descriptionKey: 'calculatedMetricsPerProfileDescription' },
  { key: 'integrationsPerTenant', min: 0, max: 100, labelKey: 'integrationsPerTenantLabel', descriptionKey: 'integrationsPerTenantDescription' },
];

// The boolean keys a tier carries. They are flags, not ceilings: absent means off, and there is no
// platform default to inherit. The editor holds one as the string 'true' (on) or '' (off), in the
// same settings map as the numbers, so a flag that is off is simply not written. Mirrors
// TierFlagKeys in iam/tierconfig.go.
export type TierFlag = { key: string; labelKey: string; descriptionKey: string };

export const TIER_FLAGS: readonly TierFlag[] = [
  { key: 'mfaRequired', labelKey: 'mfaRequiredLabel', descriptionKey: 'mfaRequiredDescription' },
];

/** The smallest legal value of a scalar. */
export function tierScalarMin(s: TierScalar): number {
  return s.min ?? 1;
}

/**
 * The scalar a field's text is not acceptable for, or null when every one is blank or valid.
 * Blank is valid: omitting the key is how a tier inherits the platform default.
 */
export function invalidTierScalar(settings: Record<string, string>): TierScalar | null {
  for (const s of TIER_SCALARS) {
    const raw = (settings[s.key] ?? '').trim();
    if (raw === '') continue;
    const n = Number(raw);
    if (!Number.isInteger(n) || n < tierScalarMin(s) || n > s.max) return s;
  }
  return null;
}

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
    // A flag is read only when it is exactly true: anything else (false, a junk value parked by
    // an out-of-band write) is off, as the server reads it.
    if (v === true && TIER_FLAGS.some((f) => f.key === k)) out[k] = 'true';
  }
  return out;
}

// Shared with the other readers of an operator-authored JSON setting — see
// parseJsonObject in lib/utils.ts.
const parseObject = parseJsonObject;

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

  // The scalar keys follow the same rule as a dimension's fields: blank omits, a number is
  // written. A value that is not a valid number is never written (the form refuses it
  // before this runs), so the key is dropped rather than sent as garbage.
  for (const s of TIER_SCALARS) {
    known.add(s.key);
    const raw = (settings[s.key] ?? '').trim();
    if (raw === '') continue;
    const n = Number(raw);
    if (Number.isFinite(n)) rendered[s.key] = n;
  }

  // A flag that is on is written as true; off omits the key (the server reads absent as off).
  for (const f of TIER_FLAGS) {
    known.add(f.key);
    if (settings[f.key] === 'true') rendered[f.key] = true;
  }

  // Anything the editor has no field for survives untouched.
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(parseObject(existingConfig))) {
    if (!known.has(k)) out[k] = v;
  }
  return JSON.stringify({ ...out, ...rendered });
}
