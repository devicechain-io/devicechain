// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The arithmetic of the bar and pie charts, apart from the drawing: a value axis that rounds out
// to tidy ticks, and the angles of a pie's slices. Pure, so it is tested without a DOM.

export interface NiceDomain {
  min: number;
  max: number;
  step: number;
  ticks: number[];
}

// niceDomain returns an axis that holds every value and zero (a bar starts at zero, so the axis
// always includes it), rounded out to a 1, 2, 2.5 or 5 step so the ticks read as round numbers. All
// zeros give 0 to 1, so an empty chart still has an axis. Non-finite values are ignored.
export function niceDomain(values: readonly number[], intervals = 4): NiceDomain {
  const finite = values.filter(Number.isFinite);
  let lo = Math.min(0, ...finite);
  let hi = Math.max(0, ...finite);
  if (lo === hi) hi = lo + 1;
  const raw = (hi - lo) / Math.max(1, intervals);
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  const norm = raw / magnitude;
  const step = (norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 2.5 ? 2.5 : norm <= 5 ? 5 : 10) * magnitude;
  lo = Math.floor(lo / step + 1e-9) * step;
  hi = Math.ceil(hi / step - 1e-9) * step;
  // Rounded to the step's own decimals so 0.1 + 0.2 does not become a tick label.
  const decimals = Math.max(0, -Math.floor(Math.log10(step)) + 1);
  const round = (v: number) => Number(v.toFixed(decimals));
  const ticks: number[] = [];
  for (let i = 0; lo + i * step <= hi + step * 1e-9; i++) ticks.push(round(lo + i * step));
  return { min: round(lo), max: round(hi), step, ticks };
}

export interface Slice {
  index: number;
  // The share of the whole, 0 to 1.
  fraction: number;
  // Clockwise from twelve o'clock, in turns (0 to 1).
  from: number;
  to: number;
}

// pieSlices lays the values out clockwise from the top, in the order given. A value that is null,
// zero or negative takes no angle (it keeps its place in the list with a fraction of 0), so the
// angles of the rest are shares of the sum of the positive ones.
export function pieSlices(values: readonly (number | null)[]): Slice[] {
  const positive = values.map((v) => (v !== null && v > 0 ? v : 0));
  const total = positive.reduce((a, v) => a + v, 0);
  let at = 0;
  return positive.map((v, index) => {
    const fraction = total > 0 ? v / total : 0;
    const slice = { index, fraction, from: at, to: at + fraction };
    at += fraction;
    return slice;
  });
}

// The point on a circle of radius r about (cx, cy), `turns` clockwise from twelve o'clock.
export function polar(cx: number, cy: number, r: number, turns: number): { x: number; y: number } {
  const angle = turns * 2 * Math.PI;
  return { x: cx + r * Math.sin(angle), y: cy - r * Math.cos(angle) };
}

// The SVG path of an open arc about (cx, cy) at radius r, for a ring segment drawn as a stroke. A
// span that fills the circle has no path here (an arc from a point to itself draws nothing): the
// caller draws a circle, as wedgePath does for the pie. An empty span has none either.
export function arcPath(cx: number, cy: number, r: number, from: number, to: number): string | undefined {
  const span = to - from;
  if (span <= 0 || span >= 1 - 1e-9) return undefined;
  const a = polar(cx, cy, r, from);
  const b = polar(cx, cy, r, to);
  return `M${a.x},${a.y}A${r},${r} 0 ${span > 0.5 ? 1 : 0} 1 ${b.x},${b.y}`;
}

// The SVG path of one wedge. A slice that fills the circle is a circle (an arc from a point to
// itself draws nothing), and an empty one has no path at all.
export function wedgePath(cx: number, cy: number, r: number, from: number, to: number): string | undefined {
  const span = to - from;
  if (span <= 0) return undefined;
  if (span >= 1 - 1e-9) {
    return `M${cx},${cy - r}A${r},${r} 0 1 1 ${cx},${cy + r}A${r},${r} 0 1 1 ${cx},${cy - r}Z`;
  }
  const a = polar(cx, cy, r, from);
  const b = polar(cx, cy, r, to);
  return `M${cx},${cy}L${a.x},${a.y}A${r},${r} 0 ${span > 0.5 ? 1 : 0} 1 ${b.x},${b.y}Z`;
}
