// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// latest-card — the current value of one measurement, big and legible.

import { formatTimestamp, formatValue } from '../format';
import { WidgetFrame } from '../frame';
import { css } from '../theme';
import { optNumber, optString, pickSample, primaryMeasurementName, type WidgetProps } from '../widget';

export function LatestCard({ widget, data }: WidgetProps) {
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const title = optString(widget.options, 'title') ?? name;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const display = formatValue(sample?.value, precision);

  return (
    <WidgetFrame title={title}>
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'center',
          height: '100%',
          padding: '12px 16px',
        }}
      >
        <div style={{ fontSize: 32, fontWeight: 600, lineHeight: 1.1, color: css('foreground') }}>
          {display}
          {sample?.value != null && unit ? (
            <span style={{ fontSize: 16, color: css('muted-foreground'), marginLeft: 6 }}>{unit}</span>
          ) : null}
        </div>
        {sample?.occurredTime ? (
          <div style={{ fontSize: 11, color: css('muted-foreground'), marginTop: 4 }}>
            {formatTimestamp(sample.occurredTime)}
          </div>
        ) : null}
      </div>
    </WidgetFrame>
  );
}
