// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pin between the identifiers a CEL condition may USE and the ones this console tells
// an author about.
//
// 🔴 WHAT THIS MEASURES IS DISCOVERABILITY, NOT REACHABILITY, and the distinction is the
// whole honest framing of it. The raw-CEL box already accepts `geo.inFence("yard")` — it is
// checked against the same environment that declares `geo`, so the capability WORKS today.
// What was missing is any way to find out it exists: the field's own help enumerated three
// of the environment's six variables and none of its functions, so geofencing had exactly
// one discovery surface in the entire product, and that surface was a documentation page.
//
// So a green run here does NOT mean an operator can geofence comfortably. They still cannot
// pick a fence from a list, and the token they must type is checked by nothing at publish.
// It means only that the vocabulary is no longer secret. Read that limit as written; a gate
// whose scope is overstated is worse than no gate, because it retires the question.
//
// It reads the REGISTRATION — the `cel.Variable(` / `cel.Function(` call sites that build the
// environment — rather than the `Var*` / `Func*` const names beside them. The consts are a
// naming convention one file away from what actually runs: a function registered inline as
// `cel.Function("distanceTo", …)` with no const would reproduce exactly the defect this file
// exists to catch, while a const-name parse stayed green.

import { describe, it, expect } from 'vitest';
import { constValues, type GoConst } from './go-source';
// 🔴 THE WHOLE PACKAGE, NOT A NAMED PAIR OF FILES. Reading env.go and geo.go by name was a
// demonstrated bypass: a `cel.Function("nearFence", …)` added in predicate/fences.go and wired
// into cel.NewEnv compiles, runs, and is completely invisible to a two-file scan — the gate
// stayed green over an undocumented function, which is the defect it exists to catch. A glob
// cannot fall behind a new file the way a list can.
const PREDICATE_FILES = import.meta.glob(
  [
    '../../../../../../backend/services/event-processing/internal/detect/predicate/*.go',
    // 🔴 A Go TEST FILE IS NOT THE ENVIRONMENT. They declare no registrations today, so the
    // gate reads the same set either way — but a const in a _test.go sharing an identifier
    // with a real one would silently overwrite it in the lookup, and a registration written
    // in a test would demand that the console document an identifier production never
    // declares. Both fail in the confusing direction.
    '!**/*_test.go',
  ],
  {
    query: '?raw',
    import: 'default',
    eager: true,
  },
) as Record<string, string>;
// Globbed rather than named, matching i18n/parity.test.ts — the console's every-locale gate
// derives its set from the filesystem for the same reason this must: a third locale added
// later would pass parity wiring while its CEL help sat ungated behind two hardcoded imports.
const LOCALE_FILES = import.meta.glob('../../i18n/locales/*/deviceProfiles.json', { eager: true }) as Record<
  string,
  { default: Record<string, string> }
>;

const PREDICATE_SRC = Object.keys(PREDICATE_FILES)
  .sort()
  .map((f) => PREDICATE_FILES[f])
  .join('\n');

const CONSTS = constValues(PREDICATE_SRC);

/**
 * The identifiers registered into the CEL environment, by call site.
 *
 * A registration names its identifier either as a const (`cel.Variable(VarGeo, …)`) or as a
 * bare literal (`cel.Function("distanceTo", …)`). Both are read, because the second is the
 * one a const-name parse would miss.
 */
function registered(
  kind: 'Variable' | 'Function',
  src: string = PREDICATE_SRC,
  consts: Map<string, GoConst> = CONSTS,
): string[] {
  const out = new Set<string>();
  for (const m of src.matchAll(new RegExp(`cel\\.${kind}\\(\\s*([A-Za-z0-9_"]+)`, 'g'))) {
    const arg = m[1];
    if (arg.startsWith('"')) {
      out.add(arg.slice(1, -1));
      continue;
    }
    const v = consts.get(arg)?.value;
    // An identifier we cannot resolve is NOT skipped. Skipping is how a real one goes
    // missing while every count still looks right.
    if (v === undefined) throw new Error(`cel.${kind}(${arg}, …) — ${arg} has no string literal value`);
    out.add(v);
  }
  return [...out];
}

/**
 * Whether `name` appears in `text` as a standalone token.
 *
 * 🔴 A PLAIN `includes` WOULD MAKE THIS GATE VIRTUALLY VACUOUS. The identifiers are `m`,
 * `attr`, `device`, `anchors`, `occurred` and `geo` — and "m" is inside "measurement", "geo"
 * is inside "geocercas" in the Spanish copy, "attr" is inside "attribute". Substring
 * matching would report the vocabulary fully documented against help text that names none of
 * it. The boundaries are the only thing making the check mean what it says.
 */
