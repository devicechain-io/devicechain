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
