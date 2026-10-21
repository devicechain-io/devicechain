// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The trend strip under a stat card's figure: the card's own buckets drawn as one line, so "is it
// rising?" is answered on the card itself. A few lines of SVG, no chart engine, so it adds
// nothing to the lazy chunk it lives in beyond itself.
//
//   - it is NEUTRAL: the stroke is the muted foreground, never a threshold colour. The figure
//     carries the band; a trend that turned red as well would say the same thing twice, and a
//     healthy card stays calm (ISA-101);
//   - the x axis is time, not index, and the line breaks where a stretch of buckets is missing, so a
//     device that went silent shows a gap rather than a line drawn across it;
//   - a lone point is a dot, and no point draws nothing (the figure beside it says why);
//   - each point is the bucket's own aggregate for the statistic the card shows. The latest
//     reading has no bucket aggregate, so its strip is the bucket averages: a trend, never a reading.

import type { AggregateTrendPoint } from '@devicechain/dashboards';
import { css } from '@devicechain/widget-sdk';

import type { AggregateStat } from './aggregate-strings';

export interface TrendPoint {
  at: number;
  value: number;
}

// The value a bucket contributes for the statistic the card shows.
export function trendValue(point: AggregateTrendPoint, stat: AggregateStat): number {
  switch (stat) {
    case 'min':
      return point.min;
    case 'max':
      return point.max;
    case 'sum':
      return point.sum;
    case 'count':
      return point.count;
    case 'avg':
    case 'last':
      return point.avg;
  }
}

export function trendPoints(series: readonly AggregateTrendPoint[] | undefined, stat: AggregateStat): TrendPoint[] {
  if (!series) return [];
  const out: TrendPoint[] = [];
  for (const p of series) {
    const at = Date.parse(p.bucketStart);
    const value = trendValue(p, stat);
    if (Number.isFinite(at) && Number.isFinite(value)) out.push({ at, value });
  }
  return out.sort((a, b) => a.at - b.at);
}

const VIEW_WIDTH = 100;
const PAD = 3;

export interface SparklineGeometry {
  // One "M x y L x y ..." sub-path per run of consecutive buckets.
  d: string;
  // The newest point as a percentage of the strip, for the end dot.
  end: { left: number; top: number } | undefined;
  points: number;
}

// sparklineGeometry maps the points into a VIEW_WIDTH x height box. A gap -- a stretch several times
// longer than the series' usual step -- starts a new run.
export function sparklineGeometry(points: readonly TrendPoint[], height: number, bucketSeconds: number): SparklineGeometry {
  if (points.length === 0) return { d: '', end: undefined, points: 0 };
  const t0 = points[0].at;
  const t1 = points[points.length - 1].at;
  let lo = Infinity;
  let hi = -Infinity;
  for (const p of points) {
    lo = Math.min(lo, p.value);
    hi = Math.max(hi, p.value);
  }
  const x = (at: number): number => (t1 === t0 ? VIEW_WIDTH / 2 : PAD + ((at - t0) / (t1 - t0)) * (VIEW_WIDTH - 2 * PAD));
  // A flat series sits mid-strip: there is nothing to scale, and the bottom edge would read as "low".
  const y = (v: number): number => (hi === lo ? height / 2 : PAD + (1 - (v - lo) / (hi - lo)) * (height - 2 * PAD));
  // A device that reports every five minutes into one-minute buckets has a point every five
  // buckets, and that is a line, not five gaps. A gap is a stretch several times longer than the
  // series' own usual step (and never under two buckets).
  const steps = points.slice(1).map((p, i) => p.at - points[i].at).sort((a, b) => a - b);
  const usual = steps.length > 0 ? steps[Math.floor(steps.length / 2)] : 0;
  const gap = Math.max(bucketSeconds * 2000, usual * 3);
  let d = '';
  let prev: number | undefined;
  for (const p of points) {
    const cmd = prev === undefined || p.at - prev > gap ? 'M' : 'L';
    d += `${d ? ' ' : ''}${cmd}${x(p.at).toFixed(2)} ${y(p.value).toFixed(2)}`;
    prev = p.at;
  }
  const last = points[points.length - 1];
  return { d, end: { left: (x(last.at) / VIEW_WIDTH) * 100, top: (y(last.value) / height) * 100 }, points: points.length };
}

export function Sparkline({
  points,
  height,
  bucketSeconds,
  label,
}: {
  points: readonly TrendPoint[];
  height: number;
  bucketSeconds: number;
  label: string;
}) {
  const g = sparklineGeometry(points, height, bucketSeconds);
  if (g.points === 0) return null;
  return (
    <div data-sparkline={g.points} role="img" aria-label={label} title={label} style={{ position: 'relative', height, width: '100%', flex: '0 0 auto' }}>
      <svg viewBox={`0 0 ${VIEW_WIDTH} ${height}`} preserveAspectRatio="none" width="100%" height={height} style={{ display: 'block', overflow: 'visible' }} aria-hidden="true">
        {g.points > 1 ? (
          <path d={g.d} fill="none" stroke={css('muted-foreground')} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
        ) : null}
      </svg>
      {g.end ? (
        <span
          data-sparkline-end=""
          style={{
            position: 'absolute',
            left: `${g.end.left}%`,
            top: `${g.end.top}%`,
            width: 6,
            height: 6,
            marginLeft: -3,
            marginTop: -3,
            borderRadius: '50%',
            background: css('muted-foreground'),
          }}
        />
      ) : null}
    </div>
  );
}
