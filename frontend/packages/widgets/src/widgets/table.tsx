// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// table — the latest value of every measurement the datasource carries, one row
// per measurement name.

import type { CSSProperties } from 'react';

import { formatTimestamp, formatValue } from '../format';
import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optNumber, optString, type WidgetProps } from '../widget';

const cell: CSSProperties = { padding: '6px 12px', textAlign: 'left' };
const headCell: CSSProperties = {
  ...cell,
  fontSize: 11,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: css('muted-foreground'),
  borderBottom: `1px solid ${css('border')}`,
};

export function Table({ widget, data }: WidgetProps) {
  const rows = Object.values(data.latest).sort((a, b) => a.name.localeCompare(b.name));
  // Round to a fixed number of decimals when configured; unset keeps the raw value
  // (formatValue's default). A live sensor value is otherwise a wall of digits.
  const precision = optNumber(widget.options, 'precision');

  return (
    <WidgetFrame title={optString(widget.options, 'title')}>
      <div style={{ height: '100%', overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13, color: css('foreground') }}>
          <thead>
            <tr>
              <th style={headCell}>Measurement</th>
              <th style={headCell}>Value</th>
              <th style={headCell}>Time</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={3} style={{ ...cell, textAlign: 'center', color: css('muted-foreground') }}>
                  No data yet
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr key={row.name}>
                  <td style={cell}>{row.name}</td>
                  <td style={{ ...cell, fontVariantNumeric: 'tabular-nums' }}>
                    {formatValue(row.value, precision)}
                    {/* classifier is deliberately NOT shown: it is the bound metric
                        definition's internal id (ADR-016), not a user-facing label —
                        appending it rendered as spurious digits after the value. */}
                  </td>
                  <td style={{ ...cell, color: css('muted-foreground') }}>{formatTimestamp(row.occurredTime)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </WidgetFrame>
  );
}
