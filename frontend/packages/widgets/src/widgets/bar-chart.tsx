// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// bar-chart -- one bar per measurement, each as tall as that measurement's latest reading. The
// categories are the measurements the datasource names (in that order, or sorted by value); one
// that has not reported yet keeps its label with no bar rather than vanishing or reading zero.
//
// Drawn as SVG with its text as DOM text nodes, not on an ECharts canvas: no chart type to
// register (a bar or pie type costs ~7 KB gzip each, and a second lazily loaded ECharts module
// makes the app bundler split the whole chart engine out of the entry chunk, which cost more than
// the types themselves), and the readability gate can measure the labels.

import { useMemo } from 'react';

import {
  css,
  formatValue,
  optNumber,
  optString,
  WidgetFrame,
  type WidgetProps,
  type WidgetScale,
} from '@devicechain/widget-sdk';

import { aggregateStrings } from '../aggregate-strings';
import { categoryItems, parseSort } from '../category-items';
import { deviceBars } from '../fleet-model';
import { fleetStrings } from '../fleet-strings';
import { niceDomain } from '../category-geometry';
import { LevelEmpty } from '../level-readout';
import { Explained } from '../no-reading';

// The width of a character of the chart font, as a share of its size: an estimate for the room a
// label needs. Wide enough for digits and capitals, so a label is trimmed rather than overlapped.
const CHAR_EM = 0.6;
// Past this many categories the value labels on the bars would collide.
export const MAX_LABELLED_BARS = 12;
// The thickest a bar is drawn, in px at density 1: a single bar does not fill the chart.
const MAX_BAR_PX = 56;

// A chart of one bar per device cannot label hundreds of bars; the first MAX_DEVICE_BARS in the
// chosen order are drawn, and the chart SAYS how many devices there are (see deviceItems).
export const MAX_DEVICE_BARS = 100;

export function BarChart({ widget, data }: WidgetProps) {
  const sort = parseSort(optString(widget.options, 'sort'));
  const perDevice = optString(widget.options, 'groupBy') === 'device';
  const measurement = optString(widget.options, 'measurement') ?? widget.datasource?.measurements[0];
  const items = useMemo(
    () => (perDevice ? [] : categoryItems(widget, data, sort)),
    [widget, data, sort, perDevice],
  );
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {(ws) => {
        if (perDevice) {
          return <DeviceBars data={data} measurement={measurement} sort={sort} unit={unit} precision={precision} ws={ws} />;
        }
        return items.length === 0 ? (
          <LevelEmpty message={aggregateStrings().noCategories} />
        ) : (
          <Bars items={items} unit={unit} precision={precision} ws={ws} />
        );
      }}
    </WidgetFrame>
  );
}

// One bar per device of the anchor, for one measurement: the same chart, with the fleet's members as
// its categories. Every way the fleet can fail to be a chart is a sentence, and a fleet larger than
// what is drawn is counted in the chart itself, where it cannot be dropped with a heading.
function DeviceBars({
  data,
  measurement,
  sort,
  unit,
  precision,
  ws,
}: {
  data: WidgetProps['data'];
  measurement: string | undefined;
  sort: ReturnType<typeof parseSort>;
  unit: string | undefined;
  precision: number | undefined;
  ws: WidgetScale;
}) {
  const strings = fleetStrings();
  const fleet = data.members;
  const bars = useMemo(
    () => (fleet && measurement ? deviceBars(fleet.members, measurement, sort) : []),
    [fleet, measurement, sort],
  );
  if (!measurement) return <LevelEmpty message={strings.noMeasurement} />;
  if (!fleet || fleet.status === 'loading') return <LevelEmpty message={strings.loading} />;
  if (fleet.status === 'unbound') return <Explained kind="unbound" message={strings.unbound} />;
  if (fleet.status === 'forbidden') return <Explained kind="forbidden" message={strings.forbidden} />;
  if (fleet.status === 'unsupported') return <LevelEmpty message={strings.unsupported} />;
  if (bars.length === 0) return <LevelEmpty message={strings.noMembers} />;
  const drawn = bars.slice(0, MAX_DEVICE_BARS);
  // Counted against the anchor, not only against this chart's own cap: a fleet the resolver read
  // only a page of is just as cut off as one this chart trimmed.
  const note = fleet.truncated || drawn.length < bars.length ? strings.truncated(drawn.length, Math.max(fleet.total, bars.length)) : undefined;
  return (
    <Bars
      items={drawn.map((b) => ({ label: b.label, key: b.token, value: b.value }))}
      unit={unit}
      precision={precision}
      ws={ws}
      note={note}
    />
  );
}

// "abcdefgh" fitted to `chars` characters: cut and ended with an ellipsis. The full text stays in
// the element's <title>, so nothing is lost to the trim.
export function fitLabel(label: string, chars: number): string {
  if (chars < 2) return '';
  return label.length <= chars ? label : `${label.slice(0, Math.max(1, chars - 1))}…`;
}

