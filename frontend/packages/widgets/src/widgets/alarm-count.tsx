// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-count — a KPI card of how many alarms match the widget's scope+filter
// (ADR-041). The number is the server total (past the loaded page), and it takes the
// color of the most severe alarm currently loaded, so an at-a-glance dashboard tile
// goes red when something critical is open and stays muted at zero.

import { WidgetFrame } from '../frame';
import type { AlarmStreamState } from '../hooks';
import { useElementSize } from '../hooks';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';
import { highestSeverity, severityColor } from './severity';

const clamp = (v: number, lo: number, hi: number): number => Math.max(lo, Math.min(hi, v));

export function AlarmCount({ widget, data }: WidgetProps<AlarmStreamState>) {
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const { total, alarms, loading } = data;

  const worst = highestSeverity(alarms.map((a) => a.severity));
  // At zero (or before any alarm is loaded) stay neutral; otherwise take the worst
  // loaded severity's color as the alert accent.
  const color = total > 0 && worst ? severityColor(worst) : css('muted-foreground');

  const valueFont = size.height > 0 ? clamp(Math.round(size.height / 2.8), 20, 56) : 40;
  const display = loading && total === 0 ? '—' : String(total);

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
        <div style={{ fontSize: valueFont, fontWeight: 700, lineHeight: 1, color }}>{display}</div>
        <div style={{ fontSize: 11, color: css('muted-foreground'), marginTop: 6, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
          {total === 1 ? 'alarm' : 'alarms'}
        </div>
      </div>
    </WidgetFrame>
  );
}
