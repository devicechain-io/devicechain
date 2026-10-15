// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-count — a KPI card of how many alarms match the widget's scope+filter
// (ADR-041). The number is the server total (past the loaded page); its color is the
// most severe among the CURRENTLY-LOADED alarms (the newest page), and the worst
// severity is also spelled out as text/aria so the signal never reads on hue alone (a
// color-blind or screen-reader viewer still gets it). At zero it stays muted.

import {
  type AlarmStreamState,
  css,
  defineWidget,
  fitValueFont,
  fitValueFontCss,
  FIT_MIN_FONT_PX,
  fontVar,
  optString,
  spaceVar,
  useSeverityColors,
  useStickyChars,
  useWidgetScale,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { highestSeverity, severityColor, severityLabel } from './severity';

// The caption under the count (11 px text plus its 6 px margin). It always shows -- it names
// the worst severity -- so the count is fitted to what is left rather than pushing it out.
export const ALARM_COUNT_CAPTION_ROOM_PX = 20;

// Rendered INSIDE the frame: the widget scale is the frame's content column, which is only in
// context below the frame.
function AlarmCountBody({ data }: { data: AlarmStreamState }) {
  const { size, scale } = useWidgetScale();
  const severity = useSeverityColors();
  const { total, alarms, loading } = data;

  const worst = highestSeverity(alarms.map((a) => a.severity));
  // At zero (or before any alarm is loaded) stay neutral; otherwise take the worst
  // loaded severity's color as the alert accent.
  const alerting = total > 0 && worst != null;
  const color = alerting ? severityColor(severity, worst) : css('muted-foreground');

  const display = loading && total === 0 ? '—' : String(total);
  const chars = useStickyChars(display.length);
  const floor = Math.round(FIT_MIN_FONT_PX * scale);
  const measured = size.width > 0 && size.height > 0;
  const valueFont = measured
    ? fitValueFont(
        { width: size.width, height: Math.max(1, size.height - ALARM_COUNT_CAPTION_ROOM_PX * scale) },
        chars,
        { floor },
      )
    : // First frame: the same curve in cq units, resolved by the browser against the frame's
      // column (it does not take the caption's room off; the first measure does).
      fitValueFontCss(chars, { floor });
  const unit = total === 1 ? 'alarm' : 'alarms';
  const ariaLabel = alerting ? `${total} ${unit}, worst severity ${severityLabel(worst)}` : `${total} ${unit}`;

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'safe center',
        alignItems: 'center',
        height: '100%',
        boxSizing: 'border-box',
        overflow: 'hidden',
        textAlign: 'center',
      }}
    >
      <div
        aria-label={ariaLabel}
        style={{
          fontSize: valueFont,
          fontWeight: 700,
          lineHeight: 1,
          whiteSpace: 'nowrap',
          fontVariantNumeric: 'tabular-nums',
          color,
        }}
      >
        {display}
      </div>
      <div
        style={{
          fontSize: fontVar('xs'),
          color: css('muted-foreground'),
          marginTop: spaceVar(1),
          textTransform: 'uppercase',
          letterSpacing: '0.05em',
        }}
      >
        <span>{unit}</span>
        {alerting ? <span>{` · worst ${severityLabel(worst)}`}</span> : null}
      </div>
    </div>
  );
}

export function AlarmCount({ widget, data }: WidgetProps<AlarmStreamState>) {
  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <AlarmCountBody data={data} />
    </WidgetFrame>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const alarmCountWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['alarm-count'],
  implementation: { kind: 'trusted-react', component: AlarmCount },
});
