// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// latest-card — the current value of one measurement, big and legible.

import { flashTextStyle, useFlashOnChange } from '../flash';
import { formatTimestamp, formatValue } from '../format';
import { WidgetFrame } from '../frame';
import { useElementSize } from '../hooks';
import { css } from '../theme';
import {
  optBoolean,
  optNumber,
  optString,
  pickSample,
  primaryMeasurementName,
  type WidgetProps,
} from '../widget';

const clamp = (v: number, lo: number, hi: number): number => Math.max(lo, Math.min(hi, v));

export function LatestCard({ widget, data }: WidgetProps) {
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const title = optString(widget.options, 'title') ?? name;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const display = formatValue(sample?.value, precision);

  // Opt-in directional flash: tint the number green/red on a rise/fall, then fade back. The
  // identity (device + measurement) suppresses a false flash when an anchor's single slot
  // interleaves values across member devices, or the shown measurement switches.
  const flashDirection = useFlashOnChange(
    sample?.value,
    optBoolean(widget.options, 'flashOnChange'),
    sample && `${sample.deviceToken}:${sample.name}`,
  );

  // Scale the value to the card so it doesn't overflow a small slot (falls back to
  // a comfortable size before first measure / in a non-DOM test).
  const valueFont = size.height > 0 ? clamp(Math.round(size.height / 3.5), 16, 44) : 32;

  return (
    <WidgetFrame title={title}>
      <div
        ref={sizeRef}
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'center',
          height: '100%',
          padding: '12px 16px',
        }}
      >
        <div
          style={{
            fontSize: valueFont,
            fontWeight: 600,
            lineHeight: 1.1,
            color: css('foreground'),
            ...flashTextStyle(flashDirection),
          }}
        >
          {display}
          {sample?.value != null && unit ? (
            <span style={{ fontSize: Math.round(valueFont / 2), color: css('muted-foreground'), marginLeft: 6 }}>
              {unit}
            </span>
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
