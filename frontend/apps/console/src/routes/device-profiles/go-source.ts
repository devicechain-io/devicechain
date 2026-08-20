// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// TEST SUPPORT — reading Go source from the console's cross-language pins.
//
// Nothing in the app imports this; it exists so the two lockstep gates beside it
// (taxonomy-lockstep, cel-vocabulary) share one parser instead of one each.
//
// 🔴 THEY HAD ONE EACH, WITH THE SAME NAME AND DIFFERENT SEMANTICS — `constValues` in both
// files, one requiring a declared type and one accepting an optional `const ` prefix. Each
// would silently fail to parse the other's const shape. That is a bad trap in general and a
// worse one here: a hardening applied to one twin does not reach the other, and this repo has
// already had a Go-source harness broken by gofmt re-aligning a const block under it. One
// parser, hardened once.
//
// Every function here FAILS LOUDLY rather than returning something empty. A parser that
// quietly finds nothing turns the set comparison downstream into "∅ equals ∅, green" — which
// is the exact failure the gates exist to prevent, wearing the gates' own clothes.

/** A Go string const: its declared type where one is written, and its wire value. */
export interface GoConst {
  /** The declared type name (`RuleType`), or '' for `const Foo = "bar"` with no type. */
  type: string;
  value: string;
}

/**
 * Every `Ident = "value"` / `Ident Type = "value"` const in a source file.
 *
 * Handles both spellings a Go file uses: members of a grouped `const ( … )` block, which begin
 * the line, and standalone `const Ident = "value"` declarations. Missing the second cost the
 * CEL gate its only function on the first run it ever did — the environment's single
 * `cel.Function` was declared standalone, and a grouped-block-only regex could not see it.
 */
export function constValues(src: string): Map<string, GoConst> {
  const out = new Map<string, GoConst>();
  for (const m of src.matchAll(/^\s*(?:const\s+)?([A-Z][A-Za-z0-9]*)(?:\s+([A-Za-z][A-Za-z0-9]*))?\s*=\s*"([^"]*)"/gm)) {
    out.set(m[1], { type: m[2] ?? '', value: m[3] });
  }
  return out;
}

/** The body of a top-level Go func, from its signature to the next top-level `func`. */
export function funcBody(src: string, signature: string): string {
  const start = src.indexOf(signature);
  if (start < 0) throw new Error(`no func matching ${JSON.stringify(signature)}`);
  const rest = src.slice(start + signature.length);
  const end = rest.indexOf('\nfunc ');
  return end < 0 ? rest : rest.slice(0, end);
}

/** A plain `const Name = 123` (or `const Name Type = 123`), as a number. */
export function intConst(src: string, name: string): number {
  const m = new RegExp(`^\\s*(?:const\\s+)?${name}\\s*(?:[A-Za-z][A-Za-z0-9]*\\s*)?=\\s*(\\d+)`, 'm').exec(src);
  if (!m) throw new Error(`no integer const ${name}`);
  return Number(m[1]);
}

/**
 * The wire values a `switch` dispatches on, for consts of one declared Go type.
 *
 * 🔴 IT REFUSES WHAT IT CANNOT ACCOUNT FOR RATHER THAN SKIPPING IT. Every ident in a case
 * clause must resolve to a const; anything else — a `RuleType("literal")` conversion, an
 * alias, a value built by concatenation — throws. An earlier version filtered case idents by
 * NAME PREFIX instead, and a kind declared as `KindMaintenance RuleType = "maintenance"` with
 * a real case in the dispatch compiled, ran, and left the gate green: the prefix was reading a
 * naming convention, which nothing enforces, where Go's type checker enforces the type.
 *
 * `skip` names idents that legitimately appear in a scanned region and are not vocabulary
 * (`default`). A literal list, so a region that grows a new one fails rather than absorbing it.
 */
export function dispatchValues(
  region: string,
  consts: Map<string, GoConst>,
  declaredType: string,
  skip: string[] = [],
): string[] {
  const out = new Set<string>();
  for (const line of region.split('\n')) {
    const m = /^\s*case\s+(.+?):\s*$/.exec(line);
    if (!m) continue;
    for (const raw of m[1].split(',')) {
      const id = raw.trim();
      if (id === '' || skip.includes(id)) continue;
      const c = consts.get(id);
      if (c === undefined) {
        throw new Error(
          `case ${id}: is not a resolvable string const — a ${declaredType} the console cannot see would be invisible here`,
        );
      }
      if (c.type !== declaredType) continue; // a different vocabulary sharing the region
      out.add(c.value);
    }
  }
  return [...out];
}
