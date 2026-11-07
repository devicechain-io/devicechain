// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The status grid's state, decided apart from the drawing: one tile per device of the fleet for ONE
// measurement, each tile placed in a threshold band, and the tally of devices per band. Pure, so each
// rule is a test.

import { bandAt, type SeverityLevel, type Threshold } from '@devicechain/widget-sdk';

import { cellFor, deviceLabel, type Cell } from './fleet-model';
import type { FleetMember } from '@devicechain/dashboards';

// The most tiles drawn. A grid is read at a glance; past this the fleet table is the tool, and the
// grid says how many devices it left out rather than pass a subset off as the fleet.
export const MAX_TILES = 400;

export interface Tile {
  token: string;
  label: string;
  cell: Cell;
  // The band the reading is in (undefined: no reading, or below the lowest threshold).
  band: Threshold | undefined;
  // What the tile counts as in the tally: a severity level, 'ok' for a reading outside every
  // alarming band, or 'none' for a device with no number (never counted as ok).
  group: SeverityLevel | 'ok' | 'none';
}

// Worst first: the order a tile is sorted in when the author asks for the worst devices on top.
const SEVERITY_RANK: Record<string, number> = { critical: 0, major: 1, minor: 2, warning: 3, info: 4, normal: 5, ok: 5, none: 6 };

export function groupOf(cell: Cell, band: Threshold | undefined): Tile['group'] {
  if (cell.kind !== 'value') return 'none';
  if (band === undefined || band.level === 'normal') return 'ok';
  return band.level;
}

export function buildTiles(members: readonly FleetMember[], measurement: string, bands: readonly Threshold[], worstFirst: boolean): Tile[] {
  const tiles = members.map((m): Tile => {
    const cell = cellFor(m, measurement);
    const band = cell.kind === 'value' ? bandAt(bands, cell.value) : undefined;
    return { token: m.token, label: deviceLabel(m), cell, band, group: groupOf(cell, band) };
  });
  if (!worstFirst) return tiles;
  return tiles
    .map((t, i) => ({ t, i }))
    .sort((a, b) => (SEVERITY_RANK[a.t.group] ?? 6) - (SEVERITY_RANK[b.t.group] ?? 6) || a.i - b.i)
    .map((x) => x.t);
}

// Devices per group, in the order the legend lists them (worst first, 'none' last), zero rows dropped.
export function tally(tiles: readonly Tile[]): { group: Tile['group']; count: number; label?: string }[] {
  const counts = new Map<Tile['group'], number>();
  for (const t of tiles) counts.set(t.group, (counts.get(t.group) ?? 0) + 1);
  return [...counts.entries()]
    .map(([group, count]) => ({ group, count }))
    .sort((a, b) => (SEVERITY_RANK[a.group] ?? 6) - (SEVERITY_RANK[b.group] ?? 6));
}

// The side of a square tile for `count` tiles in a body of width x height: the largest side at which
// they all fit, held between a readable minimum and a sane maximum. Rows then scroll if the minimum
// forces overflow.
export function tileSide(count: number, width: number, height: number, min: number, max: number, gap: number): number {
  if (count <= 0 || width <= 0 || height <= 0) return min;
  let best = min;
  for (let cols = 1; cols <= count; cols++) {
    const fit = Math.floor((width - gap * (cols - 1)) / cols);
    if (fit < min) break;
    // The ceiling is applied BEFORE the fit test: a tile never drawn above it must be judged at the
    // size it would be drawn.
    const side = Math.min(fit, max);
    const rows = Math.ceil(count / cols);
    if (rows * side + gap * (rows - 1) <= height) best = Math.max(best, side);
  }
  return Math.min(best, max);
}
