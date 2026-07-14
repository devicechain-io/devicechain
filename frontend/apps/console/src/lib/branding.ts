// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Applies a tenant's resolved white-labeling (ADR-038) to the console shell. The
// theme is driven by shadcn HSL-CHANNEL design tokens (`--primary: 197 71% 42%`,
// space-separated, no hsl() wrapper) in index.css, so a stored hex must be
// converted to an HSL-channel string before setProperty. Writing onto
// document.documentElement as an inline style wins over both the :root and .dark
// stylesheet rules, so one brand color applies in both light and dark mode — the
// GA behavior; per-mode brand pairs are deferred (ADR-038 §5 / residual Q4).
//
// The token mapping is deliberate (ADR-038 §5):
//   - primary → the brand-accent channels (--primary, --ring, --sidebar-primary,
//     --sidebar-ring): safe, high-impact, and what "brand color" mostly means.
//   - accent  → --accent / --sidebar-accent.
//   - background/foreground → the BRANDED CHROME only (--sidebar-*), NEVER the
//     global --background/--foreground, so the page base still respects light/dark.
// A null field is skipped entirely, leaving the built-in token for that aspect.

import type { TenantBranding } from '@/lib/api/user-management';

// hexToHslChannels converts "#rrggbb" to the "H S% L%" channel string the tokens
// expect. Returns null for anything that is not a 6-digit hex (defense in depth —
// the server already validates, but a bad cached value must never inject CSS).
export function hexToHslChannels(hex: string): string | null {
  const m = /^#([0-9a-fA-F]{6})$/.exec(hex.trim());
  if (!m) return null;
  const n = parseInt(m[1], 16);
  const r = (n >> 16) & 0xff;
  const g = (n >> 8) & 0xff;
  const b = n & 0xff;
  const rf = r / 255;
  const gf = g / 255;
  const bf = b / 255;
  const max = Math.max(rf, gf, bf);
  const min = Math.min(rf, gf, bf);
  const l = (max + min) / 2;
  let h = 0;
  let s = 0;
  const d = max - min;
  if (d !== 0) {
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    switch (max) {
      case rf:
        h = (gf - bf) / d + (gf < bf ? 6 : 0);
        break;
      case gf:
        h = (bf - rf) / d + 2;
        break;
      default:
        h = (rf - gf) / d + 4;
        break;
    }
    h /= 6;
  }
  const H = Math.round(h * 360);
  const S = Math.round(s * 100);
  const L = Math.round(l * 100);
  return `${H} ${S}% ${L}%`;
}

// The CSS variables a given branding field drives. Multiple vars per field so one
// brand color paints every place that channel appears.
const PRIMARY_VARS = ['--primary', '--ring', '--sidebar-primary', '--sidebar-ring'];
const ACCENT_VARS = ['--accent', '--sidebar-accent'];
const BACKGROUND_VARS = ['--sidebar-background'];
const FOREGROUND_VARS = ['--sidebar-foreground'];

// Every var branding may touch — cleared before each apply so switching tenants or
// clearing an override reverts to the stylesheet default rather than sticking.
const ALL_BRANDING_VARS = [
  ...PRIMARY_VARS,
  ...ACCENT_VARS,
  ...BACKGROUND_VARS,
  ...FOREGROUND_VARS,
];

// applyBranding writes (or clears) the branding CSS vars + document title on the
// root element. Called whenever the resolved branding changes. Passing null (or a
// branding with all-null colors) removes every override, restoring the built-in
// theme — so it is safe to call on logout / tenant switch.
export function applyBranding(branding: TenantBranding | null | undefined) {
  const root = document.documentElement;
  for (const v of ALL_BRANDING_VARS) root.style.removeProperty(v);

  if (!branding) {
    document.title = 'DeviceChain';
    return;
  }

  setColor(root, branding.primary, PRIMARY_VARS);
  setColor(root, branding.accent, ACCENT_VARS);
  setColor(root, branding.background, BACKGROUND_VARS);
  setColor(root, branding.foreground, FOREGROUND_VARS);

  document.title = branding.title?.trim() || 'DeviceChain';
}

function setColor(root: HTMLElement, hex: string | null | undefined, vars: string[]) {
  if (!hex) return;
  const channels = hexToHslChannels(hex);
  if (!channels) return; // never inject a non-hex value
  for (const v of vars) root.style.setProperty(v, channels);
}

// contrastRatio returns the WCAG contrast ratio (1–21) between two hex colors, or
// null if either is not a 6-digit hex. Used only for the editor's non-blocking
// contrast HINT — contrast is never enforced (a hard gate risks rejecting a
// legitimate brand color; ADR-038 §4).
export function contrastRatio(hexA: string, hexB: string): number | null {
  const la = relativeLuminance(hexA);
  const lb = relativeLuminance(hexB);
  if (la === null || lb === null) return null;
  const [hi, lo] = la >= lb ? [la, lb] : [lb, la];
  return (hi + 0.05) / (lo + 0.05);
}

function relativeLuminance(hex: string): number | null {
  const m = /^#([0-9a-fA-F]{6})$/.exec(hex.trim());
  if (!m) return null;
  const n = parseInt(m[1], 16);
  const channel = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  const r = channel((n >> 16) & 0xff);
  const g = channel((n >> 8) & 0xff);
  const b = channel(n & 0xff);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
