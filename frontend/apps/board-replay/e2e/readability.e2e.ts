// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The board readability gate: every committed sim board, at 960 and 1920 px wide, in light and
// dark, must render with no clipped or overflowing text and no widget on top of another.
// The checks are in readability.ts; this file drives them and holds the ratchet.
//
// The assertion is the gate. Each run also saves a full-page screenshot of every board it
// measured under test-results/readability/, as an ADVISORY artifact: nothing compares it with a
// baseline and nothing fails on it. It is there for a reviewer to look at.

import { expect, test, type Page } from '@playwright/test';
import { readdirSync } from 'node:fs';
import path from 'node:path';

import { layoutSignature, measureReadability, type Finding } from './readability';

const SIM_DIR = path.resolve(import.meta.dirname, '../../../testdata/sim-dashboards');
const BOARDS = readdirSync(SIM_DIR)
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.slice(0, -'.json'.length))
  .sort();

const WIDTHS = [960, 1920] as const;
const HEIGHT = 1080;
const THEMES = ['light', 'dark'] as const;

// How many consecutive animation frames the board must hold still before it is measured.
const SETTLE_FRAMES = 20;

// Known failures: `board|widget|kind|width`, an overlap keyed by both widgets (`a+b`). A
// RATCHET, not a waiver: a finding not listed fails (a regression), and so does a listed one
// that no longer occurs at that width in either theme (the fix landed, so the entry goes).
const KNOWN_FAILURES: ReadonlySet<string> = new Set<string>([
  // The same table in the widget lab's half-width card, with seven columns: Raised is cut off.
  'wl-gallery|wl-alarm-table|text-clipped|960',
  // One unbreakable 101-letter word in a half-width label runs out of the card. The stress
  // board carries pathological DATA on purpose; a label that cannot wrap it is the widget's
  // defect, not the board's.
  'wl-stress|wl-stress-label|text-clipped|960',
]);

const keyOf = (board: string, width: number, f: Finding) =>
  [board, f.other ? `${f.widget}+${f.other}` : f.widget, f.kind, width].join('|');

const describeFinding = (f: Finding) => `${f.detail} [widget box ${JSON.stringify(f.box)}]`;

// Load a board on the gate page and measure it once it has settled.
async function measure(page: Page, board: string, width: number, theme: string) {
  await page.setViewportSize({ width, height: HEIGHT });
  await page.goto(`/e2e/page/gate.html?board=${board}&theme=${theme}`);
  await page.waitForFunction(() => document.documentElement.dataset.gate !== undefined);
  const state = await page.evaluate(() => ({ ...document.documentElement.dataset }));
  expect(state.gate, `the gate page could not draw ${board}: ${state.gateError}`).toBe('ready');

  await page.evaluate(() => document.fonts.ready.then(() => undefined));
  await page.waitForLoadState('networkidle');
  await page.addScriptTag({ content: `window.__gateSignature = ${layoutSignature.toString()};` });
  await page.waitForFunction(
    (frames) => {
      const w = window as unknown as { __gateSignature: () => string; __gateLast?: string; __gateSame?: number };
      const sig = w.__gateSignature();
      w.__gateSame = sig === w.__gateLast ? (w.__gateSame ?? 0) + 1 : 0;
      w.__gateLast = sig;
      return w.__gateSame >= frames;
    },
    SETTLE_FRAMES,
    { polling: 'raf', timeout: 30_000 },
  );

  const result = await page.evaluate(measureReadability);
  expect(result.widgets.length, `${board}: every widget in the definition is on the page`).toBe(Number(state.gateWidgets));

  await page.screenshot({
    path: path.join(test.info().project.outputDir, 'readability', `${board.replace('/', '-')}-${width}-${theme}.png`),
    fullPage: true,
  });
  return result;
}

// Nothing the page needs is off its own origin; anything else is refused, so no board depends
// on a network the CI runner may not have.
test.beforeEach(async ({ page, baseURL }) => {
  const origin = new URL(baseURL!).origin;
  await page.route(
    (url) => url.origin !== origin && url.protocol !== 'data:' && url.protocol !== 'blob:',
    (route) => route.abort(),
  );
});

