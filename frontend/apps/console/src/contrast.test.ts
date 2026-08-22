// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ---- WCAG contrast of the semantic colour tokens ---------------------------
//
// 🔴 A SOURCE-LEVEL GUARD, because contrast is invisible to every other test in
// this app. A token can render, satisfy every snapshot and drive every
// behavioural assertion while being unreadable — which is exactly what happened:
// `--warning` shipped at 1.99:1 against the white written on it, and nothing
// anywhere went red.
//
// The rule this file encodes is that EACH STATUS IS TWO COLOURS. `--x` is ink
// (text, dots, borders on the page ground); `--x-fill` is a solid surface with
// `--x-foreground` written on top. They have opposite contrast requirements, and
// in dark mode no single value satisfies both — the failure that produced this
// file was one token trying to do both jobs. See the token block in index.css.
//
// 🔴 THE STYLESHEETS ARE READ WITH node:fs, NOT `?raw`, AND THAT IS NOT A STYLE
// CHOICE. Vitest stubs CSS imports by default (`test.css` is off), so
// `import css from './index.css?raw'` resolves to the EMPTY STRING — it imports
// cleanly, types cleanly, and hands every assertion below nothing to check. A
// parser fed "" finds no tokens and no failures, which reads exactly like a pass.
// `parses tokens at all` exists to catch that class, and it is what caught this.
//
// The files are named explicitly rather than globbed so that adding a third theme
// file is a visible edit here rather than a silent change in coverage.
//
// The /dash app is included because it declares the same tokens against the same
// dark ground. It paints no filled status surfaces today, so its `--destructive`
// was wrong as ink with nothing yet using it — a defect waiting for its first caller.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

const SOURCES: Record<string, string> = {
  'apps/console/src/index.css': read('./index.css'),
  'apps/dashboard/src/theme.css': read('../../dashboard/src/theme.css'),
};

type Hsl = readonly [number, number, number];
type Theme = Record<string, Hsl>;

/** Strip comments FIRST. A selector name inside a comment is not a selector, and
 *  reading the block after one silently parses the wrong rule — which it did:
 *  theme.css documents `.dark` in its header comment, and an earlier version of
 *  this parser read the light block twice and reported dark numbers it had never
 *  looked at. `parses the dark block, not a comment that mentions it` below is
 *  what stops that coming back. */
function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '');
}

function block(css: string, selector: string): string {
  const re = new RegExp(`(^|[\\s}])${selector.replace('.', '\\.')}\\s*\\{`, 'm');
  const m = re.exec(css);
  if (!m) throw new Error(`no ${selector} block`);
  let depth = 0;
  const start = css.indexOf('{', m.index);
  for (let i = start; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}' && --depth === 0) return css.slice(start, i);
  }
  throw new Error(`unterminated ${selector} block`);
}

function tokens(text: string): Theme {
  const out: Record<string, Hsl> = {};
  const re = /--([a-z0-9-]+):\s*([\d.]+)\s+([\d.]+)%\s+([\d.]+)%\s*;/g;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    out[m[1]] = [Number(m[2]), Number(m[3]), Number(m[4])];
  }
  return out;
}

function themes(css: string): { light: Theme; dark: Theme } {
  const clean = stripComments(css);
  const light = tokens(block(clean, ':root'));
  // A token the dark block does not redefine keeps its light value — that is how
  // the cascade behaves, and modelling it is the whole point: `--success-fill` is
  // deliberately declared only once.
  return { light, dark: { ...light, ...tokens(block(clean, '.dark')) } };
}

