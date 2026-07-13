// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { graphFromDefinition, parseGoDuration } from './roundtrip';
import type { CanvasNode } from './model';

const P = 'thermostat';

// Find a node by id in a synthesis result (asserting it exists).
function nodeById(nodes: CanvasNode[], id: string): CanvasNode {
  const n = nodes.find((x) => x.id === id);
  if (!n) throw new Error(`node ${id} not found`);
  return n;
}

describe('parseGoDuration', () => {
  it('parses the units Go emits', () => {
    expect(parseGoDuration('10m0s')).toBe(600000);
    expect(parseGoDuration('600ms')).toBe(600);
    expect(parseGoDuration('1h30m')).toBe(5_400_000);
    expect(parseGoDuration('5s')).toBe(5000);
    expect(parseGoDuration('0s')).toBe(0);
    expect(parseGoDuration('')).toBe(0);
    expect(parseGoDuration('2h0m0s')).toBe(7_200_000);
    expect(parseGoDuration('1.5s')).toBe(1500);
  });
  it('returns whole milliseconds despite float accumulation', () => {
    expect(parseGoDuration('8.001s')).toBe(8001); // not 8000.999999999999
    expect(parseGoDuration('1m30.918273645s')).toBe(90918); // sub-ms rounds to nearest ms
    expect(Number.isInteger(parseGoDuration('2.5m')!)).toBe(true);
  });
  it('rejects malformed durations', () => {
    expect(parseGoDuration('abc')).toBeNull();
    expect(parseGoDuration('10x')).toBeNull();
    expect(parseGoDuration('1h30')).toBeNull(); // trailing junk (no unit)
    expect(parseGoDuration('m5')).toBeNull();
  });
});

