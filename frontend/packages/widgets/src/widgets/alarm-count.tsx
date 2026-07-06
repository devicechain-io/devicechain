// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-count — a KPI card of how many alarms match the widget's scope+filter
// (ADR-041). The number is the server total (past the loaded page); its color is the
// most severe among the CURRENTLY-LOADED alarms (the newest page), and the worst
// severity is also spelled out as text/aria so the signal never reads on hue alone (a
// color-blind or screen-reader viewer still gets it). At zero it stays muted.

import { WidgetFrame } from '../frame';
import type { AlarmStreamState } from '../hooks';
import { useElementSize } from '../hooks';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';
import { highestSeverity, severityColor, severityLabel } from './severity';

const clamp = (v: number, lo: number, hi: number): number => Math.max(lo, Math.min(hi, v));

export function AlarmCount({ widget, data }: WidgetProps<AlarmStreamState>) {
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const { total, alarms, loading } = data;

  const worst = highestSeverity(alarms.map((a) => a.severity));
  // At zero (or before any alarm is loaded) stay neutral; otherwise take the worst
  // loaded severity's color as the alert accent.
  const alerting = total > 0 && worst != null;
  const color = alerting ? severityColor(worst) : css('muted-foreground');

  const valueFont = size.height > 0 ? clamp(Math.round(size.height / 2.8), 20, 56) : 40;
  const display = loading && total === 0 ? '—' : String(total);
  const unit = total === 1 ? 'alarm' : 'alarms';
  const ariaLabel = alerting ? `${total} ${unit}, worst severity ${severityLabel(worst)}` : `${total} ${unit}`;

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div
        ref={sizeRef}
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'center',
          alignItems: 'center',
          height: '100%',
          padding: '12px 16px',
          textAlign: 'center',
        }}
      >
        <div aria-label={ariaLabel} style={{ fontSize: valueFont, fontWeight: 700, lineHeight: 1, color }}>
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
