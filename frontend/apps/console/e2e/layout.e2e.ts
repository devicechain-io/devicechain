// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The console layout gate: dashboard-editor controls must be fully on screen and must not sit
// on top of what they edit, at the widths the console runs at and in every shipped locale
// (Spanish labels run longer than English ones).
//
//   Version history  every Make live and Roll back button lies wholly inside every box that
//                    clips it (the drawer, the table's rounded frame) and inside the viewport,
//                    and shows its whole label; the live badge sits on one line, never broken
//                    inside its word as the address beside it may be.
//   Canvas toolbar   the selected widget's toolbar (Grid, Relative, Duplicate, Copy, Delete)
//                    lies wholly OUTSIDE the widget it edits (so it can never cover its title
//                    or its content), and inside what the canvas can show: not cut off above
//                    or to the left, where a scroll area cannot scroll, and not past its right
//                    edge, where it would need a sideways scroll.
//
// Each run also saves a screenshot of every case under test-results/layout/ as an ADVISORY
// artifact for a reviewer; nothing compares it with a baseline.

import { expect, test, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import path from 'node:path';

const LANGS = ['en', 'es'] as const;

// The live badge's label in each locale, read from the shipped catalogue so the gate follows a
// rewording. Spanish's ("Activa") is the longer one, and the one that broke.
const liveLabel = (lang: (typeof LANGS)[number]): string =>
  JSON.parse(readFileSync(new URL(`../src/i18n/locales/${lang}/dashboards.json`, import.meta.url), 'utf8')).versionLive;

interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

// Runs IN the page (page.evaluate serialises it): every way `el` is cut off. An ancestor that
// clips (overflow hidden or clip) must contain it; a scroll container must contain it except
// below, which is how a canvas grows; the viewport must contain it sideways.
function clippingOf(el: Element): string[] {
  const r = el.getBoundingClientRect();
  const out: string[] = [];
  const slack = 0.5;
  const name = (a: Element) => a.tagName.toLowerCase() + (a.getAttribute('data-testid') ? `[${a.getAttribute('data-testid')}]` : '');
  for (let a = el.parentElement; a; a = a.parentElement) {
    const cs = getComputedStyle(a);
    const clipX = cs.overflowX !== 'visible';
    const clipY = cs.overflowY !== 'visible';
    if (!clipX && !clipY) {
      if (cs.position === 'fixed') break;
      continue;
    }
    const b = a.getBoundingClientRect();
    const scrollsY = cs.overflowY === 'auto' || cs.overflowY === 'scroll';
    // A scroll container's visible box starts where its content starts once scrolled home.
    const left = b.left + a.clientLeft - a.scrollLeft;
    const top = b.top + a.clientTop - a.scrollTop;
    const right = b.left + a.clientLeft + a.clientWidth;
    const bottom = top + (scrollsY ? a.scrollHeight : a.clientHeight);
    if (clipX && r.left < left - slack) out.push(`cut off at the left by ${name(a)} (${(left - r.left).toFixed(1)} px)`);
    if (clipX && r.right > right + slack) out.push(`cut off at the right by ${name(a)} (${(r.right - right).toFixed(1)} px)`);
    if (clipY && r.top < top - slack) out.push(`cut off at the top by ${name(a)} (${(top - r.top).toFixed(1)} px)`);
    if (clipY && r.bottom > bottom + slack) out.push(`cut off at the bottom by ${name(a)} (${(r.bottom - bottom).toFixed(1)} px)`);
    // A fixed box (the drawer, the canvas host) is laid out against the viewport, so nothing
    // above it clips what is inside it.
    if (cs.position === 'fixed') break;
  }
  if (r.left < -slack || r.right > window.innerWidth + slack) out.push('outside the viewport sideways');
  const own = el as HTMLElement;
  if (own.scrollWidth > own.clientWidth + 1) out.push(`its label is truncated (${own.scrollWidth} > ${own.clientWidth})`);
  return out;
}

const boxOf = (r: DOMRect | { x: number; y: number; width: number; height: number }): Box => ({
  x: r.x,
  y: r.y,
  w: r.width,
  h: r.height,
});

const intersects = (a: Box, b: Box) => a.x < b.x + b.w - 0.5 && b.x < a.x + a.w - 0.5 && a.y < b.y + b.h - 0.5 && b.y < a.y + a.h - 0.5;

async function open(page: Page, query: string, width: number, height: number) {
  await page.setViewportSize({ width, height });
  await page.goto(`/e2e/page/layout.html?${query}`);
  await page.waitForFunction(() => document.documentElement.dataset.gate !== undefined);
  const state = await page.evaluate(() => ({ ...document.documentElement.dataset }));
  expect(state.gate, `the layout page could not draw ${query}: ${state.gateError}`).toBe('ready');
  await page.evaluate(() => document.fonts.ready.then(() => undefined));
}

async function shot(page: Page, name: string) {
  await page.screenshot({ path: path.join(test.info().project.outputDir, 'layout', `${name}.png`) });
}

// Nothing the page needs is off its own origin.
test.beforeEach(async ({ page, baseURL }) => {
  const origin = new URL(baseURL!).origin;
  await page.route(
    (url) => url.origin !== origin && url.protocol !== 'data:' && url.protocol !== 'blob:',
    (route) => route.abort(),
  );
});

// Phones and a small tablet (the drawer is the full width; 560 puts the history right at the
// width where it turns from lines back into a table), just past the drawer's max width, desktop.
const VERSION_WIDTHS = [320, 390, 560, 600, 700, 1440] as const;

for (const lang of LANGS) {
  // Labelled (the widest rows) and unlabelled (the narrowest first column, where the live badge
  // is the widest thing in it, so a table short of room squeezes it hardest).
  for (const labels of ['some', 'none'] as const) {
  for (const width of VERSION_WIDTHS) {
    test(`version history actions are whole at ${width} px (${lang}, labels: ${labels})`, async ({ page }) => {
      await open(page, `surface=versions&lang=${lang}&labels=${labels}`, width, 900);
      const rows = page.getByRole('dialog').locator('tbody tr');
      await expect(rows).toHaveCount(4);
      const buttons = rows.getByRole('button');
      // Three rows offer both actions, the live one only Roll back: the gate measured the
      // pairs it exists for, not an empty table.
      await expect(buttons).toHaveCount(7);
      const findings: string[] = [];
      for (const b of await buttons.all()) {
        const label = (await b.textContent())?.trim();
        for (const f of await b.evaluate(clippingOf)) findings.push(`${label}: ${f}`);
      }
      // On a phone the history is lines, not a table, so a row's actions get the drawer's whole
      // width and sit side by side under it: squeezed into a fourth column they would stack, one above
      // the other.
      if (width < 560) {
        for (const row of await rows.all()) {
          const tops = await row.getByRole('button').evaluateAll((bs) => bs.map((b) => b.getBoundingClientRect().top));
          if (tops.some((t) => Math.abs(t - tops[0]) > 1)) findings.push(`a row's actions are not on one line: tops ${tops.join(', ')}`);
        }
      }
      // The cells holding an address or a label may break anywhere, so that a long one cannot
      // hold the row wider than the drawer; a short label must never break inside its word.
      // The live badge once rendered as "Activ" over "a". Each line its text occupies is one
      // client rect of that text, so a whole badge has exactly one.
      const live = liveLabel(lang);
      const badge = rows.getByText(live, { exact: true });
      await expect(badge).toHaveCount(1);
      const lines = await badge.evaluate((el) => {
        const tops = new Set<number>();
        for (const node of Array.from(el.childNodes)) {
          if (node.nodeType !== Node.TEXT_NODE || !node.textContent?.trim()) continue;
          const range = document.createRange();
          range.selectNodeContents(node);
          for (const r of Array.from(range.getClientRects())) if (r.width > 0) tops.add(Math.round(r.top));
        }
        return tops.size;
      });
      if (lines !== 1) findings.push(`the live badge "${live}" is laid out on ${lines} lines`);
      // The drawer scrolls inside itself; bring the history into the picture.
      await buttons.last().scrollIntoViewIfNeeded();
      await shot(page, `versions-${width}-${lang}-labels-${labels}`);
      expect(findings).toEqual([]);
    });
  }
  }
}

// A small phone (where the toolbar wraps to two lines), a phone, a laptop with the config
// panel open beside the canvas, a desktop.
const CANVAS_WIDTHS = [320, 390, 960, 1440] as const;
const WIDGETS = ['top-left', 'top-right', 'below', 'narrow'] as const;

for (const lang of LANGS) {
  for (const width of CANVAS_WIDTHS) {
    for (const id of WIDGETS) {
      test(`the toolbar on ${id} clears the widget at ${width} px (${lang})`, async ({ page }) => {
        await open(page, `surface=canvas&selected=${id}&lang=${lang}`, width, 800);
        const widget = page.getByTestId(`canvas-widget-${id}`);
        await expect(widget).toHaveAttribute('data-selected', 'true');
        const toolbar = page.getByTestId('widget-toolbar');
        const buttons = toolbar.getByRole('button');
        // Grid, Relative, Duplicate, Copy, Delete.
        await expect(buttons).toHaveCount(5);
        await expect(page.getByTestId('placement-grid')).toBeVisible();
        const wbox = boxOf((await widget.boundingBox())!);
        // The resize handles straddle the widget's edges, so they reach past its box: a toolbar
        // that clears the box can still sit on a handle.
        const handles = await Promise.all(
          (await widget.locator('[data-testid^="handle-"]').all()).map(async (h) => ({
            name: await h.getAttribute('data-testid'),
            box: boxOf((await h.boundingBox())!),
          })),
        );
        expect(handles.length, 'a selected grid widget shows its resize handles').toBeGreaterThan(0);
        const findings: string[] = [];
        let lowest = -Infinity;
        for (const b of await buttons.all()) {
          const label = (await b.textContent())?.trim();
          const bbox = boxOf((await b.boundingBox())!);
          lowest = Math.max(lowest, bbox.y + bbox.h);
          if (intersects(bbox, wbox)) findings.push(`${label}: covers the widget it edits ${JSON.stringify(wbox)}`);
          for (const h of handles) if (intersects(bbox, h.box)) findings.push(`${label}: covers the ${h.name} resize handle`);
          for (const f of await b.evaluate(clippingOf)) findings.push(`${label}: ${f}`);
        }
        // Above is where the toolbar belongs whenever it fits there: always for a widget with a
        // row above it, and for a first-row widget wherever the toolbar is one line (the editor
        // keeps a line's room above the first row). Below is only for a first row whose toolbar
        // has wrapped, on a phone.
        if (!id.startsWith('top-') || width >= 960) {
          if (lowest > wbox.y + 0.5) findings.push(`the toolbar is not above the widget (its bottom ${lowest} > the widget's top ${wbox.y})`);
        }
        await shot(page, `canvas-${id}-${width}-${lang}`);
        expect(findings).toEqual([]);
      });
    }
  }
}

// While a widget picks its host, a banner sticks to the top of the canvas saying so. Scrolled,
// the selected widget's toolbar passes under it, and the banner must stay on top: it is the
// one thing telling the author what the next click does.
test('the pick-a-host banner stays over the toolbar when the canvas scrolls', async ({ page }) => {
  await open(page, 'surface=canvas&selected=below&lang=en', 390, 160);
  await page.getByTestId('placement-relative').click();
  const banner = page.getByTestId('pick-host-hint');
  await expect(banner).toBeVisible();
  const covered = await page.evaluate(() => {
    const hint = document.querySelector('[data-testid="pick-host-hint"]')!;
    const scroller = hint.parentElement!;
    const toolbar = document.querySelector('[data-testid="widget-toolbar"]')!;
    // Scroll until the toolbar's middle sits on the banner's middle.
    const h = hint.getBoundingClientRect();
    const t = toolbar.getBoundingClientRect();
    scroller.scrollTop += t.top + t.height / 2 - (h.top + h.height / 2);
    const h2 = hint.getBoundingClientRect();
    const t2 = toolbar.getBoundingClientRect();
    if (Math.abs(t2.top + t2.height / 2 - (h2.top + h2.height / 2)) > 2) return `could not scroll the toolbar under the banner (${t2.top} vs ${h2.top})`;
    const hit = document.elementFromPoint(t2.left + t2.width / 2, h2.top + h2.height / 2);
    return hit && hint.contains(hit) ? null : `the toolbar is drawn over the banner (hit ${hit?.outerHTML.slice(0, 80)})`;
  });
  await shot(page, 'canvas-pick-host-banner');
  expect(covered).toBeNull();
});

// A pinned device type the catalogue no longer holds is named in full ("unknown type <token>"):
// in a narrow list row the picker wraps it rather than cutting it off with an ellipsis.
for (const width of [320, 390, 960] as const) {
  test(`an unknown device type in the settings list is readable at ${width} px`, async ({ page }) => {
    await open(page, 'surface=settings&lang=en', width, 1100);
    const cell = page.getByTestId('list-cell-deviceTypes-1-token');
    const label = cell.locator('[data-shot="combobox"] > span').first();
    await expect(label).toContainText('unknown type retired-boiler-controller-v2-legacy');
    const findings = await label.evaluate((el) => {
      const own = el as HTMLElement;
      const out: string[] = [];
      if (own.scrollWidth > own.clientWidth + 1) out.push(`its label is truncated (${own.scrollWidth} > ${own.clientWidth})`);
      if (own.getBoundingClientRect().right > window.innerWidth) out.push('outside the viewport sideways');
      return out;
    });
    await shot(page, `settings-unknown-type-${width}`);
    expect(findings).toEqual([]);
    // The resolved row beside it keeps its single line.
    await expect(page.getByTestId('list-cell-deviceTypes-0-token')).toContainText('Thermostat');
  });
}
