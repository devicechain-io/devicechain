// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The canvas's open-time fidelity check: before a STORED rule may be saved from the canvas, would
// saving it keep everything the stored definition says?
//
// 🔴 THE CANVAS USED TO ANSWER THIS BY NOT ASKING. A rule of a type it had no node for opened as
// a lone Source (the "cannot be shown" reason was computed and discarded); a rule with a field or
// action the canvas does not model opened without it; a rule whose definition was changed through
// the API opened from its stale saved graph. Each compiled, and each canvas Save wrote the reduced
// rule back over the real one. Nothing said so.
//
// Only the server can say what a canvas graph lowers to (compileCanvas is authoritative), so the
// check compiles the graph the editor is about to open with and compares the result against the
// stored definition. It is a pure module — no React — so it is tested without rendering; the
// editor (CanvasEditor.tsx) supplies the compile call.

import { ruleSurvivesRoundTrip } from '../rule-equal';
import type { CanvasDefinition } from './model';
import { goDurationMs } from './roundtrip';

export type Fidelity =
  // A rule being created: there is nothing stored to lose.
  | { kind: 'new' }
  // The seed compiles to the stored rule. Save is gated as normal.
  | { kind: 'faithful' }
  // The saved graph no longer matched the definition (it was changed outside the canvas), and the
  // canvas laid the CURRENT definition out again instead, faithfully. Save is allowed; a note says
  // the layout was rebuilt.
  | { kind: 'relaid' }
  // The stored canvas graph does not compile as it stands. The whole authored graph is on screen,
  // so nothing is hidden, and the canvas is the only surface that can author every node in it — so
  // Save is allowed (through the normal "compiles" gate) with a note that it replaces the rule.
  | { kind: 'uncompilableGraph' }
  // The definition has a shape the canvas cannot lay out at all (a type with no node, not JSON).
  | { kind: 'unrepresentable'; reason: string }
  // The laid-out rule compiles, but to less than the stored definition says.
  | { kind: 'lossy' }
  // The saved graph is stale AND the current definition cannot be laid out in full: saving would
  // revert the outside change.
  | { kind: 'staleLayout' }
  // A synthesized seed does not compile, so nothing can confirm it represents the stored rule.
  | { kind: 'uncompilable' }
  // The check could not reach the compiler. Fail closed, with a retry.
  | { kind: 'unverified' };

// allowsSave is the one place that says which verdicts let an EXISTING rule be saved from the
// canvas. Every other verdict means a save would be a rewrite nobody verified.
export function allowsSave(f: Fidelity): boolean {
  return f.kind === 'new' || f.kind === 'faithful' || f.kind === 'relaid' || f.kind === 'uncompilableGraph';
}

// The rules.Rule fields that are rules.Duration: the server re-marshals them canonically
// ("10m" comes back "10m0s"; a zero one is omitted), so they are compared by value. They are
// all top-level (rules/schema.go).
const DURATION_KEYS = ['window', 'hold', 'timeout', 'gap'] as const;

// canonicalDurations rewrites each top-level duration key of a definition to its exact integer
// millisecond count. A zero duration is DELETED, because Go's omitempty elides a zero Duration,
// so "0s" and absent decode to the same rule. A duration that is not a whole number of
// milliseconds (beyond float noise), or does not parse, is left as its string, so the comparison
// reports it as a difference: the canvas works in whole milliseconds, so such a rule really would
// change. Unparseable JSON, or JSON that is not an object, is returned unchanged (and
// ruleSurvivesRoundTrip then reports it as it would have).
export function canonicalDurations(definition: string): string {
  let v: unknown;
  try {
    v = JSON.parse(definition);
  } catch {
    return definition;
  }
  if (v === null || typeof v !== 'object' || Array.isArray(v)) return definition;
  const rule = { ...(v as Record<string, unknown>) };
  for (const k of DURATION_KEYS) {
    const raw = rule[k];
    if (typeof raw !== 'string') continue;
    const ms = goDurationMs(raw);
    if (ms === null) continue;
    const whole = Math.round(ms);
    if (Math.abs(ms - whole) > 1e-6) continue;
    if (whole === 0) delete rule[k];
    else rule[k] = whole;
  }
  return JSON.stringify(rule);
}

