// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// latest-card — the current value of one measurement, big and legible.

import { useMemo } from 'react';

import {
  bandAt,
  css,
  defineWidget,
  fitValueFont,
  fitValueFontCss,
  FIT_MIN_FONT_PX,
  flashTextStyle,
  fontVar,
  formatTimestamp,
  formatValue,
  levelColor,
  optBoolean,
  optNumber,
  optString,
  optThresholds,
  pickSample,
  primaryMeasurementName,
  spaceVar,
  useSeverityColors,
  useStickyChars,
  useFlashOnChange,
  useWidgetTimeZone,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors } from '../builtin-descriptors';
import { NoReading } from './no-reading-lazy';

// The room a timestamp line needs under the value. The frame's content column (what the
// widget scale reports) is already net of the frame's padding.
const TIMESTAMP_ROOM_PX = 18;

export function LatestCard({ widget, data }: WidgetProps) {
  const severity = useSeverityColors();
  const timeZone = useWidgetTimeZone();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);

  const title = optString(widget.options, 'title') ?? name;
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const display = formatValue(sample?.value, precision);
  // The value reads in the colour of the band it falls in; below the lowest threshold (or with
  // none, or in a `normal` band) it keeps the foreground: colour only for the abnormal.
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const band = bandAt(thresholds, sample?.value);
  const valueColor = levelColor(severity, band?.level) ?? css('foreground');

  // Opt-in directional flash: tint the number green/red on a rise/fall, then fade back. The
  // identity (device + measurement) suppresses a false flash when an anchor's single slot
  // interleaves values across member devices, or the shown measurement switches.
  const flashDirection = useFlashOnChange(
    sample?.value,
    optBoolean(widget.options, 'flashOnChange'),
    sample && `${sample.deviceToken}:${sample.name}`,
  );

  // Fit the value to the card from both dimensions and the string length (the unit renders at
  // half size, so it counts for half its characters).
  // Sticky: the longest length seen lately, so a value crossing 9.99 -> 10.01 holds its size.
  const chars = useStickyChars(display.length + (sample?.value != null && unit ? Math.ceil(unit.length / 2) + 1 : 0));
  return (
    <WidgetFrame title={title} sampleTime={sample?.occurredTime ?? null}>
      {({ size, scale }) => {
        // No reading: say why, rather than print a bare dash.
        if (sample?.value == null) {
          return <NoReading unbound={widget.datasource?.kind === 'unbound'} measurement={name} />;
        }
        // Measured: the JS curve. Unmeasured (first frame): the SAME curve in cq units, resolved
        // by the browser against the frame's content column, so there is no fallback-size flash.
        const floor = Math.round(FIT_MIN_FONT_PX * scale);
        const measured = size.width > 0 && size.height > 0;
        const valuePx = measured ? fitValueFont(size, chars, { floor }) : undefined;
        const valueFont = valuePx ?? fitValueFontCss(chars, { floor });
        // The timestamp is the first thing to give: it shows only when the measured box has room
        // for it under the value (unmeasured -- first frame, jsdom -- it shows).
        const showTimestamp =
          valuePx === undefined || size.height - Math.ceil(valuePx * 1.1) >= TIMESTAMP_ROOM_PX * scale;
        return (
          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'safe center',
              height: '100%',
              boxSizing: 'border-box',
              overflow: 'hidden',
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
                <span
                  style={{
                    fontSize: valuePx !== undefined ? Math.round(valuePx / 2) : `calc(${valueFont} / 2)`,
                    color: css('muted-foreground'),
                    marginLeft: spaceVar(1),
                  }}
                >
                  {unit}
                </span>
              ) : null}
            </div>
            {sample?.occurredTime && showTimestamp ? (
              <div style={{ fontSize: fontVar('xs'), color: css('muted-foreground'), marginTop: spaceVar(1) }}>
                {formatTimestamp(sample.occurredTime, timeZone)}
              </div>
            ) : null}
          </div>
        );
      }}
    </WidgetFrame>
  );
}

// The registration: this widget's portable descriptor paired with its component, declared
// through the same public `defineWidget` a third-party package uses.
export const latestCardWidget = defineWidget({
  descriptor: builtinWidgetDescriptors['latest-card'],
  implementation: { kind: 'trusted-react', component: LatestCard },
});
