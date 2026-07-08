// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-table — the raised alarms in the widget's scope (ADR-041), newest first, with
// a severity stripe per row. When the runtime supplies an action seam AND the viewer
// holds alarm:write, each active row gets Acknowledge / Clear controls; otherwise the
// table is read-only. Bound through the hub's alarm channel, so it renders identically
// from live data or the synthetic preview source.

import type { AlarmRow, WidgetActions } from '@devicechain/dashboards';
import { useState, type CSSProperties } from 'react';

import type { AlarmStreamState } from '../hooks';
import { formatDateTime, formatValue } from '../format';
import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optNumber, optString, type WidgetProps } from '../widget';
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

// statusLabel collapses the 4-state model (state × acknowledged) into the one signal an
// operator reads, matching the console's alarm badges: cleared wins regardless of the
// acknowledged flag; an active alarm is "Acknowledged" once owned, else "Active".
function statusLabel(alarm: AlarmRow): string {
  if (alarm.state === 'CLEARED') return 'Cleared';
  return alarm.acknowledged ? 'Acknowledged' : 'Active';
}

export function AlarmTable({ widget, data, actions }: WidgetProps<AlarmStreamState>) {
  const { alarms, total, loading } = data;
  // Actions render only when the runtime supplies a seam AND the viewer may write.
  const canAct = actions?.can('alarm:write') ?? false;
  const columns = canAct ? 8 : 7;
  // Round the triggering value to a fixed precision when configured; unset keeps the raw
  // value (formatValue's default), matching the table widget. An alarm's raw lastValue is
  // otherwise a full-width float (e.g. 31.19073834583732).
  const precision = optNumber(widget.options, 'precision');

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
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
                <th style={{ ...headCell, width: 4, padding: 0 }} aria-hidden="true" />
                <th scope="col" style={headCell}>
                  Severity
                </th>
                <th scope="col" style={headCell}>
                  Status
                </th>
                <th scope="col" style={headCell}>
                  Originator
                </th>
                <th scope="col" style={headCell}>
                  Alarm
                </th>
                <th scope="col" style={{ ...headCell, textAlign: 'right' }}>
                  Value
                </th>
                <th scope="col" style={headCell}>
                  Raised
                </th>
                {canAct ? (
                  <th scope="col" style={{ ...headCell, textAlign: 'right' }}>
                    Actions
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {alarms.length === 0 ? (
                <tr>
                  <td colSpan={columns} style={{ ...cell, textAlign: 'center', color: css('muted-foreground') }}>
                    {loading ? 'Loading…' : 'No alarms'}
                  </td>
                </tr>
              ) : (
                alarms.map((alarm) => {
                  const color = severityColor(alarm.severity);
                  return (
                    <tr key={alarm.token} style={{ borderBottom: `1px solid ${css('border')}` }}>
                      <td aria-hidden="true" style={{ padding: 0, width: 4, background: color }} />
                      <td style={cell}>
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                          <span
                            aria-hidden="true"
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
                      <td style={{ ...cell, textAlign: 'right' }}>{formatValue(alarm.lastValue, precision)}</td>
                      <td style={{ ...cell, color: css('muted-foreground') }}>
                        {formatDateTime(alarm.raisedTime)}
                      </td>
                      {canAct ? (
                        <td style={{ ...cell, textAlign: 'right' }}>
                          {/* actions is defined whenever canAct is true. */}
                          <AlarmRowActions alarm={alarm} actions={actions as WidgetActions} />
                        </td>
                      ) : null}
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
        {total > alarms.length ? (
          <div
            style={{
              padding: '4px 10px',
              fontSize: 11,
              color: css('muted-foreground'),
              borderTop: `1px solid ${css('border')}`,
            }}
          >
            Showing {alarms.length} of {total}
          </div>
        ) : null}
      </div>
    </WidgetFrame>
  );
}

const actionButton: CSSProperties = {
  padding: '2px 8px',
  fontSize: 12,
  lineHeight: 1.4,
  borderRadius: 4,
  border: `1px solid ${css('border')}`,
  background: css('card'),
  color: css('foreground'),
  cursor: 'pointer',
};

// AlarmRowActions owns the pending/error state for one row's Acknowledge / Clear. Only
// an ACTIVE alarm is actionable (a cleared — or any future non-active — state shows
// "—"); an acknowledged one still shows Clear. After a successful mutation the hub
// reconciles the table, so the row updates itself.
function AlarmRowActions({ alarm, actions }: { alarm: AlarmRow; actions: WidgetActions }) {
  const [pending, setPending] = useState<'ack' | 'clear' | null>(null);
  const [failed, setFailed] = useState(false);

  if (alarm.state !== 'ACTIVE') {
    return <span style={{ color: css('muted-foreground') }}>—</span>;
  }

  const run = (kind: 'ack' | 'clear', op: () => Promise<void>) => {
    setPending(kind);
    setFailed(false);
    // Call op() synchronously (so an immediate dispatch is observable) but guard a
    // synchronous throw too, so a misbehaving seam can't leave the row stuck-disabled.
    let promise: Promise<void>;
    try {
      promise = op();
    } catch {
      setFailed(true);
      setPending(null);
      return;
    }
    promise.catch(() => setFailed(true)).finally(() => setPending(null));
  };

  return (
    <span style={{ display: 'inline-flex', gap: 6, justifyContent: 'flex-end', alignItems: 'center' }}>
      {failed ? (
        <span
          role="img"
          aria-label="Action failed — retry"
          title="Action failed — retry"
          style={{ color: severityColor('CRITICAL'), fontSize: 12 }}
        >
          ⚠
        </span>
      ) : null}
      {!alarm.acknowledged ? (
        <button
          type="button"
          style={{ ...actionButton, opacity: pending ? 0.6 : 1 }}
          disabled={pending !== null}
          aria-busy={pending === 'ack'}
          aria-label={`Acknowledge alarm ${alarm.alarmKey}`}
          onClick={() => run('ack', () => actions.acknowledgeAlarm(alarm.token))}
        >
          {pending === 'ack' ? '…' : 'Ack'}
        </button>
      ) : null}
      <button
        type="button"
        style={{ ...actionButton, opacity: pending ? 0.6 : 1 }}
        disabled={pending !== null}
        aria-busy={pending === 'clear'}
        aria-label={`Clear alarm ${alarm.alarmKey}`}
        onClick={() => run('clear', () => actions.clearAlarm(alarm.token))}
      >
        {pending === 'clear' ? '…' : 'Clear'}
      </button>
    </span>
  );
}
