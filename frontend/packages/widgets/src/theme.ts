// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Theming without Tailwind (ADR-039: widgets are embeddable — no Tailwind leakage).
//
// Widgets style themselves with inline styles that reference the SAME CSS custom
// properties the console (and any host) defines — `--card`, `--foreground`,
// `--border`, `--chart-1..5`, etc. A host that sets those variables themes the
// widgets for free (light/dark included); a host without Tailwind still works.
//
// DOM elements can use `hsl(var(--card))` directly. ECharts renders to a <canvas>,
// which cannot resolve CSS variables — so for charts we read the variables off the
// live computed style and hand ECharts concrete color strings (resolveChartTheme).

// css returns an `hsl(var(--name))` reference for use in an inline style. The host
// defines the variable as HSL channels (e.g. `--card: 0 0% 100%`), matching the
// console's token set.
export function css(name: ThemeVar): string {
  return `hsl(var(--${name}))`;
}

// The subset of the console's design tokens widgets rely on. Kept small and
// semantic so the contract a host must provide is explicit and stable.
export type ThemeVar =
  | 'background'
  | 'foreground'
  | 'card'
  | 'card-foreground'
  | 'muted'
  | 'muted-foreground'
  | 'primary'
  | 'border'
  | 'destructive';

// Concrete colors for an ECharts canvas, resolved from the CSS variables in scope
// at a given element (charts can't use CSS vars directly).
export interface ChartTheme {
  foreground: string;
  mutedForeground: string;
  border: string;
  series: string[];
}

// Fallbacks used when a variable is absent (e.g. jsdom, or a host that hasn't set
// the token) so a chart still renders in sensible default colors.
const FALLBACK: ChartTheme = {
  foreground: 'hsl(240 10% 3.9%)',
  mutedForeground: 'hsl(240 3.8% 46.1%)',
  border: 'hsl(240 5.9% 90%)',
  series: [
    'hsl(220 70% 50%)',
    'hsl(160 60% 45%)',
    'hsl(30 80% 55%)',
    'hsl(280 65% 60%)',
    'hsl(340 75% 55%)',
  ],
};

function readVar(style: CSSStyleDeclaration, name: string, fallback: string): string {
  const raw = style.getPropertyValue(`--${name}`).trim();
  return raw ? `hsl(${raw})` : fallback;
}

// resolveChartTheme reads the theme tokens in scope at `el` into concrete colors
// for ECharts. Reading at the element (not the document root) means a widget
// nested under a re-themed subtree picks up the local override.
export function resolveChartTheme(el: Element): ChartTheme {
  const style = getComputedStyle(el);
  return {
    foreground: readVar(style, 'foreground', FALLBACK.foreground),
    mutedForeground: readVar(style, 'muted-foreground', FALLBACK.mutedForeground),
    border: readVar(style, 'border', FALLBACK.border),
    series: FALLBACK.series.map((fallback, i) => readVar(style, `chart-${i + 1}`, fallback)),
  };
}
