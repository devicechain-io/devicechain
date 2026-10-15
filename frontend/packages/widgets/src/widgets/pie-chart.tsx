// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// pie-chart -- each measurement's share of the total of their latest readings. A pie is shares of
// one whole, so it is only drawn for values of zero or more that add to something: a negative
// value, or all zeros, is an explained state, never a pie that quietly means something else.
//
// Drawn as SVG wedges with a DOM legend that names each category, its value and its share (so
// every figure is on screen with no pointer to hover, and a category with no reading reads "—",
// not 0 %): no
// chart type to register (see bar-chart.tsx), and the labels are text the readability gate sees.

import { useMemo } from 'react';

import { css, formatValue, optNumber, optString, fontVar, spaceVar, WidgetFrame, type WidgetProps } from '@devicechain/widget-sdk';

import { aggregateStrings } from '../aggregate-strings';
import { categoryItems, parseSort, pieProblem } from '../category-items';
import { pieSlices, wedgePath } from '../category-geometry';
import { LevelEmpty } from '../level-readout';

// The chart ramp the wedges are drawn from, in order, repeating past five categories (the legend
// names each one, so a repeated colour is still readable).
const RAMP = ['chart-1', 'chart-2', 'chart-3', 'chart-4', 'chart-5'] as const;

export function PieChart({ widget, data }: WidgetProps) {
  const sort = parseSort(optString(widget.options, 'sort'));
  const items = useMemo(() => categoryItems(widget, data, sort), [widget, data, sort]);
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const strings = aggregateStrings();
  const problem = items.length === 0 ? undefined : pieProblem(items);
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      {items.length === 0 ? (
        <LevelEmpty message={strings.noCategories} />
      ) : problem ? (
        <LevelEmpty message={problem === 'negative' ? strings.pieNegative : problem === 'noShare' ? strings.pieNoShare : strings.noData} />
      ) : (
        <Pie items={items} unit={unit} precision={precision} />
      )}
    </WidgetFrame>
  );
}

function Pie({
  items,
  unit,
  precision,
}: {
  items: { label: string; value: number | null }[];
  unit: string | undefined;
  precision: number | undefined;
}) {
  const slices = pieSlices(items.map((i) => i.value));
  const fmt = (v: number) => `${formatValue(v, precision)}${unit ? ` ${unit}` : ''}`;
  const percent = (fraction: number) => `${Math.round(fraction * 100)}%`;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', gap: spaceVar(1) }}>
      <svg
        data-pie=""
        viewBox="0 0 100 100"
        preserveAspectRatio="xMidYMid meet"
        role="img"
        aria-label={items.map((i, n) => `${i.label}: ${i.value === null ? '—' : fmt(i.value)} (${percent(slices[n].fraction)})`).join(', ')}
        style={{ flex: '1 1 0', minHeight: 0, width: '100%', display: 'block' }}
      >
        {slices.map((s) => {
          const d = wedgePath(50, 50, 48, s.from, s.to);
          const item = items[s.index];
          return d ? (
            <path
              key={item.label}
              data-slice={item.label}
              data-fraction={s.fraction}
              d={d}
              style={{ fill: css(RAMP[s.index % RAMP.length]), stroke: css('card'), strokeWidth: 1 }}
            >
              <title>{`${item.label}: ${item.value === null ? '—' : fmt(item.value)} (${percent(s.fraction)})`}</title>
            </path>
          ) : null;
        })}
      </svg>
      <ul
        data-pie-legend=""
        style={{
          flex: '0 1 auto',
          maxHeight: '45%',
          overflow: 'hidden',
          margin: 0,
          padding: 0,
          listStyle: 'none',
          display: 'flex',
          flexWrap: 'wrap',
          justifyContent: 'center',
          gap: `0 ${spaceVar(3)}`,
          fontSize: fontVar('xs'),
          color: css('muted-foreground'),
        }}
      >
        {items.map((item, i) => (
          <li key={item.label} title={`${item.label}: ${item.value === null ? '—' : fmt(item.value)}`} style={{ whiteSpace: 'nowrap' }}>
            <span
              aria-hidden="true"
              style={{
                display: 'inline-block',
                width: '0.7em',
                height: '0.7em',
                marginRight: '0.4em',
                borderRadius: 2,
                background: css(RAMP[i % RAMP.length]),
              }}
            />
            {item.label} {item.value === null ? '—' : `${fmt(item.value)} · ${percent(slices[i].fraction)}`}
          </li>
        ))}
      </ul>
    </div>
  );
}