function srgb(c: number): number {
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance([h, s, l]: Hsl): number {
  const sn = s / 100;
  const ln = l / 100;
  const c = (1 - Math.abs(2 * ln - 1)) * sn;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = ln - c / 2;
  const [r, g, b] = [
    [c, x, 0], [x, c, 0], [0, c, x], [0, x, c], [x, 0, c], [c, 0, x],
  ][Math.floor(h / 60) % 6];
  return 0.2126 * srgb(r + m) + 0.7152 * srgb(g + m) + 0.0722 * srgb(b + m);
}

function contrast(a: Hsl, b: Hsl): number {
  const [x, y] = [luminance(a), luminance(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

/** ink/surface pairs that carry TEXT — WCAG AA body text is 4.5:1. */
const TEXT_PAIRS: ReadonlyArray<readonly [string, string]> = [
  ['foreground', 'background'],
  ['muted-foreground', 'background'],
  ['card-foreground', 'card'],
  ['popover-foreground', 'popover'],
  ['primary-foreground', 'primary'],
  ['secondary-foreground', 'secondary'],
  ['accent-foreground', 'accent'],
  ['sidebar-foreground', 'sidebar-background'],
  ['sidebar-accent-foreground', 'sidebar-accent'],
  // The three filled status surfaces, each carrying white.
  ['destructive-foreground', 'destructive-fill'],
  ['success-foreground', 'success-fill'],
  ['warning-foreground', 'warning-fill'],
  // The three status INKS, on both grounds they are drawn against. `text-warning`
  // on a card is the use that failed at 2.63:1.
  ['destructive', 'background'],
  ['destructive', 'card'],
  ['success', 'background'],
  ['success', 'card'],
  ['warning', 'background'],
  ['warning', 'card'],
];

/** A filled badge is a graphic, not text: WCAG non-text contrast is 3:1. */
const GRAPHIC_PAIRS: ReadonlyArray<readonly [string, string]> = [
  ['destructive-fill', 'background'],
  ['success-fill', 'background'],
  ['warning-fill', 'background'],
];

// 🔴 DELIBERATELY NOT GATED: `--border` and `--input` against `--background`
// measure 1.27–1.87:1 in both apps and both themes, against the 3:1 that WCAG
// 1.4.11 asks of a control boundary. They are shadcn's own defaults rather than
// a value this repo chose, and the rule is genuinely arguable for a border that
// only decorates a surface the eye already separates. They are left OUT rather
// than gated at a target they happen to meet, because a threshold reverse-engineered
// from the current value is not a check. Raising them is its own decision.

describe.each(Object.entries(SOURCES))('%s', (_path, css) => {
  const { light, dark } = themes(css);

  it('parses tokens at all', () => {
    // Without this, a parser that returned {} would satisfy every assertion
    // below by never running one.
    expect(Object.keys(light).length).toBeGreaterThan(8);
  });

  it('parses the dark block, not a comment that mentions it', () => {
    const moved = Object.keys(light).filter((k) => String(light[k]) !== String(dark[k]));
    expect(moved.length).toBeGreaterThan(0);
  });

  for (const theme of ['light', 'dark'] as const) {
    const T = theme === 'light' ? light : dark;

    it(`${theme}: text pairs clear 4.5:1`, () => {
      const failures: string[] = [];
      let checked = 0;
      for (const [ink, ground] of TEXT_PAIRS) {
        if (!T[ink] || !T[ground]) continue;
        checked++;
        const ratio = contrast(T[ink], T[ground]);
        if (ratio < 4.5) failures.push(`--${ink} on --${ground}: ${ratio.toFixed(2)}:1`);
      }
      expect(checked).toBeGreaterThan(0);
      expect(failures).toEqual([]);
    });

    it(`${theme}: filled status surfaces clear 3:1 against the page`, () => {
      const failures: string[] = [];
      for (const [fill, ground] of GRAPHIC_PAIRS) {
        if (!T[fill] || !T[ground]) continue;
        const ratio = contrast(T[fill], T[ground]);
        if (ratio < 3) failures.push(`--${fill} on --${ground}: ${ratio.toFixed(2)}:1`);
      }
      expect(failures).toEqual([]);
    });
  }
});

describe('the check itself', () => {
  // 🔴 THE NEGATIVE CONTROL. Every assertion above is green, and a green
  // assertion proves nothing until the same machinery has been shown to fail.
  it('rejects a token planted below the threshold', () => {
    const bad: Theme = { background: [0, 0, 100], foreground: [0, 0, 92] };
    expect(contrast(bad.foreground, bad.background)).toBeLessThan(4.5);
  });

  it('agrees with published WCAG reference ratios', () => {
    // Black on white is exactly 21:1; mid-grey #767676 on white is the canonical
    // 4.54:1 boundary example. If the maths drifts, these move.
    expect(contrast([0, 0, 0], [0, 0, 100])).toBeCloseTo(21, 5);
    expect(contrast([0, 0, 46.3], [0, 0, 100])).toBeCloseTo(4.54, 1);
  });

  it('ignores a selector that only appears inside a comment', () => {
    const css = '/* :root is light; .dark is dark */\n:root { --a: 0 0% 0%; }\n.dark { --a: 0 0% 100%; }';
    const { light, dark } = themes(css);
    expect(light.a).toEqual([0, 0, 0]);
    expect(dark.a).toEqual([0, 0, 100]);
  });
});
