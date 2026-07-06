// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure form logic for the command-button widget's typed parameter form (ADR-043). The
// console bakes a command definition's `parameterSchema` (a JSON string) into the
// widget's options; these helpers parse it, seed defaults, validate operator input, and
// serialize the typed values back to the JSON payload command-delivery sends the device.
// Kept separate from the widget so the coercion/validation is unit-tested in isolation.
//
// SCOPE: the form types top-level SCALAR parameters (DOUBLE / INT / BOOLEAN / STRING,
// with enum / bounds / required / default). OBJECT (nested) parameters are not
// form-editable — the form blocks sending when one is REQUIRED (it can't be satisfied
// here) and otherwise omits it. The payload is a flat JSON object keyed by parameter name.

import type { CommandParameter } from '@devicechain/dashboards';

const DATA_TYPES = new Set(['DOUBLE', 'INT', 'BOOLEAN', 'STRING']);

// parseParameterSchema turns the baked JSON string into descriptors, defensively:
// a null/empty/malformed schema (or a non-array root) yields an empty list so a command
// with no arguments (or a hand-broken schema) renders as a bare Send button, never throws.
// Each descriptor is NORMALIZED (enum→string[], default→string, bounds→finite number,
// kind/dataType→known value) so a hand-edited dashboard definition with wrong-typed fields
// (e.g. `enum: "on"` or `default: 5`) can't crash the form at render/send time.
export function parseParameterSchema(schema: string | null | undefined): CommandParameter[] {
  if (!schema) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(schema);
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  const out: CommandParameter[] = [];
  for (const raw of parsed) {
    const norm = normalizeParam(raw);
    if (norm) out.push(norm);
  }
  return out;
}

// normalizeParam validates one descriptor and coerces every field to its expected type,
// dropping the descriptor entirely if it has no string `name`. Unknown/wrong-typed fields
// are discarded rather than trusted, so downstream code never sees a non-array enum or a
// numeric default.
function normalizeParam(raw: unknown): CommandParameter | null {
  if (typeof raw !== 'object' || raw === null) return null;
  const r = raw as Record<string, unknown>;
  if (typeof r.name !== 'string') return null;

  const param: CommandParameter = { name: r.name };
  if (typeof r.description === 'string') param.description = r.description;
  if (r.kind === 'OBJECT') param.kind = 'OBJECT';
  else if (r.kind === 'SCALAR') param.kind = 'SCALAR';
  if (typeof r.dataType === 'string' && DATA_TYPES.has(r.dataType)) {
    param.dataType = r.dataType as CommandParameter['dataType'];
  }
  if (typeof r.unit === 'string') param.unit = r.unit;
  if (r.required === true) param.required = true;
  if (typeof r.default === 'string') param.default = r.default;
  if (typeof r.minValue === 'number' && Number.isFinite(r.minValue)) param.minValue = r.minValue;
  if (typeof r.maxValue === 'number' && Number.isFinite(r.maxValue)) param.maxValue = r.maxValue;
  if (Array.isArray(r.enum)) {
    const values = r.enum.filter((v): v is string => typeof v === 'string');
    if (values.length > 0) param.enum = values;
  }
  return param;
}

// isScalar reports whether a parameter is a typed single value the form renders an input
// for. Kind absent means SCALAR (the Go default); only an explicit OBJECT is nested.
export function isScalar(param: CommandParameter): boolean {
  return param.kind !== 'OBJECT';
}

// parseBool interprets a schema/string boolean the way the server's strconv.ParseBool
// does (accepting '1'/'t'/'true'/'yes'/'on', case-insensitively) so a definition default
// of "True" doesn't silently render — and send — as false.
export function parseBool(raw: string): boolean {
  const s = raw.trim().toLowerCase();
  return s === 'true' || s === '1' || s === 't' || s === 'yes' || s === 'on';
}

// defaultValues seeds the form's value map from each scalar parameter. A BOOLEAN always
// gets a concrete 'true'/'false' (default false) — a checkbox is never "empty", so the
// value must reflect the box's state exactly. Other scalars seed from their declared
// default (as a string — inputs are string-backed), else start empty.
export function defaultValues(params: CommandParameter[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (const p of params) {
    if (!isScalar(p)) continue;
    if (p.dataType === 'BOOLEAN') values[p.name] = parseBool(p.default ?? '') ? 'true' : 'false';
    else if (p.default != null) values[p.name] = p.default;
  }
  return values;
}

// validateParams returns a per-parameter error map (empty = valid). It enforces required
// (a missing value on a required scalar; a BOOLEAN always has a value so is never
// "required-missing"), numeric validity/bounds for INT/DOUBLE, and blocks a REQUIRED
// OBJECT parameter (it can't be supplied through the typed form).
export function validateParams(
  params: CommandParameter[],
  values: Record<string, string>,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const p of params) {
    if (!isScalar(p)) {
      // A required structured parameter can't be satisfied here — refuse to send an
      // incomplete command rather than silently omitting it.
      if (p.required) errors[p.name] = 'Structured parameter — not supported here';
      continue;
    }
    if (p.dataType === 'BOOLEAN') continue; // a checkbox always carries a valid value
    const raw = (values[p.name] ?? '').trim();
    if (raw === '') {
      if (p.required) errors[p.name] = 'Required';
      continue; // an empty optional value is fine — it's just omitted from the payload
    }
    if (p.dataType === 'INT' || p.dataType === 'DOUBLE') {
      const n = Number(raw);
      if (!Number.isFinite(n)) {
        errors[p.name] = 'Must be a number';
      } else if (p.dataType === 'INT' && !Number.isInteger(n)) {
        errors[p.name] = 'Must be a whole number';
      } else if (p.minValue != null && n < p.minValue) {
        errors[p.name] = `Must be ≥ ${p.minValue}`;
      } else if (p.maxValue != null && n > p.maxValue) {
        errors[p.name] = `Must be ≤ ${p.maxValue}`;
      }
    }
  }
  return errors;
}

// buildPayload serializes the typed values to the JSON payload string command-delivery
// sends the device — coercing each scalar to its declared type (INT/DOUBLE → number,
// BOOLEAN → boolean, STRING → string). A BOOLEAN is always included (its checkbox state
// is meaningful); other empty optional values are omitted. OBJECT parameters are omitted
// (validateParams blocks a required one first). Returns undefined when no parameter
// contributes a value (a parameterless command sends no payload).
export function buildPayload(
  params: CommandParameter[],
  values: Record<string, string>,
): string | undefined {
  const payload: Record<string, unknown> = {};
  for (const p of params) {
    if (!isScalar(p)) continue;
    const raw = (values[p.name] ?? '').trim();
    if (p.dataType === 'BOOLEAN') {
      payload[p.name] = parseBool(raw);
      continue;
    }
    if (raw === '') continue;
    payload[p.name] = coerce(p, raw);
  }
  return Object.keys(payload).length === 0 ? undefined : JSON.stringify(payload);
}

function coerce(param: CommandParameter, raw: string): unknown {
  switch (param.dataType) {
    case 'INT':
    case 'DOUBLE': {
      const n = Number(raw);
      return Number.isFinite(n) ? n : raw; // validation gates this; fall back to raw defensively
    }
    case 'BOOLEAN':
      return parseBool(raw);
    default:
      return raw; // STRING (and untyped)
  }
}
