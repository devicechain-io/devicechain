// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// latest-card — the current value of one measurement, big and legible.

import { useMemo } from 'react';

import {
  bandAt,
  css,
  defineWidget,
  fitValueFont,
  flashTextStyle,
  formatTimestamp,
  formatValue,
  optBoolean,
  optNumber,
  optString,
  optThresholds,
  pickSample,
  primaryMeasurementName,
  useElementSize,
  useSeverityColors,
  useFlashOnChange,
  useWidgetTimeZone,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';

// The column's padding, and the room a timestamp line needs under the value. useElementSize
// reports the CONTENT box, which already excludes the padding.
const PAD_X = 16;
const PAD_Y = 12;
const TIMESTAMP_ROOM_PX = 18;

export function LatestCard({ widget, data }: WidgetProps) {
  const severity = useSeverityColors();
  const [sizeRef, size] = useElementSize<HTMLDivElement>();
  const timeZone = useWidgetTimeZone();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const title = optString(widget.options, 'title') ?? name;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const display = formatValue(sample?.value, precision);
  // The value reads in the colour of the band it falls in; below the lowest threshold (or with
  // none) it keeps the foreground. The same severity colours the chart widgets resolve.
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const band = bandAt(thresholds, sample?.value);
  const valueColor = band ? severity[band.level] : css('foreground');

  // Opt-in directional flash: tint the number green/red on a rise/fall, then fade back. The
  // identity (device + measurement) suppresses a false flash when an anchor's single slot
  // interleaves values across member devices, or the shown measurement switches.
  const flashDirection = useFlashOnChange(
    sample?.value,
    optBoolean(widget.options, 'flashOnChange'),
    sample && `${sample.deviceToken}:${sample.name}`,
  );

  // Fit the value to the card from both dimensions and the string length (the unit renders at
  // half size, so it counts for half its characters). Falls back to a comfortable size before
  // first measure / in a non-DOM test.
  const chars = display.length + (sample?.value != null && unit ? Math.ceil(unit.length / 2) + 1 : 0);
  const valueFont = fitValueFont(size, chars);
  // The timestamp is the first thing to give: it shows only when the measured box has room
  // for it under the value (unmeasured -- first frame, jsdom -- it shows).
  const showTimestamp =
    size.height <= 0 || size.height - Math.ceil(valueFont * 1.1) >= TIMESTAMP_ROOM_PX;

  return (
    <WidgetFrame title={title} sampleTime={sample?.occurredTime ?? null}>
      <div
        ref={sizeRef}
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'safe center',
          height: '100%',
          boxSizing: 'border-box',
          overflow: 'hidden',
          padding: `${PAD_Y}px ${PAD_X}px`,
        }}
      >
        <div
          data-threshold-level={band?.level}
          title={band?.label}
          style={{
            fontSize: valueFont,
            fontWeight: 600,
            lineHeight: 1.1,
            // One line, and digits of one width: the fit counts characters, so a live value
            // must not wrap or shift sideways as its digits change.
            whiteSpace: 'nowrap',
            fontVariantNumeric: 'tabular-nums',
            color: valueColor,
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
        {sample?.occurredTime && showTimestamp ? (
          <div style={{ fontSize: 11, color: css('muted-foreground'), marginTop: 4 }}>
            {formatTimestamp(sample.occurredTime, timeZone)}
          </div>
        ) : null}
      </div>
    </WidgetFrame>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const latestCardWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['latest-card'],
  implementation: { kind: 'trusted-react', component: LatestCard },
});
