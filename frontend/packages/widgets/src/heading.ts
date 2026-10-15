// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The compass's pure helpers: rounding a heading before it is folded onto 0..360, and naming the
// eight-point direction it is nearest. React-free, so the rules are asserted by value.

import { wrapHeading } from '@devicechain/widget-sdk/echarts';

import type { LevelStrings } from './level-strings';

// The heading a compass shows: rounded to `precision` places FIRST, then folded onto [0, 360).
// In that order so 359.6 at whole degrees is 0, not a display of 360. A value that is not a
// finite number has no heading (undefined).
export function shownHeading(raw: number | undefined, precision: number): number | undefined {
  if (raw === undefined || !Number.isFinite(raw)) return undefined;
  const places = Math.min(100, Math.max(0, Math.trunc(precision)));
  return wrapHeading(Number(raw.toFixed(places)));
}

const EIGHT_POINT = ['N', 'NE', 'E', 'SE', 'S', 'SW', 'W', 'NW'] as const;
const LETTER_INDEX: Record<string, 0 | 1 | 2 | 3> = { N: 0, E: 1, S: 2, W: 3 };

// The nearest of the eight points (N, NE, E, ...), written with the locale's letters.
export function cardinalOf(heading: number, letters: LevelStrings['cardinals']): string {
  const point = EIGHT_POINT[Math.round(heading / 45) % 8];
  return [...point].map((c) => letters[LETTER_INDEX[c]]).join('');
}
