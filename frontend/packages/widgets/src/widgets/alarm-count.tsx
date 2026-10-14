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
  optString,
  useElementSize,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { highestSeverity, severityColor, severityLabel } from './severity';

const PAD_X = 16;
const PAD_Y = 12;
// The caption under the count (11 px text plus its 6 px margin). It always shows -- it names
// the worst severity -- so the count is fitted to what is left rather than pushing it out.
export const ALARM_COUNT_CAPTION_ROOM_PX = 20;

export function AlarmCount({ widget, data }: WidgetProps<AlarmStreamState>) {
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const { total, alarms, loading } = data;

  const worst = highestSeverity(alarms.map((a) => a.severity));
  // At zero (or before any alarm is loaded) stay neutral; otherwise take the worst
  // loaded severity's color as the alert accent.
  const alerting = total > 0 && worst != null;
  const color = alerting ? severityColor(worst) : css('muted-foreground');

  const display = loading && total === 0 ? '—' : String(total);
  // Unmeasured stays {0,0} so the curve's fallback applies; a measured box never drops to 0.
  const valueFont = fitValueFont(
    size.height > 0 ? { width: size.width, height: Math.max(1, size.height - ALARM_COUNT_CAPTION_ROOM_PX) } : size,
    display.length,
  );
  const unit = total === 1 ? 'alarm' : 'alarms';
  const ariaLabel = alerting ? `${total} ${unit}, worst severity ${severityLabel(worst)}` : `${total} ${unit}`;

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        ref={sizeRef}
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'safe center',
          alignItems: 'center',
          height: '100%',
          boxSizing: 'border-box',
          overflow: 'hidden',
          padding: `${PAD_Y}px ${PAD_X}px`,
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
            fontSize: 11,
            color: css('muted-foreground'),
            marginTop: 6,
            textTransform: 'uppercase',
            letterSpacing: '0.05em',
          }}
        >
          <span>{unit}</span>
          {alerting ? <span>{` · worst ${severityLabel(worst)}`}</span> : null}
        </div>
      </div>
    </WidgetFrame>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const alarmCountWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['alarm-count'],
  implementation: { kind: 'trusted-react', component: AlarmCount },
});
