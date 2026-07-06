// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-table — the raised alarms in the widget's scope (ADR-041), newest first, with
// a severity stripe per row. Read-only: acknowledge/clear actions arrive with the
// widget action seam (a later slice). Bound through the hub's alarm channel, so it
// renders identically from live data or the synthetic preview source.

import type { AlarmRow } from '@devicechain/dashboards';
import type { CSSProperties } from 'react';

import type { AlarmStreamState } from '../hooks';
import { formatTimestamp, formatValue } from '../format';
import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optString, type WidgetProps } from '../widget';
import { severityColor, severityLabel } from './severity';

const cell: CSSProperties = { padding: '6px 10px', textAlign: 'left', whiteSpace: 'nowrap' };
const headCell: CSSProperties = {
  ...cell,
  position: 'sticky',
  top: 0,
  background: css('card'),
  fontSize: 11,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: css('muted-foreground'),
  borderBottom: `1px solid ${css('border')}`,
};

// statusLabel collapses the 4-state model (state × acknowledged) into one legible
// phrase — the operator cares about "is it live, and has someone seen it".
function statusLabel(alarm: AlarmRow): string {
  if (alarm.state === 'CLEARED') return alarm.acknowledged ? 'Cleared' : 'Cleared · unacked';
  return alarm.acknowledged ? 'Active · acked' : 'Active';
}

export function AlarmTable({ widget, data }: WidgetProps<AlarmStreamState>) {
  const { alarms, loading } = data;

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div style={{ height: '100%', overflow: 'auto' }}>
        <table
          style={{
            width: '100%',
            borderCollapse: 'collapse',
            fontSize: 13,
            color: css('foreground'),
            fontVariantNumeric: 'tabular-nums',
          }}
        >
          <thead>
            <tr>
              <th style={{ ...headCell, width: 4, padding: 0 }} aria-label="Severity" />
              <th style={headCell}>Severity</th>
              <th style={headCell}>Status</th>
              <th style={headCell}>Originator</th>
              <th style={headCell}>Alarm</th>
              <th style={{ ...headCell, textAlign: 'right' }}>Value</th>
              <th style={headCell}>Raised</th>
            </tr>
          </thead>
          <tbody>
            {alarms.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ ...cell, textAlign: 'center', color: css('muted-foreground') }}>
                  {loading ? 'Loading…' : 'No alarms'}
                </td>
              </tr>
            ) : (
              alarms.map((alarm) => {
                const color = severityColor(alarm.severity);
                return (
                  <tr key={alarm.token} style={{ borderBottom: `1px solid ${css('border')}` }}>
                    <td style={{ padding: 0, width: 4, background: color }} />
                    <td style={cell}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        <span
                          style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }}
                        />
                        {severityLabel(alarm.severity)}
                      </span>
                    </td>
                    <td style={{ ...cell, color: css('muted-foreground') }}>{statusLabel(alarm)}</td>
                    <td style={cell}>{alarm.originatorToken ?? alarm.originatorType}</td>
                    <td style={cell} title={alarm.message ?? undefined}>
                      {alarm.alarmKey}
                    </td>
                    <td style={{ ...cell, textAlign: 'right' }}>{formatValue(alarm.lastValue)}</td>
                    <td style={{ ...cell, color: css('muted-foreground') }}>
                      {formatTimestamp(alarm.raisedTime)}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </WidgetFrame>
  );
}
