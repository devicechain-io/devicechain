// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Colour conversion for the token generator. Hand-rolled rather than pulling a
// dependency: this package must stay installable with zero transitive deps so it
// can be consumed by the docs build, the console build and (eventually) a static
// site with no bundler at all.

/** @param {string} hex `#rrggbb` */
export function hexToRgb(hex) {
  const h = hex.trim().replace(/^#/, '');
  if (!/^[0-9a-fA-F]{6}$/.test(h)) {
    throw new Error(`not a 6-digit hex colour: ${hex}`);
  }
  return [
    parseInt(h.slice(0, 2), 16),
    parseInt(h.slice(2, 4), 16),
    parseInt(h.slice(4, 6), 16),
  ];
}

/**
 * sRGB -> HSL. Returns hue in degrees, saturation and lightness as percentages.
 *
 * Rounded to ONE decimal place deliberately. Full precision would emit values
 * like 197.14285714285714 that churn the generated files on every regeneration
 * for no visible benefit; integers would reintroduce exactly the rounding drift
 * this package exists to remove (`197 71% 42%` renders #1f8cb7, not #208cb7).
 */
export function hexToHsl(hex) {
  const [r8, g8, b8] = hexToRgb(hex);
  const r = r8 / 255;
  const g = g8 / 255;
  const b = b8 / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  let h = 0;
  let s = 0;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
    else if (max === g) h = ((b - r) / d + 2) / 6;
    else h = ((r - g) / d + 4) / 6;
  }
  const round1 = (n) => Math.round(n * 10) / 10;
  return { h: round1(h * 360), s: round1(s * 100), l: round1(l * 100) };
}

/**
 * The bare `H S% L%` triple shadcn/Tailwind expects, e.g. `197.1 70.2% 42.2%`.
 * Note the absence of `hsl(...)` — shadcn composes these into
 * `hsl(var(--primary) / <alpha>)`, so wrapping them breaks alpha support.
 */
export function hexToShadcnTriple(hex) {
  const { h, s, l } = hexToHsl(hex);
  return `${h} ${s}% ${l}%`;
}

/** Mix toward white (amount > 0) or black (amount < 0), amount in -1..1. */
export function shade(hex, amount) {
  const [r, g, b] = hexToRgb(hex);
  const t = amount < 0 ? 0 : 255;
  const p = Math.abs(amount);
  const ch = (c) => Math.round((t - c) * p + c);
  return (
    '#' +
    [ch(r), ch(g), ch(b)]
      .map((c) => c.toString(16).padStart(2, '0'))
      .join('')
  );
}
