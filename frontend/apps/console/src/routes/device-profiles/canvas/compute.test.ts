// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { NODE_CATALOG, defaultConfig, portTypeOf } from './model';
import { buildCanvasDefinition } from './roundtrip';

// The compute node (slice 9a-2) is the client twin of the Go graph catalog. These guard the mirror:
// a drift here would let the editor draw a wire the server-authoritative compiler then rejects.
describe('compute node model', () => {
  it('exposes only a value output (no input) — a compute reads its consumer env, not an edge', () => {
    const spec = NODE_CATALOG.compute;
    expect(spec.category).toBe('compute');
    expect(spec.out).toEqual({ value: 'value' });
    expect(spec.in).toEqual({});
  });

  it('gives every CEL-leaf condition and the branch a value input port', () => {
    for (const t of ['threshold', 'duration', 'aggregate', 'deltaRate', 'repeating', 'correlation', 'branch'] as const) {
      expect(NODE_CATALOG[t].in.value).toBe('value');
    }
  });

  it('does NOT give absence a value port — it has no leaf for a computed value to feed', () => {
    expect(NODE_CATALOG.absence.in.value).toBeUndefined();
  });

  it('seeds a fresh compute config with empty name + expr', () => {
    expect(defaultConfig('compute', 'thermostat')).toEqual({ name: '', expr: '' });
  });

  it('resolves value ports so the connect guard accepts value→value and rejects value→stream', () => {
    expect(portTypeOf('compute', 'value', true)).toBe('value');
    expect(portTypeOf('threshold', 'value', false)).toBe('value');
    expect(portTypeOf('branch', 'value', false)).toBe('value');
    // A same-typed wire (what isValidConnection requires) holds for value→value...
    expect(portTypeOf('compute', 'value', true)).toBe(portTypeOf('threshold', 'value', false));
    // ...and a value output does NOT match a stream input (the guard would reject it).
    expect(portTypeOf('compute', 'value', true)).not.toBe(portTypeOf('threshold', 'in', false));
  });

  it('preserves a compute node and its value edge through the editor round-trip', () => {
    const def = buildCanvasDefinition(
      [
        { id: 'src', type: 'source', config: { scope: { kind: 'profile', profileToken: 'thermostat' } } },
        { id: 'cmp', type: 'compute', config: { name: 'tempF', expr: 'm["tempC"] * 1.8 + 32.0' } },
        { id: 'c', type: 'threshold', config: { name: 'hot', when: { cel: 'tempF > 100.0' } } },
      ],
      [
        { from: 'src:out', to: 'c:in' },
        { from: 'cmp:value', to: 'c:value' },
      ],
    );
    expect(def.nodes.find((n) => n.type === 'compute')?.config).toEqual({ name: 'tempF', expr: 'm["tempC"] * 1.8 + 32.0' });
    expect(def.edges).toContainEqual({ from: 'cmp:value', to: 'c:value' });
  });
});
