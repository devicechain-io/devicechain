// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure form logic for the command-button widget's typed parameter form (ADR-043). The
// console bakes a command definition's `parameterSchema` (a JSON string) into the
// widget's options; these helpers parse it, seed defaults, validate operator input, and
// serialize the typed values back to the JSON payload command-delivery sends the device.
// Kept separate from the widget so the coercion/validation is unit-tested in isolation.
//
// SCOPE: the form types top-level SCALAR parameters (DOUBLE / INT / BOOLEAN / STRING,
// with enum / bounds / required / default). OBJECT (nested) parameters are surfaced as a
// read-only note and omitted from the payload — a typed nested-object form is deferred.
// The payload is a flat JSON object keyed by parameter name.

import type { CommandParameter } from '@devicechain/dashboards';

// parseParameterSchema turns the baked JSON string into descriptors, defensively:
// a null/empty/malformed schema (or a non-array root) yields an empty list so a command
// with no arguments (or a hand-broken schema) renders as a bare Send button, never throws.
export function parseParameterSchema(schema: string | null | undefined): CommandParameter[] {
  if (!schema) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(schema);
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  // Keep only well-formed descriptors (an object with a string `name`); a stray
  // non-object entry is dropped rather than crashing the form.
  return parsed.filter(
    (p): p is CommandParameter =>
      typeof p === 'object' && p !== null && typeof (p as { name?: unknown }).name === 'string',
  );
}

// isScalar reports whether a parameter is a typed single value the form renders an input
// for. Kind absent means SCALAR (the Go default); only an explicit OBJECT is nested.
export function isScalar(param: CommandParameter): boolean {
  return param.kind !== 'OBJECT';
}

// defaultValues seeds the form's value map from each scalar parameter's declared
// default (as a string — inputs are string-backed; booleans use 'true'/'false'). A
// parameter with no default starts empty.
export function defaultValues(params: CommandParameter[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (const p of params) {
    if (isScalar(p) && p.default != null) values[p.name] = p.default;
  }
  return values;
}

// validateParams returns a per-parameter error map (empty = valid). It enforces required
// (a missing value on a required scalar) and numeric validity/bounds for INT/DOUBLE. An
// OBJECT parameter is skipped (not form-editable yet).
export function validateParams(
  params: CommandParameter[],
  values: Record<string, string>,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const p of params) {
    if (!isScalar(p)) continue;
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
// BOOLEAN → boolean, STRING → string). Empty optional values are omitted. Returns
// undefined when no parameter contributes a value (a parameterless command sends no
// payload, matching the console's "payload or undefined" behavior).
export function buildPayload(
  params: CommandParameter[],
  values: Record<string, string>,
): string | undefined {
  const payload: Record<string, unknown> = {};
  for (const p of params) {
    if (!isScalar(p)) continue;
    const raw = (values[p.name] ?? '').trim();
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
      return raw === 'true';
    default:
      return raw; // STRING (and untyped)
  }
}
