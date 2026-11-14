// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The chart colour theme: concrete colours resolved from the host's CSS custom properties,
// because an ECharts canvas cannot resolve `var(--x)`. Pure DOM + React, no echarts import,
// but it lives on the `/echarts` subpath with the rest of the chart helpers so the SDK root
// stays chart-free.

import { useSyncExternalStore } from 'react';

import { useWidgetThemeRoot } from '../context';
import { severityColorsAt, severityFallbackColors, type AlarmLevel } from '../severity';

// Concrete colors for an ECharts canvas, resolved from the CSS variables in scope
// at a given element (charts can't use CSS vars directly).
export interface ChartTheme {
  foreground: string;
  mutedForeground: string;
  border: string;
  series: string[];
  // The four coloured threshold levels, concrete (comma-form `hsl(h, s%, l%)`, which every
  // canvas colour parser accepts and which severityFill can add an alpha to). `normal` has
  // none: it renders in the widget's own neutral (see severity.ts).
  severity: Record<AlarmLevel, string>;
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

// One store per theme root: charts in the same embed share a snapshot and observer.
// Weak keys allow an unmounted embed's container and store to be collected.
const themeStores = new WeakMap<Element, ReturnType<typeof createThemeStore>>();

function createThemeStore(root: Element) {
  let theme: ChartTheme | null = null;
  const listeners = new Set<() => void>();
  let observer: MutationObserver | null = null;
  const recompute = () => { theme = resolveChartTheme(root); };

  return {
    getSnapshot: (): ChartTheme => {
      if (!theme) recompute();
      return theme as ChartTheme;
    },
    subscribe: (listener: () => void): (() => void) => {
      if (!observer) {
        recompute();
        observer = new MutationObserver(() => {
          recompute();
          for (const notify of listeners) notify();
        });
        // A host can override tokens locally or inherit a palette from an ancestor.
        // Observe only those elements, never unrelated descendants of the page.
        for (let element: Element | null = root; element; element = element.parentElement) {
          observer.observe(element, { attributes: true, attributeFilter: ['class', 'style'] });
        }
      }
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0 && observer) {
          observer.disconnect();
          observer = null;
          theme = null;
        }
      };
    },
  };
}

// Resolve live colors at the host's container, or at <html> for the default theme.
export function useChartTheme(): ChartTheme {
  const root = useWidgetThemeRoot() ?? document.documentElement;
  let store = themeStores.get(root);
  if (!store) {
    store = createThemeStore(root);
    themeStores.set(root, store);
  }
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
}
