// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The board readability gate: every committed sim board, at 390, 960 and 1920 px wide, in light and
// dark, must render with no clipped or overflowing text and no widget on top of another.
// The checks are in readability.ts; this file drives them and holds the ratchet.
//
// The assertion is the gate. Each run also saves a whole-board screenshot of every board it
// measured under test-results/readability/, as an ADVISORY artifact: nothing compares it with a
// baseline and nothing fails on it. It is there for a reviewer to look at.

import { STACK_BELOW_WIDTH } from '@devicechain/dashboards';
import { expect, test, type Page } from '@playwright/test';
import { readdirSync } from 'node:fs';
import path from 'node:path';

import { layoutSignature, measureReadability, type Finding } from './readability';

const SIM_DIR = path.resolve(import.meta.dirname, '../../../testdata/sim-dashboards');
const BOARDS = readdirSync(SIM_DIR)
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.slice(0, -'.json'.length))
  .sort();

const WIDTHS = [390, 960, 1920] as const;
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
  // The stress board's unbreakable word again on a phone: deliberate data, as at 960.
  'wl-stress|wl-stress-label|text-clipped|390',
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
  // No empty first frame: the chart engine loads on demand, so a board whose charts mounted
  // before it arrived would show empty chart boxes that fill in later. Every sim board (and the
  // negative control) holds a chart, so the count is also the reach control.
  expect(Number(state.gateCharts), `${board}: no chart was on the first frame, so the check below saw nothing`).toBeGreaterThan(0);
  expect(state.gateChartsEmpty, `${board}: charts on the first frame that had not drawn`).toBe('0');

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
  // Nothing sticks out past the right edge of the board: at a phone width the board reflows
  // into the columns that fit rather than scrolling sideways.
  const stickout = await page.evaluate(() => {
    const grid = document.querySelector('[data-grid]');
    if (!grid) return 0;
    const edge = grid.getBoundingClientRect().right;
    return Math.max(0, ...[...document.querySelectorAll('[data-widget]')].map((el) => el.getBoundingClientRect().right - edge));
  });
  expect(stickout, `${board} at ${width} px: a widget runs past the board's right edge`).toBeLessThanOrEqual(1);
  // A phone reads one widget per row: every grid widget starts at the board's left edge (and
  // none overlaps, which measureReadability checks), so no two share a row. A relative widget
  // (data-host) sits where its host puts it and is not part of the column.
  if (width < STACK_BELOW_WIDTH) {
    const besideAnother = await page.evaluate(() => {
      const grid = document.querySelector('[data-grid]');
      if (!grid) return [];
      const edge = grid.getBoundingClientRect().left;
      return [...document.querySelectorAll('[data-widget]:not([data-host])')]
        .filter((el) => Math.abs(el.getBoundingClientRect().left - edge) > 1)
        .map((el) => el.getAttribute('data-widget'));
    });
    expect(besideAnother, `${board} at ${width} px: widgets not in a single column`).toEqual([]);
  }
  expect(result.widgets.length, `${board}: every widget in the definition is on the page`).toBe(Number(state.gateWidgets));

  // The whole board in one frame, taken by growing the viewport to the page's height rather than
  // with `fullPage`. Chromium's beyond-the-viewport capture repaints some layers at the wrong
  // scale -- big fitted numerals, gauge and map canvases drawn about twice their size and cropped
  // by their tiles -- while the DOM, the measurements above and an on-screen frame are all
  // correct. It did so in most runs of the 1920 px Sitepulse board, so the advisory image showed
  // a defect the board does not have. The page lays the board out at its natural height, so a
  // viewport that tall changes no widget's box.
  const height = await page.evaluate(() => document.documentElement.scrollHeight);
  if (height > HEIGHT) {
    await page.setViewportSize({ width, height });
    await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
  }
  await page.screenshot({
    path: path.join(test.info().project.outputDir, 'readability', `${board.replace('/', '-')}-${width}-${theme}.png`),
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
// cannot fit it, a label that cannot fit, a level gauge and a radial gauge whose fitted readings
// cannot fit their columns,
// and a widget on top of another, beside one widget that
// fits and must NOT be flagged (a check that flagged everything would pass the first half of this
// test). The parser refuses two grid widgets on one cell, so the overlap is a widget placed
// RELATIVE to a host and hung off its edge over a third widget it is not anchored to.

for (const width of WIDTHS) {
  test(`the negative control fails the gate at ${width} px`, async ({ page }) => {
    for (const theme of THEMES) {
      const { findings } = await measure(page, 'fixture/gate-negative-control', width, theme);
      const kinds = (id: string) => new Set(findings.filter((f) => f.widget === id).map((f) => f.kind));
      const where = `${theme}: ${JSON.stringify(findings, null, 1)}`;
      // The card's READING outgrows its container (text-overflow) and the frame cuts it off
      // (text-clipped): each check is pinned by its own expectation, so dropping either one
      // from readability.ts fails here. Its title and subject line are cut too, and a cut
      // ellipsis line alone yields both kinds (nc-long-title below), so the READING's findings
      // are picked out by its text (the gate page's synthetic 888.8, at the card's precision):
      // otherwise a check that stopped seeing a cut value would still pass on the header.
      // At a phone width the board reflows (six cells across), which changes how far each
      // fault reaches: the card's reading is only cut, not clipped. The exact kinds are pinned at
      // the two wide widths, where the authored layout is drawn; at 390 each fault must still be
      // seen, and the clean widget must still not be. The overlap is the exception: it hangs a relative widget off a
      // host's edge onto a third widget, and the reflow moves the host (and the badge with it) away.
      if (width < 960) {
        expect(kinds('nc-overflowing-card').has('text-overflow'), where).toBe(true);
        expect(kinds('nc-overflowing-label').has('text-clipped'), where).toBe(true);
        expect(kinds('nc-long-title').has('text-overflow'), where).toBe(true);
        expect(findings.filter((f) => f.widget === 'nc-clean' || f.other === 'nc-clean'), where).toEqual([]);
        continue;
      }
      expect([...kinds('nc-overflowing-card')].sort(), where).toEqual(['text-clipped', 'text-overflow']);
      const reading = findings.filter((f) => f.widget === 'nc-overflowing-card' && f.detail.startsWith('"888.8000'));
      expect([...new Set(reading.map((f) => f.kind))].sort(), where).toEqual(['text-clipped', 'text-overflow']);
      expect(kinds('nc-overflowing-label').has('text-clipped'), where).toBe(true);
      // A level widget's fitted reading: a vertical linear gauge one cell wide, reading 888.8 on
      // a 0..1 scale. The fit cannot shrink the text below its floor, so the value runs out of
      // the column; the gate must see a level widget's own readout, not only frame text.
      const level = findings.filter((f) => f.widget === 'nc-level-overflow' && f.detail.startsWith('"888.8000'));
      expect(level.some((f) => f.kind === 'text-clipped'), where).toBe(true);
      // The radial gauge's number, the same way. It used to be canvas text, which this gate cannot
      // see at all: a gauge one cell wide whose 888.8000 cannot fit the dial would have passed. It
      // is now a fitted DOM readout over the dial, so the cut must be found.
      const gauge = findings.filter((f) => f.widget === 'nc-gauge-overflow' && f.detail.startsWith('"888.8000'));
      expect(gauge.some((f) => f.kind === 'text-clipped'), where).toBe(true);
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

// ---- The showcase embed ------------------------------------------------------------
//
// The public site shows sitepulse through the replay's EMBED: the board laid out once on a
// fixed 960x1080 canvas, then scaled as one picture to whatever box the page gives it. A widget
// change that breaks that picture must fail here, so the embed is gated at the sizes the site
// uses, in both themes, three ways:
//
//   1. at the canvas's own size (scale 1) the board reads cleanly and every widget lies
//      inside the canvas: nothing cut off at its bottom or right edge;
//   2. in each container the canvas is scaled to exactly the largest size that fits, centred,
//      and wholly inside the container (letterboxed, never cropped);
//   3. in each container the board's layout, measured in CANVAS pixels, is the layout of
//      step 1: the container changed the scale and nothing else. That is what makes step 1's
//      readability hold at every scale, and it is the check a reflowing embed fails.
//
// Readability is measured at scale 1 only because measureReadability compares glyph boxes
// (which a transform scales) with element sizes (which it does not); step 3 is what carries
// the result to the scaled sizes.

const EMBED_CANVAS = { width: 960, height: 1080 } as const;
const EMBED_CONTAINERS: ReadonlyArray<readonly [number, number]> = [
  [1280, 720],
  [640, 360],
  // A phone held upright, and a 4K wall: letterboxed top and bottom, and scaled up 2x.
  [375, 667],
  [3840, 2160],
];

// The board's layout in canvas pixels: each widget's box relative to the canvas, divided by the
// canvas's scale, plus its text. Runs in the page.
function canvasLayout(): { scale: number; canvas: number[]; viewport: number[]; widgets: string[]; outside: string[] } {
  const c = document.querySelector<HTMLElement>('[data-testid="embed-canvas"]')!;
  const scale = Number(c.dataset.scale);
  const r = c.getBoundingClientRect();
  const q = (n: number) => Math.round(n * 2) / 2;
  const widgets: string[] = [];
  const outside: string[] = [];
  for (const w of Array.from(document.querySelectorAll<HTMLElement>('[data-widget]'))) {
    const b = w.getBoundingClientRect();
    const box = [(b.left - r.left) / scale, (b.top - r.top) / scale, b.width / scale, b.height / scale];
    widgets.push(`${w.dataset.widget}:${box.map(q).join(',')}:${w.textContent ?? ''}`);
    if (box[0] < -1 || box[1] < -1 || box[0] + box[2] > c.offsetWidth + 1 || box[1] + box[3] > c.offsetHeight + 1) {
      outside.push(`${w.dataset.widget} ${JSON.stringify(box.map(Math.round))}`);
    }
  }
  return { scale, canvas: [r.left, r.top, r.width, r.height], viewport: [innerWidth, innerHeight], widgets, outside };
}

async function embedAt(page: Page, size: readonly [number, number], theme: string, extra = '') {
  await page.setViewportSize({ width: size[0], height: size[1] });
  await page.goto(`/e2e/page/gate.html?board=sim/sp-dashboard&recording=showcase&theme=${theme}&embed=960x1080${extra}`);
  await page.waitForFunction(() => document.documentElement.dataset.gate !== undefined);
  const state = await page.evaluate(() => ({ ...document.documentElement.dataset }));
  expect(state.gate, `the gate page could not draw the embed: ${state.gateError}`).toBe('ready');
  expect(Number(state.gateCharts), 'the embed: no chart was on the first frame').toBeGreaterThan(0);
  expect(state.gateChartsEmpty, 'the embed: charts on the first frame that had not drawn').toBe('0');
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
  const layout = await page.evaluate(canvasLayout);
  await page.screenshot({ path: path.join(test.info().project.outputDir, 'readability', `embed-${size[0]}x${size[1]}-${theme}${extra.includes('reflow') ? '-control' : extra ? '-flow' : ''}.png`) });
  return layout;
}

test('the embed is gated at the canvas and the container sizes the site uses', () => {
  // Dropping a size from this list drops it from the gate; this pins the list.
  expect(EMBED_CANVAS).toEqual({ width: 960, height: 1080 });
  expect(EMBED_CONTAINERS).toEqual([
    [1280, 720],
    [640, 360],
    [375, 667],
    [3840, 2160],
  ]);
});

for (const theme of THEMES) {
  test(`the showcase embed holds its layout at every container size, ${theme}`, async ({ page }) => {
    // 1. The canvas at its own size: readable, and every widget inside it.
    const native = await embedAt(page, [EMBED_CANVAS.width, EMBED_CANVAS.height], theme);
    expect(native.scale).toBe(1);
    const { findings, widgets } = await page.evaluate(measureReadability);
    expect(widgets.length).toBeGreaterThan(0);
    expect(findings.map((f) => `${f.widget} ${f.kind}: ${describeFinding(f)}`), 'the embed canvas does not read cleanly').toEqual([]);
    expect(native.outside, 'widgets that run past the embed canvas').toEqual([]);

    for (const size of EMBED_CONTAINERS) {
      const at = await embedAt(page, size, theme);
      // 2. The largest scale that fits, centred, inside the container.
      const want = Math.min(size[0] / EMBED_CANVAS.width, size[1] / EMBED_CANVAS.height);
      expect(at.scale, `${size}: scale`).toBeCloseTo(want, 6);
      const [left, top, width, height] = at.canvas;
      expect(width).toBeCloseTo(EMBED_CANVAS.width * want, 1);
      expect(height).toBeCloseTo(EMBED_CANVAS.height * want, 1);
      expect(left).toBeGreaterThanOrEqual(-0.5);
      expect(top).toBeGreaterThanOrEqual(-0.5);
      expect(left + width).toBeLessThanOrEqual(size[0] + 0.5);
      expect(top + height).toBeLessThanOrEqual(size[1] + 0.5);
      expect(Math.abs(left - (size[0] - width - left)), `${size}: centred across`).toBeLessThanOrEqual(1);
      expect(Math.abs(top - (size[1] - height - top)), `${size}: centred down`).toBeLessThanOrEqual(1);
      // 3. The same layout, in canvas pixels, as at the canvas's own size.
      expect(at.widgets, `${size}: the embed reflowed`).toEqual(native.widgets);
      expect(at.outside).toEqual([]);
    }
  });
}

// A host page that gives the embed a width and no height (a block in its flow) gets the whole
// canvas at that width, at the canvas's own aspect ratio: never a box of zero height, which
// would scale the board to nothing and show the visitor an empty strip.
test('the showcase embed in a host with no height takes the canvas aspect ratio', async ({ page }) => {
  const native = await embedAt(page, [EMBED_CANVAS.width, EMBED_CANVAS.height], 'dark');
  for (const width of [375, 1280]) {
    const at = await embedAt(page, [width, 300], 'dark', '&host=flow');
    const want = width / EMBED_CANVAS.width;
    expect(at.scale, `${width}: scale`).toBeCloseTo(want, 6);
    const [left, top, w, h] = at.canvas;
    // (The box's height is fractional; the host measures whole pixels, so within half of one.)
    expect(Math.abs(left)).toBeLessThanOrEqual(0.5);
    expect(Math.abs(top)).toBeLessThanOrEqual(0.5);
    expect(w).toBeCloseTo(width, 1);
    expect(h).toBeCloseTo(EMBED_CANVAS.height * want, 1);
    expect(at.widgets, `${width}: the embed reflowed`).toEqual(native.widgets);
  }
});

// What step 3 compares is the text as well as the boxes, so the text must not depend on WHEN
// the gate runs: two renders a few seconds apart that straddle the instant an age turns from
// "13 hr. ago" to "14 hr. ago" would otherwise read as a reflow. The recording's ages are
// drawn as the embed draws them (its cursor read as now), whatever the wall clock says, so the
// same board drawn a day apart is the same text, and its ages read seconds, as a visitor's do.
test('the showcase embed reads the same whenever the gate runs', async ({ page }) => {
  const at = (iso: string) => Date.parse(iso);
  await page.clock.setFixedTime(at('2027-01-01T00:00:00Z'));
  const first = await embedAt(page, [EMBED_CANVAS.width, EMBED_CANVAS.height], 'dark');
  await page.clock.setFixedTime(at('2027-01-02T07:30:00Z'));
  const later = await embedAt(page, [EMBED_CANVAS.width, EMBED_CANVAS.height], 'dark');
  expect(later.widgets, 'the embed text moved with the wall clock').toEqual(first.widgets);
  const ages = first.widgets.join('\n').match(/\d+ [a-z]+\. ago|\d+ days? ago/g) ?? [];
  expect(ages.length, 'the embed shows no ages: this check has nothing to hold').toBeGreaterThan(0);
  expect(ages.filter((a) => !a.endsWith('sec. ago')), 'ages the embed would not show a visitor').toEqual([]);
});

// The negative control: the same board laid out at the container's width (no fixed canvas),
// which is the defect the embed prevents. The layout check must see it at both sizes.
test('the negative control: a reflowing embed fails the layout check', async ({ page }) => {
  const native = await embedAt(page, [EMBED_CANVAS.width, EMBED_CANVAS.height], 'dark');
  for (const size of EMBED_CONTAINERS) {
    const at = await embedAt(page, size, 'dark', '&reflow=1');
    expect(at.widgets, `${size}: a reflowed board passed the layout check`).not.toEqual(native.widgets);
  }
  // And a canvas too short for the board fails the inside-the-canvas check.
  await page.setViewportSize({ width: 960, height: 600 });
  await page.goto(`/e2e/page/gate.html?board=sim/sp-dashboard&recording=showcase&theme=dark&embed=960x600`);
  await page.waitForFunction(() => document.documentElement.dataset.gate === 'ready');
  await page.waitForSelector('[data-widget]');
  const short = await page.evaluate(canvasLayout);
  expect(short.outside.length, 'a board cut off by its canvas passed the inside-the-canvas check').toBeGreaterThan(0);
});
