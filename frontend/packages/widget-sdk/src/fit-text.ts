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

const HEIGHT_SHARE = 0.6;
const GLYPH_WIDTH = 0.65;

export function fitValueFont(size: { width: number; height: number }, chars: number): number {
  if (!(size.width > 0) || !(size.height > 0)) return FIT_FALLBACK_FONT_PX;
  const n = Math.max(1, Math.ceil(Number.isFinite(chars) ? chars : 1));
  const byHeight = size.height * HEIGHT_SHARE;
  const byWidth = size.width / (n * GLYPH_WIDTH);
  return Math.max(FIT_MIN_FONT_PX, Math.floor(Math.min(byHeight, byWidth)));
}
