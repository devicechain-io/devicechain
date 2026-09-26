// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import { allowsSave, canonicalDurations, graphMatches, judgeSynthesized, openStoredRule, type SeedCandidates } from './fidelity';
import type { CanvasDefinition } from './model';

const rule = (extra: Record<string, unknown> = {}) =>
  JSON.stringify({ name: 'hot', type: 'threshold', severity: 'major', when: { metric: 'tempC', op: 'gt', threshold: 30 }, ...extra });

describe('canonicalDurations', () => {
  it('maps every spelling of one duration to the same milliseconds', () => {
    expect(JSON.parse(canonicalDurations(rule({ hold: '10m' }))).hold).toBe(600000);
    expect(JSON.parse(canonicalDurations(rule({ hold: '10m0s' }))).hold).toBe(600000);
    expect(JSON.parse(canonicalDurations(rule({ window: '8.001s' }))).window).toBe(8001); // float noise tolerated
  });

  it('deletes a zero duration, which Go omits', () => {
    expect('timeout' in JSON.parse(canonicalDurations(rule({ timeout: '0s' })))).toBe(false);
  });

  it('keeps a duration the canvas would have to round as its string', () => {
    expect(JSON.parse(canonicalDurations(rule({ gap: '1500us' }))).gap).toBe('1500us');
  });

  it('leaves other keys and unparseable input alone', () => {
    expect(JSON.parse(canonicalDurations(rule({ hold: '1m' }))).when).toEqual({ metric: 'tempC', op: 'gt', threshold: 30 });
    expect(canonicalDurations('{not json')).toBe('{not json');
    expect(canonicalDurations('[1]')).toBe('[1]');
  });
});

describe('judgeSynthesized', () => {
  const ok = (definition: string) => ({ ok: true, definition });

  it('calls a seed that does not compile uncompilable', () => {
    expect(judgeSynthesized(rule(), { ok: false, definition: null }).kind).toBe('uncompilable');
  });

  it('calls a dropped field lossy', () => {
    const stored = rule({ actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKeyTemplate: '"z-" + series' } }] });
    const compiled = rule({ actions: [{ type: 'raiseAlarm', raiseAlarm: {} }] });
    expect(judgeSynthesized(stored, ok(compiled)).kind).toBe('lossy');
  });

  it('calls a dropped action lossy', () => {
    const stored = rule({ actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'a' } }, { type: 'future' }] });
    const compiled = rule({ actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'a' } }] });
    expect(judgeSynthesized(stored, ok(compiled)).kind).toBe('lossy');
  });

  it('calls a rounded sub-millisecond duration lossy', () => {
    expect(judgeSynthesized(rule({ hold: '1500us' }), ok(rule({ hold: '2ms' }))).kind).toBe('lossy');
  });

  it('is faithful over keys the compiler adds and durations it respells', () => {
    const stored = JSON.stringify({ type: 'duration', when: { metric: 'tempC', op: 'gt', threshold: 30 }, hold: '10m', window: '0s' });
    const compiled = JSON.stringify({ name: '', type: 'duration', when: { metric: 'tempC', op: 'gt', threshold: 30 }, hold: '10m0s' });
    expect(judgeSynthesized(stored, ok(compiled)).kind).toBe('faithful');
  });
});

describe('graphMatches', () => {
  const ok = (definition: string) => ({ ok: true, definition });

  it('holds when the graph compiles to the stored rule', () => {
    expect(graphMatches(rule(), ok(rule()))).toBe(true);
  });

  // Both directions: a stale graph can carry a field the definition has since DROPPED.
  it('fails when the graph carries a field the definition no longer has', () => {
    expect(graphMatches(rule(), ok(rule({ description: 'stale' })))).toBe(false);
  });

  it('fails when the definition carries a field the graph does not', () => {
    expect(graphMatches(rule({ description: 'new' }), ok(rule()))).toBe(false);
  });

  it('fails when the graph does not compile', () => {
    expect(graphMatches(rule(), { ok: false, definition: null })).toBe(false);
  });
});

