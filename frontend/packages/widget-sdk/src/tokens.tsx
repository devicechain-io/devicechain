// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Size tokens: the type ramp and spacing steps a widget draws from instead of writing px
// literals, and the density scale they all multiply by.
//
//   --dc-scale                          the board's density scale (0.9 compact / 1 comfortable / 1.5 wall)
//   --dc-font-<xs|sm|md|lg|xl|display>  calc(<base px> * var(--dc-scale))
//   --dc-space-<1..4>                   calc(<base px> * var(--dc-scale))
//
// The `dc-` prefix is load-bearing: these are set on a board that lives inside someone else's
// page, and the bare names collide -- Tailwind v4 keeps font FAMILIES in `--font-*` (a host's
// `--font-display: "Inter"` is its documented example), so `var(--font-display, 28px)` in such
// a host resolves to a family name and the font size is thrown away.
//
// The renderer sets them on its root (tokenStyle), so every widget under it scales with the
// board's density; `fontVar('md')` is `var(--dc-font-md, 13px)`, which still renders the base
// size anywhere the tokens are not set (a host that mounts a bare WidgetFrame). Canvas widgets
// cannot read a CSS variable and take the numeric equivalent from useWidgetScale().px().
//
// Colour tokens are the ThemeVar set in ./theme; the severity hues are generated from the
// table in ./severity (WidgetTokenStyles below injects them), so no colour is typed twice.

import { DENSITY_SCALE, type CanvasDensity } from '@devicechain/dashboards';
import type { CSSProperties } from 'react';

import { severityTokenCss } from './severity';

export const FONT_TOKENS = {
  xs: 11,
  sm: 12,
  md: 13,
  lg: 16,
  xl: 20,
  display: 28,
} as const;
export type FontToken = keyof typeof FONT_TOKENS;

export const SPACE_TOKENS = { 1: 4, 2: 8, 3: 12, 4: 16 } as const;
export type SpaceToken = keyof typeof SPACE_TOKENS;

// 'font-md' | 'space-3' -- the names useWidgetScale().px() accepts.
export type SizeToken = `font-${FontToken}` | `space-${SpaceToken}`;

export function densityScale(density: CanvasDensity | undefined): number {
  return DENSITY_SCALE[density ?? 'comfortable'];
}

export function fontVar(token: FontToken): string {
  return `var(--dc-font-${token}, ${FONT_TOKENS[token]}px)`;
}

export function spaceVar(token: SpaceToken): string {
  return `var(--dc-space-${token}, ${SPACE_TOKENS[token]}px)`;
}

// The numeric px of a size token at a scale, for a canvas that cannot read the variable.
export function tokenPx(token: SizeToken, scale = 1): number {
  const [kind, name] = token.split('-') as ['font' | 'space', string];
  const base = kind === 'font' ? FONT_TOKENS[name as FontToken] : SPACE_TOKENS[Number(name) as SpaceToken];
  return Math.round(base * scale * 100) / 100;
}

// The custom properties a board root sets for a density scale.
export function tokenStyle(scale: number): CSSProperties {
  const vars: Record<string, string> = { '--dc-scale': String(scale) };
  for (const [name, px] of Object.entries(FONT_TOKENS)) vars[`--dc-font-${name}`] = `calc(${px}px * var(--dc-scale))`;
  for (const [name, px] of Object.entries(SPACE_TOKENS)) vars[`--dc-space-${name}`] = `calc(${px}px * var(--dc-scale))`;
  return vars as CSSProperties;
}

// The severity hues as a stylesheet at ZERO specificity (`:where`), so a host that defines
// `--sev-<level>` itself (any ordinary `:root` / `.dark` rule) always wins and a host that
// does not still gets the colours. They sit on the document root, where useSeverityColors
// reads them -- nothing is scoped to a board element, so no WidgetThemeRoot registration is
// needed for them to resolve.
export function WidgetTokenStyles() {
  return <style data-dc-tokens="">{severityTokenCss({ hostWins: true })}</style>;
}
