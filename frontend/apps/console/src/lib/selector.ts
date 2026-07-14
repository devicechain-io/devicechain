// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Composes a CEL membership selector from a set of facet conditions the browse UI
// picks (ADR-061 G4). It emits ONLY the lowerable facet-predicate subset the
// device-management selector engine accepts (SD-3): `attr[<key>] <op> <literal>`,
// `<key> in attr`, joined by `&&`, with a per-value `||` fan-out for a multi-value
// equality. The backend re-compiles + cost-gates + lowers whatever it is sent, so
// this composer is a convenience that produces a valid candidate — never the
// authority. The composed string is exactly what previewSelector evaluates and what
// a saved dynamic group stores.

// A facet's stored value type (mirrors the backend AttributeValueType vocabulary).
export type FacetValueType = 'STRING' | 'LONG' | 'DOUBLE' | 'BOOLEAN' | 'JSON';

// The comparison a condition applies. `present` is `<key> in attr` (has the facet at
// all); `eq`/`neq` compare the value; the four numeric operators apply to LONG/DOUBLE.
export type FacetOperator = 'present' | 'eq' | 'neq' | 'lt' | 'lte' | 'gt' | 'gte';

// One picked facet condition. `values` is the raw string form of each chosen value;
// only `eq` uses more than one (a vocabulary multi-select → an OR of equalities).
export interface FacetCondition {
  key: string;
  valueType: FacetValueType;
  operator: FacetOperator;
  values: string[];
  // Optional display label used only in issue messages; defaults to `key`.
  label?: string;
}

export interface BuiltSelector {
  // The composed CEL, or null when no condition contributes a usable predicate.
  selector: string | null;
  // Human-readable reasons a condition was dropped (e.g. a non-numeric value under a
  // numeric operator). Surfaced in the UI so a half-typed condition is explained, not
  // silently ignored.
  issues: string[];
}

// The backend caps a selector at MaxSelectorLeaves EXISTS semi-joins (compile.go). A
// multi-value equality fans out to one leaf per value, so the composer can exceed it;
// mirror the bound here to fail with a friendly issue instead of a raw backend error.
export const MAX_SELECTOR_LEAVES = 32;