function mentionsToken(text: string, name: string): boolean {
  return new RegExp(`(^|[^A-Za-z0-9_])${name}([^A-Za-z0-9_]|$)`).test(text);
}

// The one field description that ENUMERATES the vocabulary, in each locale. The canvas's own
// CEL help is deliberately not gated here: it describes the box without listing identifiers,
// so it has nothing to fall out of step with. That is a gap, stated rather than papered over.
const HELP: Record<string, string> = Object.fromEntries(
  Object.entries(LOCALE_FILES).map(([path, mod]) => [
    path.split('/').slice(-2)[0],
    mod.default.ruleCelDescription,
  ]),
);

describe('the predicate environment extraction', () => {
  // 🔴 THE NEGATIVE CONTROL FOR EVERY ASSERTION BELOW. All of them are "each registered name
  // is mentioned"; over an empty set that is vacuously true, and a broken regex is exactly
  // how the set becomes empty.
  it('reads the whole predicate package, not a hand-listed pair of files', () => {
    // The control for the glob. A pattern that matched nothing would leave PREDICATE_SRC empty
    // and make every "is it documented" check below vacuously true.
    const names = Object.keys(PREDICATE_FILES).map((f) => f.split('/').pop());
    expect(names.length).toBeGreaterThan(2);
    expect(names).toContain('env.go');
    expect(names).toContain('geo.go');
    expect(PREDICATE_SRC.length).toBeGreaterThan(5000);
  });

  it('finds a plausible environment', () => {
    expect(registered('Variable').length).toBeGreaterThanOrEqual(6);
    expect(registered('Variable')).toContain('geo');
    expect(registered('Variable')).toContain('m');
    expect(registered('Function')).toEqual(['inFence']);
  });

  it('matches identifiers on token boundaries, not as substrings', () => {
    // The control for the control. If this ever passed, every check below would be reporting
    // on the presence of ordinary prose.
    expect(mentionsToken('a measurement over time', 'm')).toBe(false);
    expect(mentionsToken('las geocercas del sitio', 'geo')).toBe(false);
    expect(mentionsToken('the vocabulary: m, attr, geo.', 'm')).toBe(true);
    expect(mentionsToken('written geo.inFence("yard")', 'geo')).toBe(true);
  });

  // 🔴 THE REFUSAL IS NOW ACTUALLY EXERCISED. This test used to assert that `Map.get` does
  // not throw — which cannot fail — and then re-check two const values pinned three lines
  // above. The branch it claimed to cover, `registered` throwing on an identifier it cannot
  // resolve, was unreachable from a test, because the function read module state instead of
  // taking a source. A control that documents a guarantee without checking it is the same
  // shape as a comment asserting an invariant nothing enforces.
  it('refuses a registration whose identifier it cannot resolve', () => {
    const src = 'cel.Variable(VarSomethingNew, cel.StringType),';
    expect(() => registered('Variable', src, new Map())).toThrow(/VarSomethingNew/);
  });

  it('reads a bare string literal registration — the counterweight', () => {
    // A function registered inline, with no const at all, must still be SEEN. Refusing
    // everything unresolvable would be easy and would also refuse this.
    expect(registered('Function', 'cel.Function("distanceTo", …)', new Map())).toEqual(['distanceTo']);
  });
});

// The control for the glob: a pattern matching nothing would make describe.each iterate an
// empty list, running zero assertions and reporting a green file.
describe('the locale set', () => {
  it('found every locale the console ships', () => {
    expect(Object.keys(HELP).sort()).toEqual(['en', 'es']);
  });
});

describe.each(Object.keys(HELP))('the CEL help in %s', (locale) => {
  const text = HELP[locale];

  it('exists and is substantial', () => {
    expect(typeof text).toBe('string');
    expect(text.length).toBeGreaterThan(40);
  });

  it('names every variable a condition may read', () => {
    const missing = registered('Variable').filter((v) => !mentionsToken(text, v));
    expect({ locale, missing }).toEqual({ locale, missing: [] });
  });

  it('names every function a condition may call', () => {
    const missing = registered('Function').filter((f) => !mentionsToken(text, f));
    expect({ locale, missing }).toEqual({ locale, missing: [] });
  });
});
