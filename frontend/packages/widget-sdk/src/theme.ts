// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Theming without Tailwind (widgets are embeddable — no Tailwind leakage).
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
//
// Colours only: sizes are SizeTokens (see ./tokens). The chart ramp and the severity hues are
// part of the set, so a widget colours itself from a token rather than a literal; `normal` has
// NO severity token (it renders in the ordinary foreground, see ./severity).
export type ThemeVar =
  | 'background'
  | 'foreground'
  | 'card'
  | 'card-foreground'
  | 'muted'
  | 'muted-foreground'
  | 'primary'
  | 'primary-foreground'
  | 'border'
  | 'destructive'
  | 'chart-1'
  | 'chart-2'
  | 'chart-3'
  | 'chart-4'
  | 'chart-5'
  | 'sev-critical'
  | 'sev-major'
  | 'sev-minor'
  | 'sev-warning';
