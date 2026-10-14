// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chart colour theme: concrete colours resolved from the host's CSS custom properties,
// because an ECharts canvas cannot resolve `var(--x)`. Pure DOM + React, no echarts import,
// but it lives on the `/echarts` subpath with the rest of the chart helpers so the SDK root
// stays chart-free.

import { useMemo, useSyncExternalStore } from 'react';

import { useWidgetThemeRoot } from '../context';
import { severityColorsAt, severityFallbackColors, type SeverityLevel } from '../severity';

// Concrete colors for an ECharts canvas, resolved from the CSS variables in scope
// at a given element (charts can't use CSS vars directly).
export interface ChartTheme {
  foreground: string;
  mutedForeground: string;
  border: string;
  series: string[];
  // The five threshold colours, concrete (comma-form `hsl(h, s%, l%)`, which every canvas
  // colour parser accepts and which severityFill can add an alpha to).
  severity: Record<SeverityLevel, string>;
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
  severity: severityFallbackColors(),
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
    severity: severityColorsAt(el, style),
  };
}

// --- shared chart-theme store ------------------------------------------------
//
// Charts need concrete colors resolved from the CSS-variable theme. Rather than
// have every chart widget mount its own MutationObserver on <html> and recompute
// the same theme, one page-wide observer resolves it once per change and every
// widget reads the shared, memoized value via useSyncExternalStore — so a busy
// dashboard pays O(1), not O(number of charts), per theme toggle.

let sharedTheme: ChartTheme | null = null;
const themeListeners = new Set<() => void>();
let themeObserver: MutationObserver | null = null;

function recomputeTheme(): void {
  sharedTheme = resolveChartTheme(document.documentElement);
}

function subscribeTheme(listener: () => void): () => void {
  if (!themeObserver) {
    recomputeTheme();
    themeObserver = new MutationObserver(() => {
      recomputeTheme();
      for (const l of themeListeners) l();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'style'],
    });
  }
  themeListeners.add(listener);
  return () => {
    themeListeners.delete(listener);
    if (themeListeners.size === 0 && themeObserver) {
      themeObserver.disconnect();
      themeObserver = null;
      sharedTheme = null;
    }
  };
}

function getThemeSnapshot(): ChartTheme {
  if (!sharedTheme) recomputeTheme();
  return sharedTheme as ChartTheme;
}

// useChartTheme returns the current chart colors and re-renders the caller when
// the document theme changes (a light/dark class or inline-style swap on <html>).
export function useChartTheme(): ChartTheme {
  const shared = useSyncExternalStore(subscribeTheme, getThemeSnapshot, getThemeSnapshot);
  // A host that scopes its tokens to a container says so (WidgetThemeRoot); its tokens are
  // fixed for the container's life, so they are read once there rather than observed.
  const root = useWidgetThemeRoot();
  const scoped = useMemo(() => (root ? resolveChartTheme(root) : null), [root]);
  return scoped ?? shared;
}