test('the gate covers every committed sim board', () => {
  // A board added to testdata/sim-dashboards is gated without a change here; this pins that
  // the directory was read at all (a wrong path would gate nothing and pass).
  expect(BOARDS).toEqual(['bp-dashboard', 'sp-dashboard', 'wl-gallery', 'wl-stress']);
});

test('every known failure names a gated board and width', () => {
  for (const k of KNOWN_FAILURES) {
    const [board, , , width] = k.split('|');
    expect(BOARDS, k).toContain(board);
    expect(WIDTHS.map(String), k).toContain(width);
  }
});

for (const board of BOARDS) {
  for (const width of WIDTHS) {
    test(`${board} at ${width} px reads cleanly in light and dark`, async ({ page }) => {
      // key -> description -> the themes it was seen in
      const seen = new Map<string, Map<string, string[]>>();
      for (const theme of THEMES) {
        const { findings } = await measure(page, `sim/${board}`, width, theme);
        for (const f of findings) {
          const k = keyOf(board, width, f);
          const byDetail = seen.get(k) ?? new Map<string, string[]>();
          byDetail.set(describeFinding(f), [...(byDetail.get(describeFinding(f)) ?? []), theme]);
          seen.set(k, byDetail);
        }
      }
      const fresh = [...seen.keys()]
        .filter((k) => !KNOWN_FAILURES.has(k))
        .map((k) => `${k}: ${[...seen.get(k)!].map(([d, themes]) => `${d} (${themes.join(', ')})`).join('; ')}`);
      const stale = [...KNOWN_FAILURES].filter((k) => k.startsWith(`${board}|`) && k.endsWith(`|${width}`) && !seen.has(k));
      expect(fresh, 'new readability findings: fix the board or the widget, do not allowlist them').toEqual([]);
      expect(stale, 'allowlisted findings that no longer occur: delete these entries').toEqual([]);
    });
  }
}

// ---- The negative control --------------------------------------------------------
//
// A gate is worth nothing until it has been seen to fail. This board is broken on purpose and
// kept permanently: a card whose title cannot fit its width (the frame drops a title only below
// a height, so this card is tall enough to keep it) and whose reading is cut, a card whose title alone
// cannot fit it, a label that cannot fit, and two widgets nudged on top of each other, beside
// one widget that fits and must NOT be flagged (a check that flagged everything would pass the
// first half of this test).

for (const width of WIDTHS) {
  test(`the negative control fails the gate at ${width} px`, async ({ page }) => {
    for (const theme of THEMES) {
      const { findings } = await measure(page, 'fixture/gate-negative-control', width, theme);
      const kinds = (id: string) => new Set(findings.filter((f) => f.widget === id).map((f) => f.kind));
      const where = `${theme}: ${JSON.stringify(findings, null, 1)}`;
      // The card's reading outgrows its container (text-overflow) and the frame cuts it off
      // (text-clipped): each check is pinned by its own expectation, so dropping either one
      // from readability.ts fails here.
      expect([...kinds('nc-overflowing-card')].sort(), where).toEqual(['text-clipped', 'text-overflow']);
      expect(kinds('nc-overflowing-label').has('text-clipped'), where).toBe(true);
      // A frame title cut to an ellipsis is a finding, though the frame truncates it on
      // purpose (readability.ts says why). This card's reading fits; only its title does not.
      const title = findings.filter((f) => f.widget === 'nc-long-title');
      expect([...kinds('nc-long-title')].sort(), where).toEqual(['text-clipped', 'text-overflow']);
      expect(
        title.every((f) => f.detail.includes('A frame title far too long')),
        where,
      ).toBe(true);
      expect(title.some((f) => f.kind === 'text-overflow' && f.detail.endsWith('(cut to an ellipsis)')), where).toBe(true);
      expect(
        findings.some((f) => f.kind === 'overlap' && f.widget === 'nc-overlap-a' && f.other === 'nc-overlap-b'),
        where,
      ).toBe(true);
      expect(findings.filter((f) => f.widget === 'nc-clean' || f.other === 'nc-clean'), where).toEqual([]);
      // Every finding names its widget's box.
      for (const f of findings) expect(f.box.w * f.box.h, where).toBeGreaterThan(0);
    }
  });
}
