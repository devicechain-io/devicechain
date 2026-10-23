// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// timeseries-chart — a live line chart of the rolling measurement window, one
// series per measurement name. The window is seeded from bucketedMeasurements
// history (via useMeasurementStream's initialSamples) and extended by the live tail. An
// interruption of the live feed that history has not filled yet is drawn as a break in the line.

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
  pickSample,
  primaryMeasurementName,
  spaceVar,
  useWidgetScale,
  useWidgetTimeRange,
  useWidgetTimeZone,
  viewerLocale,
  WidgetFrame,
  type MeasurementStreamState,
  type WidgetProps,
} from '@devicechain/widget-sdk';
import { buildLineOption, EChart, loadChartEngine, useChartTheme } from '@devicechain/widget-sdk/echarts';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { NoReading, useSettledEmpty } from './no-reading-lazy';
import type { EventMarkersView } from '../event-markers-types';

// `markers` is supplied by the event-marker feed (a chart with markAlarms / markCommands on) and by
// nothing else; without it the chart is drawn exactly as it always was.
type TimeSeriesProps = WidgetProps & { markers?: EventMarkersView };

export function TimeSeriesChart({ widget, data, markers }: TimeSeriesProps) {
  const range = useWidgetTimeRange();
  const pinned = optNumber(widget.options, 'windowSeconds');
  const spanSeconds = isValidSpanSeconds(pinned) ? pinned : range?.spanSeconds;
  // The frame's "Download CSV": the samples on screen, nothing re-read. The builder is its own chunk, so
  // the chart carries only this closure.
  const exportRows = () => {
    const device = widget.datasource?.kind === 'device' ? widget.datasource.deviceToken : undefined;
    return import('../export-rows').then((m) =>
      m.samplesExport({
        subject: optString(widget.options, 'title') ?? device ?? 'chart',
        samples: data.samples,
        unit: optString(widget.options, 'unit'),
        precision: optNumber(widget.options, 'precision'),
        seriesIs: 'measurement',
        spanSeconds,
      }),
    );
  };
  return (
    <WidgetFrame title={optString(widget.options, 'title')} flush exportRows={exportRows}>
      <TimeSeriesBody widget={widget} data={data} markers={markers} />
    </WidgetFrame>
  );
}

// Rendered INSIDE the frame: the widget scale is the frame's content column, which is only in
// context below the frame.
function TimeSeriesBody({ widget, data, markers }: TimeSeriesProps) {
  const theme = useChartTheme();
  const { size, scale } = useWidgetScale();

  const measurements = widget.datasource?.measurements;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  // The value axis's ends, when the author set them (each widens for a reading beyond it).
  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const option = useMemo(() => {
    const base = buildLineOption(data.samples, theme, { measurements, unit, precision, thresholds, scale, gaps: data.gaps, min, max }, size);
    return markers ? markers.decorate(base, size) : base;
  }, [data.samples, data.gaps, theme, measurements, unit, precision, thresholds, scale, size, markers, min, max]);

  // Nothing in the window: say why (after the history read has had its moment), with the last reading
  // known outside it as a hint.
  const name = primaryMeasurementName(widget);
  const explain = useSettledEmpty(data.samples.length === 0);
  if (explain) {
    return (
      <NoReading
        unbound={widget.datasource?.kind === 'unbound'}
        measurement={name}
        lastSample={pickSample(data.latest, name)}
        unit={unit}
        precision={precision}
      />
    );
  }

  const chart = (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      <EChart option={option} />
      <SpanBadge widget={widget} data={data} />
    </div>
  );
  if (!markers) return chart;
  // The list and notes sit UNDER the chart, never over it: the chart gives up their height.
  return (
    <div style={{ display: 'flex', flexDirection: 'column', width: '100%', height: '100%' }}>
      <div style={{ flex: 1, minHeight: 0 }}>{chart}</div>
      {markers.footer}
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

// What preloadWidgets calls: the chart engine loads on demand, as its own chunk, so a host that
// awaits this for a board holding this widget draws the chart on the board's first frame.
TimeSeriesChart.preload = loadChartEngine;

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const timeSeriesChartWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['timeseries-chart'],
  implementation: { kind: 'trusted-react', component: TimeSeriesChart },
});