describe('graphFromDefinition', () => {
  it('synthesizes a source→threshold→raiseAlarm graph', () => {
    const def = JSON.stringify({
      name: 'hot',
      type: 'threshold',
      severity: 'critical',
      when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'overheat' } }],
    });
    const { graph, error } = graphFromDefinition(def, P);
    expect(error).toBeUndefined();
    expect(graph).not.toBeNull();
    const g = graph!;
    expect(g.schemaVersion).toBe(1);
    expect(g.nodes).toHaveLength(3);

    const source = nodeById(g.nodes, 'source');
    expect(source.type).toBe('source');
    expect(source.config).toEqual({ scope: { kind: 'profile', profileToken: P } });

    const cond = nodeById(g.nodes, 'condition');
    expect(cond.type).toBe('threshold');
    expect(cond.config).toEqual({
      name: 'hot',
      severity: 'critical',
      when: { metric: 'tempC', op: 'gt', threshold: { kind: 'literal', value: 30 } },
    });

    const action = nodeById(g.nodes, 'action-0');
    expect(action.type).toBe('action');
    expect(action.config).toEqual({ action: 'raiseAlarm', alarmKey: 'overheat' });

    expect(g.edges).toEqual([
      { from: 'source:out', to: 'condition:in' },
      { from: 'condition:signal', to: 'action-0:in' },
    ]);
  });

  it('maps a dynamic-attribute bound', () => {
    const def = JSON.stringify({
      name: 'over-limit',
      type: 'threshold',
      when: { metric: 'tempC', op: 'gt', thresholdAttr: 'tempLimit' },
    });
    const { graph } = graphFromDefinition(def, P);
    const cond = nodeById(graph!.nodes, 'condition');
    expect(cond.config).toEqual({
      name: 'over-limit',
      when: { metric: 'tempC', op: 'gt', threshold: { kind: 'attribute', attribute: 'tempLimit' } },
    });
  });

  it('parses a duration hold string to ms', () => {
    const def = JSON.stringify({
      name: 'sustained',
      type: 'duration',
      hold: '10m0s',
      when: { metric: 'tempC', op: 'ge', threshold: 30 },
    });
    const cond = nodeById(graphFromDefinition(def, P).graph!.nodes, 'condition');
    expect(cond.config).toMatchObject({ holdMs: 600000 });
  });

  it('synthesizes absence with no when leaf', () => {
    const def = JSON.stringify({ name: 'silent', type: 'absence', severity: 'major', timeout: '5m0s' });
    const cond = nodeById(graphFromDefinition(def, P).graph!.nodes, 'condition');
    expect(cond.config).toEqual({ name: 'silent', severity: 'major', timeoutMs: 300000 });
    expect('when' in cond.config).toBe(false);
  });

  it('synthesizes a tumbling aggregate', () => {
    const def = JSON.stringify({
      name: 'avg-hot',
      type: 'aggregate',
      agg: 'avg',
      windowMode: 'tumbling',
      metric: 'tempC',
      window: '1m0s',
      op: 'gt',
      threshold: 25,
    });
    const cond = nodeById(graphFromDefinition(def, P).graph!.nodes, 'condition');
    expect(cond.config).toEqual({
      name: 'avg-hot',
      agg: 'avg',
      windowMode: 'tumbling',
      metric: 'tempC',
      windowMs: 60000,
      op: 'gt',
      threshold: 25,
    });
  });

  it('synthesizes correlation with no actions', () => {
    const def = JSON.stringify({ name: 'zone', type: 'correlation', anchorType: 'zone', count: 3, window: '1m0s' });
    const { graph } = graphFromDefinition(def, P);
    expect(graph!.nodes).toHaveLength(2); // source + condition, no actions
    const cond = nodeById(graph!.nodes, 'condition');
    expect(cond.config).toEqual({ name: 'zone', anchorType: 'zone', count: 3, windowMs: 60000 });
  });

  it('errors on an unknown rule type', () => {
    const { graph, error } = graphFromDefinition(JSON.stringify({ type: 'frobnicate' }), P);
    expect(graph).toBeNull();
    expect(error).toMatch(/cannot be shown/i);
  });

  it('errors on malformed JSON', () => {
    const { graph, error } = graphFromDefinition('{not json', P);
    expect(graph).toBeNull();
    expect(error).toMatch(/not valid JSON/i);
  });

  it('lays out multiple actions on separate rows', () => {
    const def = JSON.stringify({
      name: 'multi',
      type: 'threshold',
      severity: 'warning',
      when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [
        { type: 'raiseAlarm', raiseAlarm: {} },
        { type: 'sendCommand', sendCommand: { command: 'cool', payload: '{"level":2}' } },
      ],
    });
    const { graph } = graphFromDefinition(def, P);
    const g = graph!;
    expect(g.nodes.filter((n) => n.type === 'action')).toHaveLength(2);
    expect(nodeById(g.nodes, 'action-0').config).toEqual({ action: 'raiseAlarm' });
    expect(nodeById(g.nodes, 'action-1').config).toEqual({ action: 'sendCommand', command: 'cool', payload: '{"level":2}' });
    // Distinct rows.
    expect(nodeById(g.nodes, 'action-0').ui!.y).not.toBe(nodeById(g.nodes, 'action-1').ui!.y);
  });

  it('inserts a branch node between the condition and a guarded action (slice 9c)', () => {
    const def = JSON.stringify({
      name: 'severe-only',
      type: 'threshold',
      severity: 'critical',
      when: { metric: 'tempC', op: 'gt', threshold: 30 },
      actions: [
        { type: 'raiseAlarm', raiseAlarm: { alarmKey: 'overheat' }, guard: 'value > 100.0' },
        { type: 'sendCommand', sendCommand: { command: 'cool' } }, // unguarded → wired straight through
      ],
    });
    const { graph } = graphFromDefinition(def, P);
    const g = graph!;
    // The guarded action gets a branch carrying its guard, wired condition→branch→action.
    const branch = nodeById(g.nodes, 'branch-0');
    expect(branch.type).toBe('branch');
    expect(branch.config).toEqual({ when: 'value > 100.0' });
    expect(g.edges).toContainEqual({ from: 'condition:signal', to: 'branch-0:in' });
    expect(g.edges).toContainEqual({ from: 'branch-0:out', to: 'action-0:in' });
    // The unguarded action stays wired straight from the condition — no branch synthesized for it.
    expect(g.nodes.find((n) => n.id === 'branch-1')).toBeUndefined();
    expect(g.edges).toContainEqual({ from: 'condition:signal', to: 'action-1:in' });
  });
});