interface Compiled {
  ok: boolean;
  definition?: string | null;
}

// holds reports whether `compiled` carries everything `stored` says (one direction).
const holds = (stored: string, compiled: string): boolean => ruleSurvivesRoundTrip(canonicalDurations(stored), canonicalDurations(compiled));

// judgeSynthesized judges a seed the canvas SYNTHESIZED from the stored definition. One
// direction is right here: the compiler always writes keys an API-authored rule may omit
// (`name`, `"when":{}`), and emitting them loses nothing.
export function judgeSynthesized(stored: string, compiled: Compiled): Fidelity {
  if (!compiled.ok || !compiled.definition) return { kind: 'uncompilable' };
  return holds(stored, compiled.definition) ? { kind: 'faithful' } : { kind: 'lossy' };
}

// graphMatches reports whether a STORED graph still describes the stored definition. It must
// hold in BOTH directions: the graph can be stale, and a field REMOVED from the definition since
// the graph was saved is exactly as much a revert as a field changed. The two sides cannot differ
// in the keys the compiler always writes, because a canvas-authored definition IS a compile of
// its graph.
export function graphMatches(stored: string, compiled: Compiled): boolean {
  return !!compiled.ok && !!compiled.definition && holds(stored, compiled.definition) && holds(compiled.definition, stored);
}

// The candidate seeds, resolved from the stored rule before anything is compiled.
export interface SeedCandidates {
  // The stored definition, verbatim.
  stored: string;
  // The stored authoring graph, when the rule has a well-formed one.
  graph: CanvasDefinition | null;
  // The graph synthesized from the definition, or null with the reason it could not be.
  synthesized: CanvasDefinition | null;
  synthesisError: string | null;
}

// What the editor opens with: the graph it mounts, the verdict, and the compile of that graph
// when the check made one (so the editor need not compile the same graph again).
export interface Opened<R extends Compiled = Compiled> {
  graph: CanvasDefinition;
  fidelity: Fidelity;
  compiled: R | null;
}

// openWithoutCompile answers the cases that need no compiler: a definition the canvas cannot lay
// out at all. It returns null when a compile is needed.
export function openWithoutCompile(c: SeedCandidates, blank: CanvasDefinition): Opened<never> | null {
  if (c.graph || c.synthesized) return null;
  return { graph: blank, fidelity: { kind: 'unrepresentable', reason: c.synthesisError ?? '' }, compiled: null };
}

// openStoredRule decides what the canvas opens a stored rule with. `compile` is the server
// compile; a rejection (transport, auth, timeout) yields `unverified`. It never rejects itself.
export async function openStoredRule<R extends Compiled>(
  c: SeedCandidates,
  blank: CanvasDefinition,
  compile: (graph: CanvasDefinition) => Promise<R>,
): Promise<Opened<R>> {
  const immediate = openWithoutCompile(c, blank);
  if (immediate) return immediate;
  try {
    if (c.graph) {
      const res = await compile(c.graph);
      if (graphMatches(c.stored, res)) return { graph: c.graph, fidelity: { kind: 'faithful' }, compiled: res };
      if (!res.ok) return { graph: c.graph, fidelity: { kind: 'uncompilableGraph' }, compiled: res };
      // The graph compiles, but not to the stored rule: it is stale. Lay the CURRENT definition
      // out instead, and let that be saved only if it is faithful in its own right.
      if (c.synthesized) {
        const again = await compile(c.synthesized);
        if (judgeSynthesized(c.stored, again).kind === 'faithful') {
          return { graph: c.synthesized, fidelity: { kind: 'relaid' }, compiled: again };
        }
      }
      return { graph: c.graph, fidelity: { kind: 'staleLayout' }, compiled: res };
    }
    const synthesized = c.synthesized as CanvasDefinition; // openWithoutCompile returned null
    const res = await compile(synthesized);
    return { graph: synthesized, fidelity: judgeSynthesized(c.stored, res), compiled: res };
  } catch {
    return { graph: c.graph ?? (c.synthesized as CanvasDefinition), fidelity: { kind: 'unverified' }, compiled: null };
  }
}
