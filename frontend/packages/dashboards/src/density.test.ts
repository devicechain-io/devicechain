// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition, serializeDefinition } from './definition';
import { setCanvasDensity } from './editor-model';
import { sameLabel } from './same-label';
import { DENSITY_SCALE, isCanvasDensity } from './types';

describe('canvas.density', () => {
  it('is absent by default, so an old board is byte-identical after a round trip', () => {
    const def = parseDashboardDefinition({ widgets: [] });
    expect(def.canvas.density).toBeUndefined();
    expect(JSON.parse(serializeDefinition(def)).canvas).not.toHaveProperty('density');
  });

  it('parses each density and writes it back', () => {
    for (const density of ['compact', 'comfortable', 'wall'] as const) {
      const def = parseDashboardDefinition({ widgets: [], canvas: { density } });
      expect(def.canvas.density).toBe(density);
      expect(JSON.parse(serializeDefinition(def)).canvas.density).toBe(density);
    }
  });

  it('falls back to the default for a value this build does not know', () => {
    expect(parseDashboardDefinition({ widgets: [], canvas: { density: 'enormous' } }).canvas.density).toBeUndefined();
    expect(parseDashboardDefinition({ widgets: [], canvas: { density: 3 } }).canvas.density).toBeUndefined();
  });

  it('scales 0.9 / 1 / 1.5', () => {
    expect(DENSITY_SCALE).toEqual({ compact: 0.9, comfortable: 1, wall: 1.5 });
    expect(isCanvasDensity('wall')).toBe(true);
    expect(isCanvasDensity('huge')).toBe(false);
  });

  it('setCanvasDensity stores a non-default and removes the key for comfortable', () => {
    const base = parseDashboardDefinition({ widgets: [] });
    const wall = setCanvasDensity(base, 'wall');
    expect(wall.canvas.density).toBe('wall');
    const back = setCanvasDensity(wall, 'comfortable');
    expect(back.canvas).not.toHaveProperty('density');
    expect(serializeDefinition(back)).toBe(serializeDefinition(base));
  });
});

describe('sameLabel', () => {
  it('ignores case and surrounding whitespace', () => {
    expect(sameLabel(' SP-Fuel-01 ', 'sp-fuel-01')).toBe(true);
  });

  it('is false for different text and never matches an empty or missing string', () => {
    expect(sameLabel('Fuel', 'Fuel level')).toBe(false);
    expect(sameLabel('', '')).toBe(false);
    expect(sameLabel(undefined, 'x')).toBe(false);
    expect(sameLabel('  ', '  ')).toBe(false);
  });
});
