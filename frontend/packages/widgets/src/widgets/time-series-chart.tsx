// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// timeseries-chart — a live line chart of the rolling measurement window, one
// series per measurement name. The window is seeded from bucketedMeasurements
// history (via useMeasurementStream's initialSamples) and extended by the live tail.

import { useMemo } from 'react';

import { isValidSpanSeconds, spanLabel } from '@devicechain/dashboards';
import {
  css,
  defineWidget,
  fontVar,
  formatDateTime,
  formatTimestamp,
  optNumber,
  optString,
  optThresholds,
  spaceVar,
  useWidgetScale,
  useWidgetTimeRange,
  useWidgetTimeZone,
  viewerLocale,
  WidgetFrame,
  type MeasurementStreamState,
  type WidgetProps,
} from '@devicechain/widget-sdk';
import { buildLineOption, EChart, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

export function TimeSeriesChart({ widget, data }: WidgetProps) {
  return (
    <WidgetFrame title={optString(widget.options, 'title')} flush>
      <TimeSeriesBody widget={widget} data={data} />
    </WidgetFrame>
  );
}

// Rendered INSIDE the frame: the widget scale is the frame's content column, which is only in
// context below the frame.
function TimeSeriesBody({ widget, data }: WidgetProps) {
  const theme = useChartTheme();
  const { size, scale } = useWidgetScale();

  const measurements = widget.datasource?.measurements;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const option = useMemo(
    () => buildLineOption(data.samples, theme, { measurements, unit, precision, thresholds, scale }, size),
    [data.samples, theme, measurements, unit, precision, thresholds, scale, size],
  );

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      <EChart option={option} />
      <SpanBadge widget={widget} data={data} />
    </div>
  );
}

// The words the span badge prints. The widget packages carry no i18n runtime (see level-strings).
const SPAN_STRINGS = {
  en: {
    from: (time: string) => `from ${time}`,
    pinned: (span: string) => `Pinned to ${span}: the board's time range does not apply to this chart.`,
    cut: (time: string, span: string) =>
      `Showing from ${time}, not the whole ${span}: older samples were dropped to bound memory.`,
  },
  es: {
    from: (time: string) => `desde ${time}`,
    pinned: (span: string) => `Fijado a ${span}: el intervalo de tiempo del panel no se aplica a este gráfico.`,
    cut: (time: string, span: string) =>
      `Se muestra desde ${time}, no todo el intervalo de ${span}: se descartaron las muestras más antiguas para acotar la memoria.`,
  },
};

const DAY_MS = 86_400_000;

// SpanBadge says what span the chart draws whenever the board's header would say something else:
//   - a chart PINNED to its own span (`windowSeconds`) ignores the viewer's range pick, so under a
//     "24h" header a pinned hour would read as a day of data. It prints its own span ("1h").
//   - a window the retention ceiling cut (`truncatedBefore`) covers less than its span. It prints
//     where it starts ("from 14:03"), so a short window never passes for the whole range.
// Nothing shows when the chart draws exactly the board's range, or off a board (no header).
function SpanBadge({ widget, data }: { widget: WidgetProps['widget']; data: MeasurementStreamState }) {
  const range = useWidgetTimeRange();
  const timeZone = useWidgetTimeZone();
  const pinned = optNumber(widget.options, 'windowSeconds');
  const span = isValidSpanSeconds(pinned) ? pinned : range?.spanSeconds;
  const showSpan = range !== undefined && span !== range.spanSeconds;
  const cutAt = data.truncatedBefore;
  if (!showSpan && !cutAt) return null;

  const words = viewerLocale()?.toLowerCase().startsWith('es') ? SPAN_STRINGS.es : SPAN_STRINGS.en;
  const label = span === undefined ? '' : spanLabel(span);
  // A start more than a day back prints its date, or "from 09:14" would name the wrong day.
  const end = range?.paused && range.pausedAt !== undefined ? range.pausedAt : Date.now();
  const time = cutAt
    ? end - Date.parse(cutAt) > DAY_MS
      ? formatDateTime(cutAt, timeZone)
      : formatTimestamp(cutAt, timeZone)
    : '';
  const text = [showSpan ? label : '', cutAt ? words.from(time) : ''].filter(Boolean).join(' · ');
  const title = [showSpan ? words.pinned(label) : '', cutAt ? words.cut(time, label) : ''].filter(Boolean).join(' ');
  return (
    <span
      data-chart-span={showSpan ? label : ''}
      data-chart-truncated={cutAt ? '' : undefined}
      title={title}
      style={{
        position: 'absolute',
        top: spaceVar(1),
        right: spaceVar(2),
        padding: `0 ${spaceVar(1)}`,
        fontSize: fontVar('xs'),
        whiteSpace: 'nowrap',
        color: css('muted-foreground'),
        background: css('muted'),
        borderRadius: 4,
        pointerEvents: 'auto',
      }}
    >
      {text}
    </span>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const timeSeriesChartWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['timeseries-chart'],
  implementation: { kind: 'trusted-react', component: TimeSeriesChart },
});