// Escape a Go/CEL double-quoted string literal body. CEL string literals take
// C-style escapes; we only need to neutralize the delimiter, the escape char, and
// the common control chars so a facet value with a quote/newline can't break out.
function celQuote(s: string): string {
  const escaped = s
    .replace(/\\/g, '\\\\')
    .replace(/"/g, '\\"')
    .replace(/\n/g, '\\n')
    .replace(/\r/g, '\\r')
    .replace(/\t/g, '\\t');
  return `"${escaped}"`;
}

// The `attr["key"]` index expression, key safely quoted.
function attrIndex(key: string): string {
  return `attr[${celQuote(key)}]`;
}

// Render a chosen value as its CEL literal, or null if it is not valid for the type
// (e.g. a non-numeric string under LONG/DOUBLE, a non-boolean under BOOLEAN). A JSON
// facet has no scalar literal form and never composes here.
function literalFor(valueType: FacetValueType, raw: string): string | null {
  switch (valueType) {
    case 'STRING':
      return celQuote(raw);
    case 'LONG': {
      // A LONG facet takes an integer literal (a fractional value would compose a
      // consistent-but-useless group that matches nothing).
      const trimmed = raw.trim();
      if (!/^-?[0-9]+$/.test(trimmed)) return null;
      return trimmed;
    }
    case 'DOUBLE': {
      // A CEL-parseable decimal literal: optional leading minus (CEL has no unary
      // plus), a digit on at least one side of the dot, optional exponent. This is
      // tighter than the backend's numeric storage form on purpose — the value is
      // interpolated as a raw CEL literal, so it must parse, not just cast.
      const trimmed = raw.trim();
      if (!/^-?([0-9]+(\.[0-9]+)?|\.[0-9]+)([eE][+-]?[0-9]+)?$/.test(trimmed)) {
        return null;
      }
      return trimmed;
    }
    case 'BOOLEAN': {
      const v = raw.trim().toLowerCase();
      if (v === 'true') return 'true';
      if (v === 'false') return 'false';
      return null;
    }
    case 'JSON':
      return null;
  }
}

const NUMERIC_OP: Partial<Record<FacetOperator, string>> = {
  lt: '<',
  lte: '<=',
  gt: '>',
  gte: '>=',
};

// Build the CEL fragment for a single condition, or null (with a reason) if it does
// not yield a usable predicate. `leaves` is the number of EXISTS semi-joins the fragment
// lowers to (one per OR term), used to enforce the backend's leaf cap.
function fragmentFor(cond: FacetCondition): { cel: string | null; issue?: string; leaves: number } {
  const idx = attrIndex(cond.key);
  const label = cond.label ?? cond.key;

  if (cond.operator === 'present') {
    return { cel: `${celQuote(cond.key)} in attr`, leaves: 1 };
  }

  const chosen = cond.values.map((v) => v.trim()).filter((v) => v.length > 0);
  if (chosen.length === 0) {
    return { cel: null, leaves: 0 }; // an empty condition simply doesn't contribute (no issue)
  }

  if (cond.operator === 'eq' || cond.operator === 'neq') {
    const op = cond.operator === 'eq' ? '==' : '!=';
    const literals = chosen.map((v) => literalFor(cond.valueType, v));
    // `== null` catches both null and an unrecognized valueType's undefined (defensive
    // against the server growing a value type the UI hasn't mapped).
    if (literals.some((l) => l == null)) {
      return {
        cel: null,
        issue: `“${label}”: a value is not valid for type ${cond.valueType}.`,
        leaves: 0,
      };
    }
    // Multiple values only compose for equality (an OR of matches). For inequality a
    // multi-value has no single unambiguous reading, so the UI restricts neq to one.
    if (cond.operator === 'neq' && literals.length > 1) {
      return { cel: null, issue: `“${label}”: “is not” takes a single value.`, leaves: 0 };
    }
    const terms = literals.map((l) => `${idx} ${op} ${l}`);
    return {
      cel: terms.length === 1 ? terms[0] : `(${terms.join(' || ')})`,
      leaves: terms.length,
    };
  }

  // A numeric comparison — single value, LONG/DOUBLE only.
  const sqlOp = NUMERIC_OP[cond.operator];
  if (!sqlOp) return { cel: null, leaves: 0 };
  if (cond.valueType !== 'LONG' && cond.valueType !== 'DOUBLE') {
    return {
      cel: null,
      issue: `“${label}”: a numeric comparison needs a LONG or DOUBLE facet.`,
      leaves: 0,
    };
  }
  const lit = literalFor(cond.valueType, chosen[0]);
  if (lit == null) {
    return { cel: null, issue: `“${label}”: “${chosen[0]}” is not a number.`, leaves: 0 };
  }
  return { cel: `${idx} ${sqlOp} ${lit}`, leaves: 1 };
}

// Compose the conditions into one CEL selector, ANDing every usable fragment. Returns
// null when nothing usable was picked (the caller then shows no preview rather than an
// empty-string selector, which the backend would reject as non-boolean).
export function buildSelector(conditions: FacetCondition[]): BuiltSelector {
  const fragments: string[] = [];
  const issues: string[] = [];
  let leaves = 0;
  for (const cond of conditions) {
    const frag = fragmentFor(cond);
    if (frag.issue) issues.push(frag.issue);
    if (frag.cel) {
      fragments.push(frag.cel);
      leaves += frag.leaves;
    }
  }
  if (fragments.length === 0) {
    return { selector: null, issues };
  }
  // Fail closed on the leaf cap with a friendly issue rather than sending a selector the
  // backend would reject with a raw "exceeds the limit" error.
  if (leaves > MAX_SELECTOR_LEAVES) {
    issues.push(
      `Too many facet comparisons (${leaves}); the limit is ${MAX_SELECTOR_LEAVES}. Remove some values or axes.`,
    );
    return { selector: null, issues };
  }
  const selector = fragments.length === 1 ? fragments[0] : fragments.join(' && ');
  return { selector, issues };
}