function Bars({
  items,
  unit,
  precision,
  ws,
  note,
}: {
  items: { label: string; value: number | null; key?: string }[];
  unit: string | undefined;
  precision: number | undefined;
  ws: WidgetScale;
  // A line along the bottom of the chart: how much of the fleet the bars are.
  note?: string;
}) {
  const W = ws.size.width > 0 ? ws.size.width : 240;
  const fullH = ws.size.height > 0 ? ws.size.height : 160;
  const noteH = note ? ws.px('font-xs') * 1.8 : 0;
  const H = Math.max(1, fullH - noteH);
  // Columns in a wide chart, rows in a tall one: rows leave a long measurement name its full width.
  const horizontal = H > W;
  const font = ws.px('font-xs');
  const pad = ws.px('space-1');
  const charPx = font * CHAR_EM;
  const fmt = (v: number) => formatValue(v, precision);
  const withUnit = (v: number) => (unit ? `${fmt(v)} ${unit}` : fmt(v));
  const domain = niceDomain(items.flatMap((i) => (i.value === null ? [] : [i.value])));
  const span = domain.max - domain.min;
  const few = items.length <= MAX_LABELLED_BARS;
  const widest = (texts: string[]) => texts.reduce((n, t) => Math.max(n, t.length), 0) * charPx;
  const tickText = domain.ticks.map(fmt);

  // The plot box. Vertical: value ticks down the left, category labels along the bottom, room
  // above for the unit and the tallest bar's value. Horizontal: category labels down the left,
  // value ticks along the bottom, room to the right for the longest bar's value.
  const catLabelH = font * 1.7;
  const topH = font * 1.7;
  let x0: number, x1: number, y0: number, y1: number;
  if (horizontal) {
    x0 = Math.min(W * 0.4, widest(items.map((i) => i.label)) + pad * 2);
    x1 = W - pad - (few ? widest(items.map((i) => (i.value === null ? '' : withUnit(i.value)))) + pad : pad);
    y0 = pad;
    y1 = H - catLabelH;
  } else {
    x0 = widest(tickText) + pad * 2;
    x1 = W - pad;
    y0 = topH;
    y1 = H - catLabelH;
  }
  const length = horizontal ? Math.max(0, x1 - x0) : Math.max(0, y1 - y0);
  // A value's distance along the value axis from the plot's low end (left, or bottom).
  const along = (v: number) => ((v - domain.min) / span) * length;
  const bandPx = (horizontal ? y1 - y0 : x1 - x0) / items.length;
  // Each bar's value is printed beside it when the labels fit: no more than MAX_LABELLED_BARS, and,
  // in columns, the widest value no wider than a column (side by side they would run together).
  // Otherwise every value is still in the chart's accessible description and on each bar's title.
  const labelled =
    few &&
    (horizontal || widest(items.map((i) => (i.value === null ? '' : fmt(i.value)))) <= bandPx);
  const thick = Math.min(bandPx * 0.62, MAX_BAR_PX * ws.scale);
  const muted = { fill: css('muted-foreground') };
  const text = { fontSize: font, fontVariantNumeric: 'tabular-nums' } as const;
  const zero = along(0);

  return (
    <svg
      data-bar-chart={horizontal ? 'horizontal' : 'vertical'}
      width={W}
      height={H}
      viewBox={`0 0 ${W} ${H}`}
      role="img"
      aria-label={items.map((i) => `${i.label}: ${i.value === null ? '—' : withUnit(i.value)}`).join(', ')}
      style={{ display: 'block' }}
    >
      {note ? (
        <text data-bar-note="" x={pad} y={fullH - font * 0.4} style={{ ...text, ...muted }}>
          {note}
        </text>
      ) : null}
      {unit ? (
        <text
          x={horizontal ? W - pad : pad}
          y={horizontal ? H - pad : font}
          textAnchor={horizontal ? 'end' : 'start'}
          style={{ ...text, ...muted }}
        >
          {unit}
        </text>
      ) : null}
      {domain.ticks.map((t, i) => {
        const d = along(t);
        return horizontal ? (
          <g key={t}>
            <line x1={x0 + d} x2={x0 + d} y1={y0} y2={y1} style={{ stroke: css('border') }} />
            <text x={x0 + d} y={y1 + font * 1.2} textAnchor="middle" style={{ ...text, ...muted }}>
              {tickText[i]}
            </text>
          </g>
        ) : (
          <g key={t}>
            <line x1={x0} x2={x1} y1={y1 - d} y2={y1 - d} style={{ stroke: css('border') }} />
            <text x={x0 - pad} y={y1 - d + font * 0.35} textAnchor="end" style={{ ...text, ...muted }}>
              {tickText[i]}
            </text>
          </g>
        );
      })}
      {items.map((item, i) => {
        const centre = (horizontal ? y0 : x0) + bandPx * (i + 0.5);
        const label = fitLabel(item.label, Math.floor((horizontal ? x0 - pad : bandPx) / charPx));
        const v = item.value;
        const end = v === null ? zero : along(v);
        const lo = Math.min(zero, end);
        const hi = Math.max(zero, end);
        return (
          <g key={item.key ?? item.label} data-bar={item.label} data-value={v ?? undefined}>
            <title>{`${item.label}: ${v === null ? '—' : withUnit(v)}`}</title>
            {v === null ? null : horizontal ? (
              <rect x={x0 + lo} y={centre - thick / 2} width={Math.max(1, hi - lo)} height={thick} style={{ fill: css('chart-1') }} />
            ) : (
              <rect x={centre - thick / 2} y={y1 - hi} width={thick} height={Math.max(1, hi - lo)} style={{ fill: css('chart-1') }} />
            )}
            {labelled && v !== null ? (
              horizontal ? (
                <text x={x0 + hi + pad} y={centre + font * 0.35} style={{ ...text, fill: css('foreground') }}>
                  {withUnit(v)}
                </text>
              ) : (
                <text x={centre} y={y1 - hi - pad} textAnchor="middle" style={{ ...text, fill: css('foreground') }}>
                  {fmt(v)}
                </text>
              )
            ) : null}
            <text
              x={horizontal ? x0 - pad : centre}
              y={horizontal ? centre + font * 0.35 : y1 + font * 1.2}
              textAnchor={horizontal ? 'end' : 'middle'}
              style={{ ...text, ...muted }}
            >
              {label}
            </text>
          </g>
        );
      })}
    </svg>
  );
}
