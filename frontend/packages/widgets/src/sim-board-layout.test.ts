// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Lints the committed sim boards against the REAL built-in descriptors: the span limits are
// the ones each widget declares in its `layout` block. This lives here, not in
// packages/dashboards, because `dashboards` must not depend on `widgets`.
//
// This test reads fixtures off disk, so it needs the Node typings (see the note in
// options.test.ts: the reference is program-wide, not file-scoped).
/// <reference types="node" />

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  lintLayout,
  parseDashboardDefinition,
  spanLimitsOfLayout,
  type DashboardDefinition,
  type LayoutFinding,
  type LayoutLintOptions,
  type PixelSize,
  type WidgetInstance,
} from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { builtinWidgetDescriptors } from './builtin-descriptors';

// ---- TEMPORARY pixel floor -----------------------------------------------------
//
// Deleted when constant-size square cells make a span in cells a readable size by
// construction. Until then it is the rendered size below which a widget is not legible, on
// today's grid: 24 columns, 40 px rows, 8 px gutters (at 960 wide a column is ~32 px; at 1920
// ~72 px). Deliberately CONSERVATIVE: a false error on a good board teaches people to ignore
// the lint.
const PIXEL_FLOOR: Record<string, PixelSize> = {
  'latest-card': { width: 140, height: 112 },
  'alarm-count': { width: 120, height: 112 },
  gauge: { width: 140, height: 120 },
  'timeseries-chart': { width: 240, height: 160 },
  table: { width: 200, height: 120 },
  'alarm-table': { width: 240, height: 120 },
  label: { width: 64, height: 32 },
  image: { width: 64, height: 64 },
  'command-button': { width: 160, height: 100 },
  'entity-selector': { width: 140, height: 80 },
  map: { width: 200, height: 160 },
};

const WIDTHS = [960, 1920];

function options(columns: number): LayoutLintOptions {
  return {
    columns,
    spanLimitsOf: (w: WidgetInstance) =>
      spanLimitsOfLayout(builtinWidgetDescriptors[w.type as keyof typeof builtinWidgetDescriptors]?.layout),
    pixelFloor: { widths: WIDTHS, minPxOf: (w) => PIXEL_FLOOR[w.type] },
  };
}

const FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'testdata', 'sim-dashboards');

// Every fixture on disk, so a board a scenario adds is linted the moment its fixture lands:
// the Go guards named sitepulse and widgetlab and never saw buildingpulse.
function boards(): string[] {
  return readdirSync(FIXTURES)
    .filter((n) => n.endsWith('.json'))
    .map((n) => n.slice(0, -'.json'.length))
    .sort();
}

function load(token: string): DashboardDefinition {
  return parseDashboardDefinition(JSON.parse(readFileSync(join(FIXTURES, `${token}.json`), 'utf8')));
}

// Known failures: `board|widget|code[|breakpoint][|width]`. A RATCHET, not a waiver. The test fails on a finding
// that is not listed (a regression) AND on a listed one that no longer occurs (the fix
// landed, so the entry must go). Empty: every committed board passes the lint.
const KNOWN_FAILURES: ReadonlySet<string> = new Set<string>([]);

// The breakpoint and the width are part of the key, so a finding that survives at one width
// (or at one breakpoint's box) after a partial fix cannot hide behind an entry another width
// or box still earns.
const key = (board: string, f: LayoutFinding) =>
  [board, f.widgetId, f.code, f.breakpoint, f.width].filter((p) => p !== undefined).join('|');

describe('the committed sim boards', () => {
  it('include buildingpulse and widgetlab, not only sitepulse', () => {
    expect(boards()).toEqual(['bp-dashboard', 'sp-dashboard', 'wl-gallery', 'wl-stress']);
  });

  it('use only widget types the built-in descriptors and the pixel floor both describe', () => {
    // A type missing from either would lint as an info finding (or skip the floor) and pass:
    // both must be complete or the lint silently covers less than it appears to.
    for (const token of boards()) {
      for (const w of load(token).widgets) {
        expect(builtinWidgetDescriptors, `${token}: ${w.id} is a ${w.type}`).toHaveProperty(w.type);
        expect(PIXEL_FLOOR, `${token}: ${w.id} is a ${w.type}, which the pixel floor does not describe`).toHaveProperty(w.type);
      }
    }
  });

  it('are judged by limits that agree with the floor: every minimum span clears its own pixel floor', () => {
    // A minSpan that renders below its own minPx makes the smallest LEGAL box an error, so
    // the two numbers would contradict each other on every board that uses it.
    for (const [type, descriptor] of Object.entries(builtinWidgetDescriptors)) {
      const min = descriptor.layout?.minSpan ?? { cols: 1, rows: 1 };
      const probe = parseDashboardDefinition({
        schemaVersion: 1,
        canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
        widgets: [{ id: 'probe', type, layout: { base: { col: 0, colSpan: min.cols, row: 0, rowSpan: min.rows, z: 0 } } }],
      });
      expect(lintLayout(probe, options(24)), type).toEqual([]);
    }
  });

  it('have exactly the known layout findings, no more and no fewer', () => {
    const seen = new Set<string>();
    const detail = new Map<string, string>();
    for (const token of boards()) {
      const def = load(token);
      for (const f of lintLayout(def, options(def.canvas.grid.columns))) {
        seen.add(key(token, f));
        detail.set(key(token, f), f.message);
      }
    }
    const fresh = [...seen].filter((k) => !KNOWN_FAILURES.has(k)).map((k) => `${k}: ${detail.get(k)}`);
    const stale = [...KNOWN_FAILURES].filter((k) => !seen.has(k));
    expect(fresh, 'new layout findings: fix the board, do not allowlist them').toEqual([]);
    expect(stale, 'allowlisted findings that no longer occur: delete these entries').toEqual([]);
  });
});