describe('allowsSave', () => {
  it('permits exactly the verdicts that cannot rewrite a rule unseen', () => {
    const kinds = ['new', 'faithful', 'relaid', 'uncompilableGraph', 'unrepresentable', 'lossy', 'staleLayout', 'uncompilable', 'unverified'] as const;
    const allowed = kinds.filter((kind) => allowsSave(kind === 'unrepresentable' ? { kind, reason: 'r' } : { kind }));
    expect(allowed).toEqual(['new', 'faithful', 'relaid', 'uncompilableGraph']);
  });
});

describe('openStoredRule', () => {
  const g = (tag: string): CanvasDefinition => ({ schemaVersion: 1, nodes: [{ id: tag, type: 'source', config: {} }], edges: [] });
  const blank = g('blank');
  const graph = g('graph');
  const synthesized = g('synth');
  // A compile that answers per graph, as the real one does.
  const compiler = (answers: Record<string, { ok: boolean; definition: string | null } | Error>) =>
    vi.fn(async (def: CanvasDefinition) => {
      const a = answers[def.nodes[0].id];
      if (a instanceof Error) throw a;
      return a;
    });
  const cands = (c: Partial<SeedCandidates>): SeedCandidates => ({ stored: rule(), graph: null, synthesized: null, synthesisError: null, ...c });

  it('refuses without compiling when nothing can be laid out', async () => {
    const compile = compiler({});
    const o = await openStoredRule(cands({ synthesisError: 'no node for it' }), blank, compile);
    expect(o).toEqual({ graph: blank, fidelity: { kind: 'unrepresentable', reason: 'no node for it' }, compiled: null });
    expect(compile).not.toHaveBeenCalled();
  });

  it('opens a matching stored graph', async () => {
    const o = await openStoredRule(cands({ graph, synthesized }), blank, compiler({ graph: { ok: true, definition: rule() } }));
    expect({ graph: o.graph, kind: o.fidelity.kind }).toEqual({ graph, kind: 'faithful' });
  });

  it('re-lays a stale graph from the definition when that is faithful', async () => {
    const compile = compiler({ graph: { ok: true, definition: rule({ description: 'stale' }) }, synth: { ok: true, definition: rule() } });
    const o = await openStoredRule(cands({ graph, synthesized }), blank, compile);
    expect({ graph: o.graph, kind: o.fidelity.kind, compiled: o.compiled }).toEqual({
      graph: synthesized,
      kind: 'relaid',
      compiled: { ok: true, definition: rule() },
    });
  });

  it('refuses a stale graph when the definition cannot be laid out in full', async () => {
    const compile = compiler({ graph: { ok: true, definition: rule({ description: 'stale' }) }, synth: { ok: true, definition: rule({ severity: 'minor' }) } });
    const o = await openStoredRule(cands({ graph, synthesized }), blank, compile);
    expect({ graph: o.graph, kind: o.fidelity.kind }).toEqual({ graph, kind: 'staleLayout' });
  });

  it('opens a stored graph that does not compile, for repair on the canvas', async () => {
    const o = await openStoredRule(cands({ graph, synthesized }), blank, compiler({ graph: { ok: false, definition: null } }));
    expect({ graph: o.graph, kind: o.fidelity.kind }).toEqual({ graph, kind: 'uncompilableGraph' });
  });

  it('judges a synthesized seed one-directionally', async () => {
    const o = await openStoredRule(cands({ synthesized }), blank, compiler({ synth: { ok: true, definition: rule({ name: 'hot', extra: undefined }) } }));
    expect(o.fidelity.kind).toBe('faithful');
    const lossy = await openStoredRule(cands({ stored: rule({ futureKnob: 1 }), synthesized }), blank, compiler({ synth: { ok: true, definition: rule() } }));
    expect(lossy.fidelity.kind).toBe('lossy');
  });

  it('fails closed when the compiler cannot be reached', async () => {
    const o = await openStoredRule(cands({ synthesized }), blank, compiler({ synth: new Error('down') }));
    expect({ kind: o.fidelity.kind, compiled: o.compiled }).toEqual({ kind: 'unverified', compiled: null });
  });
});
