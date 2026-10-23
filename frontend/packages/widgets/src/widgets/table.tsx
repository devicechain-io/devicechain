// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// table — the latest value of every measurement the datasource carries, one row
// per measurement name.

import type { CSSProperties } from 'react';

import {
  css,
  fontVar,
  spaceVar,
  FlashValue,
  formatTimestamp,
  optBoolean,
  optNumber,
  optString,
  useWidgetTimeZone,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { NoReading } from '../no-reading';

const cell: CSSProperties = { padding: `${spaceVar(1)} ${spaceVar(3)}`, textAlign: 'left' };
const headCell: CSSProperties = {
  ...cell,
  fontSize: fontVar('xs'),
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  color: css('muted-foreground'),
  borderBottom: `1px solid ${css('border')}`,
};

export function Table({ widget, data }: WidgetProps) {
  const timeZone = useWidgetTimeZone();
  const rows = Object.values(data.latest).sort((a, b) => a.name.localeCompare(b.name));
  // Round to a fixed number of decimals when configured; unset keeps the raw value
  // (formatValue's default). A live sensor value is otherwise a wall of digits.
  const precision = optNumber(widget.options, 'precision');
  // Opt-in directional flash per row (keyed by measurement name, so each value's cue is
  // independent — see FlashValue).
  const flash = optBoolean(widget.options, 'flashOnChange');

  if (rows.length === 0) {
    const asked = widget.datasource?.measurements;
    return (
      <WidgetFrame title={optString(widget.options, 'title')}>
        <NoReading unbound={widget.datasource?.kind === 'unbound'} measurement={asked?.length === 1 ? asked[0] : undefined} precision={precision} />
      </WidgetFrame>
    );
  }

  return (
    <WidgetFrame title={optString(widget.options, 'title')} flush>
      <div style={{ height: '100%', overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: fontVar('md'), color: css('foreground') }}>
          <thead>
            <tr>
              <th style={headCell}>Measurement</th>
              <th style={headCell}>Value</th>
              <th style={headCell}>Time</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
                <tr key={row.name}>
                  <td style={cell}>{row.name}</td>
                  <td style={{ ...cell, fontVariantNumeric: 'tabular-nums' }}>
                    <FlashValue
                      value={row.value}
                      precision={precision}
                      enabled={flash}
                      identity={`${row.deviceToken}:${row.name}`}
                    />
                    {/* classifier is deliberately NOT shown: it is the bound metric
                        definition's internal id (ADR-016), not a user-facing label —
                        appending it rendered as spurious digits after the value. */}
                  </td>
                  <td style={{ ...cell, color: css('muted-foreground') }}>{formatTimestamp(row.occurredTime, timeZone)}</td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>
    </WidgetFrame>
  );
}
