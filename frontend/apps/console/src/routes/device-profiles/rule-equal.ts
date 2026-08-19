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

// elided reports whether a value is one Go's `omitempty` would leave out of the wire form —
// so a key carrying it and a key that is absent decode to the SAME rules.Rule.
//
// 🔴 ZERO IS NOT IN THIS SET, AND THAT IS THE ONE DELIBERATE ASYMMETRY. `Threshold` is a
// *float64: omitempty on a pointer elides only nil, so `"threshold": 0` genuinely differs
// from an absent threshold and survives a Go round-trip. Treating 0 as elidable would make a
// rebuild that DROPPED a zero threshold compare equal to the stored rule — a missed loss,
// which is the failure this whole check exists to prevent. `"count": 0` therefore still
// reports as a difference, and that is the trade taken knowingly: a warning nobody needed
// costs an operator one sentence, a silent rewrite costs them the rule.
function elided(v: unknown): boolean {
  if (v === null) return true;
  if (v === false) return true;
  if (v === '') return true;
  if (Array.isArray(v)) return v.length === 0;
  return typeof v === 'object' && stableStringify(v) === '{}';
}

// prune removes the keys that carry nothing, recursively, so the containment check below
// compares two definitions the way the SERVER would read them rather than the way they happen
// to be spelled.
//
// It matters because the two sides are spelled by different writers. `buildDefinition` omits a
// field it has nothing to say about; a client whose serializer emits empty collections and
// strings — the default for .NET and many Python setups — writes `"description": ""` and
// `"actions": []` instead. Go's decoder cannot tell those apart, so neither may this: without
// the normalization the form would warn that it was about to lose a description that was never
// there.
function prune(v: unknown): unknown {
  if (v === null || typeof v !== 'object') return v;
  if (Array.isArray(v)) return v.map(prune);
  const out: Record<string, unknown> = {};
  for (const [k, raw] of Object.entries(v as Record<string, unknown>)) {
    const pv = prune(raw);
    if (elided(pv)) continue;
    out[k] = pv;
  }
  return out;
}

// contains reports whether `whole` carries everything in `part`, at equal values.
function contains(whole: unknown, part: unknown): boolean {
  if (part === null || typeof part !== 'object') return stableStringify(whole) === stableStringify(part);
  if (Array.isArray(part)) {
    // Arrays are compared WHOLE, not element-wise-contained. A rule's `actions` array is
    // ordered and its length is meaningful — an action the form dropped must register as a
    // loss, and a per-element containment check would let a shortened array pass.
    return stableStringify(whole) === stableStringify(part);
  }
  if (whole === null || typeof whole !== 'object' || Array.isArray(whole)) return false;
  const w = whole as Record<string, unknown>;
  for (const [k, pv] of Object.entries(part as Record<string, unknown>)) {
    if (!(k in w)) return false;
    if (!contains(w[k], pv)) return false;
  }
  return true;
}

// ruleSurvivesRoundTrip reports whether `rebuilt` — what this form would emit for a rule it
// has just read — still carries everything `stored` said. It is the OPEN-time counterpart of
// sameLogicalRule, and it is deliberately ONE-DIRECTIONAL where that one is symmetric.
//
// 🔴 THE DIRECTION IS THE WHOLE DESIGN. Data loss is "the stored rule said something the
// rebuild does not"; the reverse — the form emitting a key the stored rule omitted, such as
// the `name: ""` that buildDefinition always writes — loses nothing and must not be reported.
// A symmetric comparison here would warn on ordinary API-authored rules that simply left an
// optional field out, and a warning that fires on healthy rules is one nobody reads.
//
// A parse failure on either side yields false: unreadable is the worst kind of lossy.
export function ruleSurvivesRoundTrip(stored: string, rebuilt: string): boolean {
  try {
    return contains(prune(JSON.parse(rebuilt)), prune(JSON.parse(stored)));
  } catch {
    return false;
  }
}
