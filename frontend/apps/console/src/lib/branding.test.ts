// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { hexToHslChannels, applyBranding, contrastRatio } from './branding';

// The inverse conversion, written independently of the implementation, so the
// round-trip assertions below check a real property rather than restating the
// code under test. Mirrors the CSS `hsl()` function.
function hslChannelsToHex(channels: string): string {
  const [h, s, l] = channels.replace(/%/g, '').split(' ').map(Number);
  const sn = s / 100;
  const ln = l / 100;
  const k = (n: number) => (n + h / 30) % 12;
  const a = sn * Math.min(ln, 1 - ln);
  const f = (n: number) => ln - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  const hex = (x: number) =>
    Math.round(x * 255)
      .toString(16)
      .padStart(2, '0');
  return `#${hex(f(0))}${hex(f(8))}${hex(f(4))}`;
}

describe('hexToHslChannels', () => {
  it('rejects anything that is not a 6-digit hex', () => {
    // Defense in depth: a bad cached branding value must never reach setProperty.
    for (const bad of ['', '#fff', '208cb7', '#20 8cb7', '#gggggg', 'red', '#208cb7ff']) {
      expect(hexToHslChannels(bad)).toBeNull();
    }
  });

  it('emits bare space-separated channels with no hsl() wrapper', () => {
    // shadcn composes these as `hsl(var(--primary) / <alpha>)`; a wrapper here
    // would silently break every alpha variant that uses the branded token.
    const channels = hexToHslChannels('#208cb7')!;
    expect(channels).toMatch(/^-?[\d.]+ [\d.]+% [\d.]+%$/);
    expect(channels).not.toContain('hsl');
  });

  it('round-trips a tenant colour back to the exact hex they typed', () => {
    // THE REGRESSION THIS FILE EXISTS FOR. Rounding channels to integers loses
    // the colour: the platform's own #208cb7 rounded to `197 71% 42%`, which
    // renders #1f8cb7 — one channel off, and not the colour the tenant chose.
    // Same defect @devicechain/brand removed from our palette; it applied to
    // theirs too.
    const colors = [
      '#208cb7', // the brandmark blue — the value that exposed this
      '#24a3d6',
      '#1f425e',
      '#9aceec',
      '#e8489b',
      '#f5a524',
      '#000000',
      '#ffffff',
      '#7f7f7f',
    ];
    for (const hex of colors) {
      expect(hslChannelsToHex(hexToHslChannels(hex)!)).toBe(hex);
    }
  });

  it('handles achromatic input without producing NaN channels', () => {
    // max === min means hue is undefined; a naive division would emit "NaN NaN% ".
    expect(hexToHslChannels('#808080')).toBe('0 0% 50.2%');
  });
});

describe('applyBranding', () => {
  const root = () => document.documentElement;
  const varsOf = () =>
    Object.fromEntries(
      ['--primary', '--ring', '--sidebar-primary', '--sidebar-ring', '--accent'].map((v) => [
        v,
        root().style.getPropertyValue(v),
      ])
    );

  it('paints every var a brand channel drives, from one colour', () => {
    applyBranding({ primary: '#208cb7' } as never);
    const vars = varsOf();
    for (const v of ['--primary', '--ring', '--sidebar-primary', '--sidebar-ring']) {
      expect(vars[v]).toBe('197.1 70.2% 42.2%');
    }
  });

  it('clears every override when branding goes away', () => {
    // Safe on logout / tenant switch: the built-in stylesheet value must come
    // back rather than the previous tenant's colour sticking.
    applyBranding({ primary: '#e8489b', accent: '#f5a524' } as never);
    expect(root().style.getPropertyValue('--primary')).not.toBe('');
    applyBranding(null);
    expect(Object.values(varsOf()).every((x) => x === '')).toBe(true);
    expect(document.title).toBe('DeviceChain');
  });

  it('never injects a non-hex value', () => {
    applyBranding(null);
    applyBranding({ primary: 'red; background: url(evil)' } as never);
    expect(root().style.getPropertyValue('--primary')).toBe('');
  });
});

describe('contrastRatio', () => {
  it('spans the WCAG range and is order-independent', () => {
    expect(contrastRatio('#000000', '#ffffff')).toBeCloseTo(21, 5);
    expect(contrastRatio('#ffffff', '#000000')).toBeCloseTo(21, 5);
    expect(contrastRatio('#208cb7', '#208cb7')).toBeCloseTo(1, 5);
    expect(contrastRatio('nope', '#ffffff')).toBeNull();
  });
});
