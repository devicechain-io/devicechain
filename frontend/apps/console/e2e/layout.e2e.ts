// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

// The console layout gate: dashboard-editor controls must be fully on screen and must not sit
// on top of what they edit, at the widths the console runs at and in every shipped locale
// (Spanish labels run longer than English ones).
//
//   Version history  every Make live and Roll back button lies wholly inside every box that
//                    clips it (the drawer, the table's rounded frame) and inside the viewport,
//                    and shows its whole label.
//   Canvas toolbar   the selected widget's toolbar (Grid, Relative, Duplicate, Copy, Delete)
//                    lies wholly OUTSIDE the widget it edits (so it can never cover its title
//                    or its content), and inside what the canvas can show: not cut off above
//                    or to the left, where a scroll area cannot scroll, and not past its right
//                    edge, where it would need a sideways scroll.
//
// Each run also saves a screenshot of every case under test-results/layout/ as an ADVISORY
// artifact for a reviewer; nothing compares it with a baseline.

import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';

const LANGS = ['en', 'es'] as const;

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
  for (const width of VERSION_WIDTHS) {
    test(`version history actions are whole at ${width} px (${lang})`, async ({ page }) => {
      await open(page, `surface=versions&lang=${lang}`, width, 900);
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
      // The drawer scrolls inside itself; bring the history into the picture.
      await buttons.last().scrollIntoViewIfNeeded();
      await shot(page, `versions-${width}-${lang}`);
      expect(findings).toEqual([]);
    });
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
        const findings: string[] = [];
        for (const b of await buttons.all()) {
          const label = (await b.textContent())?.trim();
          const bbox = boxOf((await b.boundingBox())!);
          if (intersects(bbox, wbox)) findings.push(`${label}: covers the widget it edits ${JSON.stringify(wbox)}`);
          for (const f of await b.evaluate(clippingOf)) findings.push(`${label}: ${f}`);
        }
        await shot(page, `canvas-${id}-${width}-${lang}`);
        expect(findings).toEqual([]);
      });
    }
  }
}
