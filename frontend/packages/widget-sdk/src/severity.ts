// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The five severity colours a threshold resolves to. A threshold names a LEVEL, never a
// colour: an author cannot write `#f00`, so a board stays coherent under a re-themed host,
// dark mode and (later) a greyscale operator theme where colour means severity and nothing
// else.
//
// 🔑 DEFINED ONCE, HERE. The values below are the single source of the `--sev-*` custom
// properties. A host that defines `--sev-<level>` as HSL channels (the same convention as
// every other token) wins; a host that does not still renders the colours below, light or
// dark, so a threshold never draws as nothing for want of a token. `severityTokenCss()` emits
// the same values as a stylesheet so a host's token set can be generated from this table
// instead of re-typed.

import { useMemo, useSyncExternalStore } from 'react';

import { useWidgetThemeRoot } from './context';

export const SEVERITY_LEVELS = ['normal', 'warning', 'minor', 'major', 'critical'] as const;
export type SeverityLevel = (typeof SEVERITY_LEVELS)[number];

export function isSeverityLevel(value: unknown): value is SeverityLevel {
  return typeof value === 'string' && (SEVERITY_LEVELS as readonly string[]).includes(value);
}

// HSL channels ("h s% l%"), the form the host tokens use. The warm-to-cool ramp mirrors the
// alarm severities (red, orange, amber, sky) so one hue means one severity on a card, a gauge
// band and an alarm row; `normal` is the green the ramp lacks. The dark column is lighter so
// each reads as INK against a dark ground.
export const SEVERITY_TOKENS: Readonly<Record<SeverityLevel, { light: string; dark: string }>> = {
  normal: { light: '142 71% 33%', dark: '142 64% 52%' },
  warning: { light: '199 89% 44%', dark: '199 89% 62%' },
  minor: { light: '32 95% 40%', dark: '38 92% 56%' },
  major: { light: '25 95% 48%', dark: '25 95% 62%' },
  critical: { light: '0 72% 46%', dark: '0 84% 66%' },
};

// The custom-property name a host defines for a level.
export function severityVar(level: SeverityLevel): string {
  return `--sev-${level}`;
}

// The tokens as CSS, `:root` light and `.dark` dark -- what a host stylesheet pastes (or a
// build step writes) so the host and this package cannot disagree about a colour.
export function severityTokenCss(): string {
  const block = (selector: string, side: 'light' | 'dark') =>
    `${selector} {\n${SEVERITY_LEVELS.map((l) => `  ${severityVar(l)}: ${SEVERITY_TOKENS[l][side]};`).join('\n')}\n}`;
  return `${block(':root', 'light')}\n${block('.dark', 'dark')}\n`;
}

// --- concrete colours ---------------------------------------------------------------
//
// A canvas (and an inline style that must be one value) needs a concrete colour, not
// `var(--sev-x)`. These resolve the level colours in scope at an element: the host's
// `--sev-<level>` token when it defines one, else the table above for the light or dark ground.
// They live HERE, not with the chart theme, so a DOM-only widget (a card colouring its value
// by threshold) gets them from the SDK root without reaching into the `/echarts` entry; the
// chart theme reads its severity colours through the same functions.

// A channel triple ("h s% l%", space- or comma-separated) as a comma-form hsl() colour, the
// form every canvas colour parser accepts and severityFill can add an alpha to.
function hslFromChannels(channels: string): string {
  const [h, s, l] = channels.replace(/,/g, ' ').trim().split(/\s+/);
  return `hsl(${h}, ${s}, ${l})`;
}

function severityColors(dark: boolean, host: (cssVar: string) => string | undefined): Record<SeverityLevel, string> {
  const out = {} as Record<SeverityLevel, string>;
  for (const level of SEVERITY_LEVELS) {
    out[level] = hslFromChannels(host(severityVar(level)) || SEVERITY_TOKENS[level][dark ? 'dark' : 'light']);
  }
  return out;
}

// Dark when the element sits under a `.dark` ancestor -- the host convention every token block
// here uses -- or the document root carries it.
function isDark(el: Element): boolean {
  return el.closest('.dark') !== null || el.ownerDocument.documentElement.classList.contains('dark');
}

// The light colours with no host involved (jsdom, a host with no tokens yet).
export function severityFallbackColors(): Record<SeverityLevel, string> {
  return severityColors(false, () => undefined);
}

// The colours in scope at `el`. `style` is its computed style when the caller already has it.
export function severityColorsAt(el: Element, style: CSSStyleDeclaration = getComputedStyle(el)): Record<SeverityLevel, string> {
  return severityColors(isDark(el), (name) => style.getPropertyValue(name).trim() || undefined);
}

// One page-wide observer, shared by every caller (the same O(1)-per-toggle shape as the chart
// theme store): re-resolved when <html> changes class (light/dark) or inline style (tokens).
let shared: Record<SeverityLevel, string> | null = null;
const listeners = new Set<() => void>();
let observer: MutationObserver | null = null;

function subscribe(listener: () => void): () => void {
  if (!observer) {
    shared = severityColorsAt(document.documentElement);
    observer = new MutationObserver(() => {
      shared = severityColorsAt(document.documentElement);
      for (const l of listeners) l();
    });
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style'] });
  }
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && observer) {
      observer.disconnect();
      observer = null;
      shared = null;
    }
  };
}

function snapshot(): Record<SeverityLevel, string> {
  shared ??= severityColorsAt(document.documentElement);
  return shared;
}

// useSeverityColors returns the five concrete severity colours and re-renders the caller when
// the document theme changes. A container-scoped theme (WidgetThemeRoot) is read once there,
// as the chart theme does.
export function useSeverityColors(): Record<SeverityLevel, string> {
  const page = useSyncExternalStore(subscribe, snapshot, snapshot);
  const root = useWidgetThemeRoot();
  const scoped = useMemo(() => (root ? severityColorsAt(root) : null), [root]);
  return scoped ?? page;
}
