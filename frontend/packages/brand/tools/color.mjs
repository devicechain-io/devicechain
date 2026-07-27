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

/** HSL (deg, %, %) -> `#rrggbb`. */
export function hslToHex(h, s, l) {
  const sn = s / 100;
  const ln = l / 100;
  const k = (n) => (n + h / 30) % 12;
  const a = sn * Math.min(ln, 1 - ln);
  const f = (n) => ln - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  const to255 = (x) => Math.round(x * 255);
  return (
    '#' +
    [to255(f(0)), to255(f(8)), to255(f(4))]
      .map((c) => c.toString(16).padStart(2, '0'))
      .join('')
  );
}

/**
 * Scale a colour's LIGHTNESS by `factor`, preserving hue and saturation.
 *
 * This is how Infima's primary ramp is built, and getting it wrong is not
 * subtle. The obvious implementation — linearly mixing toward white or black in
 * RGB — adds white to all three channels equally, which DESATURATES. Measured
 * against the docs site's existing ramp, a mix-to-white `lightest` came out at
 * 51.5% saturation against the correct 71.1%: a visibly washed-out, greyer blue.
 * Scaling lightness in HSL keeps the hue and the saturation exactly where they
 * were and only moves the value up or down the ramp.
 */
export function scaleLightness(hex, factor) {
  const { h, s, l } = hexToHsl(hex);
  return hslToHex(h, s, Math.max(0, Math.min(100, l * factor)));
}
