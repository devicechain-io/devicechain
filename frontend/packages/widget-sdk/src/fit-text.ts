// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// fitValueFont -- the one font-size curve for a big readout (a card's number, a KPI count, a
// gauge's value). Every widget used to carry its own clamp of the card HEIGHT with a hard cap
// (44 px, 56 px); that ignored the width, so a long string overflowed a wide-but-short or
// narrow-but-tall box, and it stopped growing on a wall display. This curve reads both
// dimensions and the string length.
//
//   font = min( height * HEIGHT_SHARE ,  width / (chars * GLYPH_WIDTH) )
//
// then floored at MIN_FONT_PX. There is no upper cap: a bigger box gets a bigger number.
//
// `size` is the box the text may occupy, so a caller passes its content box (the measured
// element minus its own padding). HEIGHT_SHARE leaves room for the line's leading and for a
// secondary line (unit, caption) beneath it. GLYPH_WIDTH is the advance of a digit as a
// fraction of the font size. Measured: system faces (Segoe UI, Roboto, Arial bold) set a
// digit at 0.56-0.58 em, but the console's Inter sets them at 0.61-0.62 em on average and
// 0.67 em at the widest in the 600-700 weights a readout uses. 0.65 covers Inter's digits to
// within a few percent (which the caller's padding absorbs) without shrinking a system-font
// number much; a '.' or '-' counted as a full character adds slack.

export const FIT_MIN_FONT_PX = 12;
// What to use before the first measure (ElementSize is {0,0} until then) or in a non-DOM test.
export const FIT_FALLBACK_FONT_PX = 32;

// The curve's two constants. Exported so the CSS path (fitValueFontCss, `cq` units) and the
// JS path (fitValueFont, a canvas widget's numeric font) are built from THESE and cannot
// drift: a number in a card and a number in a gauge of the same size look the same.
export const FIT_HEIGHT_SHARE = 0.6;
export const FIT_GLYPH_WIDTH = 0.65;

export interface FitOptions {
  // The smallest font the curve returns, in px. Density scales THIS (a wall display's floor is
  // larger, a compact board's smaller); the curve itself never changes. Default FIT_MIN_FONT_PX.
  floor?: number;
}

function charCount(chars: number): number {
  return Math.max(1, Math.ceil(Number.isFinite(chars) ? chars : 1));
}

export function fitValueFont(size: { width: number; height: number }, chars: number, options: FitOptions = {}): number {
  const floor = options.floor ?? FIT_MIN_FONT_PX;
  if (!(size.width > 0) || !(size.height > 0)) return Math.max(floor, FIT_FALLBACK_FONT_PX);
  const byHeight = size.height * FIT_HEIGHT_SHARE;
  const byWidth = size.width / (charCount(chars) * FIT_GLYPH_WIDTH);
  return Math.max(floor, Math.floor(Math.min(byHeight, byWidth)));
}

// The same curve as a CSS length, for text inside a `container-type: size` box (the widget
// frame's content column): `cqh`/`cqw` are that box's height and width, so the browser resolves
// the font on the very first frame -- no {0,0} measure flash -- and re-resolves on every
// resize with no JS. The box a caller passes to fitValueFont is the SAME box these units
// measure (the frame's padded content column).
export function fitValueFontCss(chars: number, options: FitOptions = {}): string {
  const floor = options.floor ?? FIT_MIN_FONT_PX;
  const perChar = 100 / (charCount(chars) * FIT_GLYPH_WIDTH);
  return `max(${floor}px, min(${FIT_HEIGHT_SHARE * 100}cqh, ${Number(perChar.toFixed(3))}cqw))`;
}

// A big readout's character count held at the longest recently seen, so a value crossing
// 9.99 -> 10.01 does not jump the font size by a character's worth (~20 % on a short
// string). The count grows at once and shrinks only after `holdMs` with nothing longer.
export interface StickyChars {
  value: number;
  since: number;
}

export function stickyChars(prev: StickyChars | undefined, chars: number, now: number, holdMs: number): StickyChars {
  if (!prev || chars >= prev.value) return { value: chars, since: now };
  if (now - prev.since >= holdMs) return { value: chars, since: now };
  return prev;
}
