// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { DashboardDefinition } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { boardAddress, progressOf, stepIndex } from './rotation';

const board = (...slots: string[]) =>
  ({ slots: Object.fromEntries(slots.map((s) => [s, { type: 'device', label: s }])) }) as unknown as Pick<DashboardDefinition, 'slots'>;

describe('stepIndex', () => {
  it('wraps both ways', () => {
    expect(stepIndex(0, 1, 3)).toBe(1);
    expect(stepIndex(2, 1, 3)).toBe(0);
    expect(stepIndex(0, -1, 3)).toBe(2);
    expect(stepIndex(1, -1, 3)).toBe(0);
    expect(stepIndex(0, 1, 1)).toBe(0);
  });
});

describe('boardAddress', () => {
  const link = '?rotate=a,b&every=30&theme=dark&bind.pump=pump-7&bind.fan=fan-2&range=6h';

  it('keeps a bind on the boards that have the slot, and drops it where the board has none', () => {
    expect(boardAddress(link, board('pump'))).toBe('?rotate=a%2Cb&every=30&theme=dark&bind.pump=pump-7&range=6h');
    expect(boardAddress(link, board('fan', 'pump'))).toContain('bind.fan=fan-2');
    const bare = boardAddress(link, board());
    expect(bare).not.toContain('bind.');
    expect(bare).toContain('range=6h');
  });

  it('never matches a slot by inheritance', () => {
    expect(boardAddress('?bind.constructor=x&bind.toString=y', board())).toBe('');
  });

  it('is empty for no link, and for a board with no slots at all', () => {
    expect(boardAddress('', board('pump'))).toBe('');
    expect(boardAddress('?bind.pump=p', {} as Pick<DashboardDefinition, 'slots'>)).toBe('');
  });
});

describe('progressOf', () => {
  it('is the share of the interval gone, held to 0..1', () => {
    expect(progressOf(0, 60)).toBe(0);
    expect(progressOf(30_000, 60)).toBe(0.5);
    expect(progressOf(60_000, 60)).toBe(1);
    expect(progressOf(90_000, 60)).toBe(1);
    expect(progressOf(-5, 60)).toBe(0);
  });
});
