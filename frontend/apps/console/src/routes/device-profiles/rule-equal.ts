// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Logical equality of two rules.Rule definition JSON strings — used to decide whether a form
// edit left the rule's definition untouched, so a canvas-authored rule's AuthoringGraph
// sidecar can be preserved across an incidental edit rather than NULLed (ADR-053 / Fable 9b-1
// MED). It is order-independent because the two authoring surfaces emit different key orders
// (the form its own; the canvas the server's canonical Go marshal).

// stableStringify serializes a JSON value with object keys sorted, so two logically-equal
// values serialize identically regardless of key order. A key whose value is an empty object
// is treated as ABSENT: the canvas stores the server's canonical marshal, which always emits
// `"when":{}` (rules.Rule.When has no omitempty), while the form omits the when key entirely
// for absence / match-every rules — the two encode the same rule, so an empty object must not
// register as a difference (M1).
export function stableStringify(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v) ?? 'null';
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(',')}]`;
  const obj = v as Record<string, unknown>;
  const parts: string[] = [];
  for (const k of Object.keys(obj).sort()) {
    const sv = stableStringify(obj[k]);
    if (sv === '{}') continue; // empty object ⇔ absent key
    parts.push(`${JSON.stringify(k)}:${sv}`);
  }
  return `{${parts.join(',')}}`;
}

// sameLogicalRule reports whether two rule-definition JSON strings encode the same rules.Rule
// (order-independent). A parse failure on either side yields false (treat as changed).
export function sameLogicalRule(a: string, b: string): boolean {
  try {
    return stableStringify(JSON.parse(a)) === stableStringify(JSON.parse(b));
  } catch {
    return false;
  }
}
