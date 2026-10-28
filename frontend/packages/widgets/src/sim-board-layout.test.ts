// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Lints the committed sim boards against the REAL built-in descriptors: the span limits are
// the ones each widget declares in its `layout` block. This lives here, not in
// packages/dashboards, because `dashboards` must not depend on `widgets`.
//
// There is no pixel floor. Cells are square and never smaller than the grid's minCellSize (a
// narrower board reflows instead), so a widget inside its own declared limits is a readable size
// at every width the authored layout renders at; the limits are calibrated at that smallest cell
// (see builtin-descriptors.ts). Overlap is not checked here either: the parser refuses it, and
// load() below would throw.
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
  type WidgetInstance,
} from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { builtinWidgetDescriptors } from './builtin-descriptors';

function options(columns: number): LayoutLintOptions {
  return {
    columns,
    spanLimitsOf: (w: WidgetInstance) =>
      spanLimitsOfLayout(builtinWidgetDescriptors[w.type as keyof typeof builtinWidgetDescriptors]?.layout),
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

// Known failures: `board|widget|code[|breakpoint]`. A RATCHET, not a waiver. The test fails on a
// finding that is not listed (a regression) AND on a listed one that no longer occurs (the fix
// landed, so the entry must go). Empty: every committed board passes the lint.
const KNOWN_FAILURES: ReadonlySet<string> = new Set<string>([]);

// The breakpoint is part of the key, so a finding that survives at one breakpoint's placement
// after a partial fix cannot hide behind an entry another breakpoint still earns.
const key = (board: string, f: LayoutFinding) =>
  [board, f.widgetId, f.code, f.breakpoint].filter((p) => p !== undefined).join('|');

describe('the committed sim boards', () => {
  it('include buildingpulse, widgetlab and towerpulse, not only sitepulse', () => {
    expect(boards()).toEqual(['bp-dashboard', 'sp-dashboard', 'tp-annex-dashboard', 'tp-dashboard', 'wl-gallery', 'wl-stress']);
  });

  it('are written in the current format, so nothing about them is converted on load', () => {
    for (const token of boards()) expect(load(token).convertedFrom, token).toBeUndefined();
  });

  it('use only widget types the built-in descriptors describe', () => {
    // A type missing from them would lint as an info finding and pass: the descriptors must be
    // complete or the lint silently covers less than it appears to.
    for (const token of boards()) {
      for (const w of load(token).widgets) {
        expect(builtinWidgetDescriptors, `${token}: ${w.id} is a ${w.type}`).toHaveProperty(w.type);
      }
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
