// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// useWidgetScale -- the numeric half of the size contract. HTML text sizes itself with `cq`
// units off the frame's content column (fitValueFontCss); a canvas (an ECharts gauge) cannot
// read those, so it asks this hook for the same box in numbers. Both read the one curve in
// ./fit-text and the one box -- the frame's padded content column -- so they agree.

import { useMemo, useRef } from 'react';

import { useWidgetBodySize, useWidgetDensityScale, type WidgetBodySize } from './context';
import { stickyChars, type StickyChars } from './fit-text';
import { tokenPx, type SizeToken } from './tokens';

export type SizeClass = 'compact' | 'regular' | 'large';

// The content-column sizes (px, at density 1; multiplied by the density scale) that separate the
// classes. Compact: too small for a full variant (a value and a caption); large: room for
// extras (a sparkline, a legend).
export const SIZE_CLASS_BREAKS = {
  compactWidth: 120,
  compactHeight: 72,
  largeWidth: 420,
  largeHeight: 220,
} as const;

export function sizeClassOf(size: WidgetBodySize, scale = 1): SizeClass {
  // Unmeasured (first frame, jsdom) is `regular`: the default rendering, not a degraded one.
  if (!(size.width > 0) || !(size.height > 0)) return 'regular';
  const b = SIZE_CLASS_BREAKS;
  if (size.width < b.compactWidth * scale || size.height < b.compactHeight * scale) return 'compact';
  if (size.width >= b.largeWidth * scale && size.height >= b.largeHeight * scale) return 'large';
  return 'regular';
}

export interface WidgetScale {
  sizeClass: SizeClass;
  // The board's density scale (1 outside a renderer).
  scale: number;
  // The frame's content column in px, quantised to 4.
  size: WidgetBodySize;
  // The px of a size token at this density, for a canvas.
  px: (token: SizeToken) => number;
}

export function makeWidgetScale(size: WidgetBodySize, scale: number): WidgetScale {
  return { sizeClass: sizeClassOf(size, scale), scale, size, px: (token: SizeToken) => tokenPx(token, scale) };
}

// useWidgetScale reads the frame's content column, so it works in a component RENDERED INSIDE a
// WidgetFrame -- not in the component that renders the frame (the frame's context does not
// reach above it). A widget that needs the numbers for its own top-level render passes
// WidgetFrame a function child instead: `<WidgetFrame>{(scale) => ...}</WidgetFrame>`.
export function useWidgetScale(): WidgetScale {
  const size = useWidgetBodySize();
  const scale = useWidgetDensityScale();
  return useMemo(() => makeWidgetScale(size, scale), [size, scale]);
}

// How long a readout keeps its longest recent length after it shrinks.
const STICKY_HOLD_MS = 60_000;

// useStickyChars -- the character count to hand the font curve for a live readout: the longest
// seen in the last minute, so 9.99 -> 10.01 does not resize the number.
export function useStickyChars(chars: number, holdMs = STICKY_HOLD_MS): number {
  const held = useRef<StickyChars | undefined>(undefined);
  // Written during render on purpose: the value is a pure function of (prev, chars, time) and
  // an extra render recomputes the same answer, so there is nothing to commit or clean up.
  held.current = stickyChars(held.current, chars, Date.now(), holdMs);
  return held.current.value;
}
