// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// fleet-count -- the figure above a fleet: how many of a group's devices need attention ("3 of 21"),
// or simply how many devices the group holds. It reads the same member feed as the fleet table, so
// a board that already shows the table asks the platform for nothing more.
//
//   - ATTENTION counts the devices whose newest reading of one measurement is in an abnormal band
//     of the card's thresholds (fleet-count-model.ts). The figure takes the colour of the worst
//     level among them; with none flagged (0 of N, or a fleet in its normal band) it keeps the
//     foreground, so a healthy fleet is calm;
//   - a device with no reading is counted apart ("2 not reporting", or "1 could not be read" when
//     its read failed), never as healthy; while any member is still loading there is no figure;
//   - a device whose profile does not declare the measurement is not in the "of N" at all, and is
//     noted apart ("1 does not report this"): a plant in a fuel count is outside the question,
//     which "not reporting" would misstate. A declared measurement the device is silent on (a dead
//     probe, a live heartbeat) stays in N as not reporting; so does a device whose profile could
//     not be read;
//   - a group larger than the feed reads says so ("Showing 500 of 812 devices") instead of
//     presenting the subset as the fleet;
//   - DEVICES shows the group's size, taken from the roster rather than from the readings.
//
// DOM text, so the readability gate sees it.

import { useMemo } from 'react';

import {
  css,
  FIT_MIN_FONT_PX,
  fontVar,
  levelColor,
  optString,
  optThresholds,
  primaryMeasurementName,
  spaceVar,
  useSeverityColors,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { attentionCount } from '../fleet-count-model';
import { fleetStrings } from '../fleet-strings';
import { LevelEmpty, LevelReadout } from '../level-readout';

// The room the caption lines need, before the density scale.
const CAPTION_ROOM_PX = 22;

export function FleetCount({ widget, data }: WidgetProps) {
  const strings = fleetStrings();
  const severity = useSeverityColors();
  const fleet = data.members;
  const showDevices = optString(widget.options, 'show') === 'devices';
  const measurement = optString(widget.options, 'measurement') ?? primaryMeasurementName(widget);
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const title = optString(widget.options, 'title');

  const empty = (message: string) => (
    <WidgetFrame title={title}>
      <LevelEmpty message={message} />
    </WidgetFrame>
  );
  if (!fleet || fleet.status === 'loading') return empty(strings.loading);
  if (fleet.status === 'unbound') return empty(strings.unbound);
  if (fleet.status === 'forbidden') return empty(strings.forbidden);
  if (fleet.status === 'unsupported') return empty(strings.unsupported);
  if (!showDevices) {
    if (measurement === undefined) return empty(strings.noMeasurementCount);
    if (thresholds.length === 0) return empty(strings.noBands);
  }

  const counted = showDevices ? undefined : attentionCount(fleet.members, measurement ?? '', thresholds);
  // Zero devices is a count, and the answer to "how many": 0. Attention over no devices has no
  // "N" to be a share of, so it says there are none, as the fleet table does.
  if (!showDevices && fleet.members.length === 0) return empty(strings.noMembers);
  // A count over the members read so far would be an undercount that looks like an answer.
  if (counted && counted.pending > 0) return empty(strings.loading);
  // No member's profile declares this measurement: there is no "N" here either. Over a
  // truncated group only the members read were asked, so the message says so rather than speaking
  // for devices past the page.
  if (counted && counted.members === 0) {
    return empty(fleet.truncated ? strings.noneMeasuredShown(fleet.members.length, fleet.total) : strings.noneMeasured);
  }

  const figure = showDevices ? String(fleet.total) : String(counted!.flagged);
  const unit = showDevices ? undefined : strings.countOf(counted!.members);
  const color = counted ? levelColor(severity, counted.worst) : undefined;
  const caption = showDevices ? strings.devices : counted!.flagged > 0 ? (counted!.label ?? strings.needAttention) : strings.needAttention;
  const notes: string[] = [];
  if (counted) {
    const silent = counted.members - counted.reporting - counted.unread;
    if (silent > 0) notes.push(strings.notReporting(silent));
    if (counted.unread > 0) notes.push(strings.notRead(counted.unread));
    if (counted.notMeasured > 0) notes.push(strings.notMeasured(counted.notMeasured));
    // The device count is the roster's own total, exact past the page; only the attention count
    // is taken from the members read, so only it says it covers part of the fleet.
    if (fleet.truncated) notes.push(strings.truncated(fleet.members.length, fleet.total));
  }

  return (
    <WidgetFrame title={title}>
      {({ size, scale }) => {
        const room = Math.round(CAPTION_ROOM_PX * scale) * (1 + notes.length);
        const fit = { width: size.width, height: Math.max(Math.round(FIT_MIN_FONT_PX * scale), size.height - room) };
        return (
          <div
            data-fleet-count={showDevices ? 'devices' : 'attention'}
            data-flagged={counted?.flagged}
            data-of={counted?.members}
            data-threshold-level={counted?.worst}
            style={{ display: 'flex', flexDirection: 'column', justifyContent: 'safe center', height: '100%', overflow: 'hidden' }}
          >
            <div style={{ flex: '1 1 0', minHeight: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <LevelReadout text={figure} unit={unit} color={color} size={fit} scale={scale} />
            </div>
            <div
              data-fleet-count-caption=""
              style={{
                flex: '0 0 auto',
                marginTop: spaceVar(1),
                textAlign: 'center',
                fontSize: fontVar('sm'),
                color: css('muted-foreground'),
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {caption}
            </div>
            {notes.map((note) => (
              <div
                key={note}
                data-fleet-count-note=""
                style={{ textAlign: 'center', fontSize: fontVar('xs'), color: css('muted-foreground'), overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
              >
                {note}
              </div>
            ))}
          </div>
        );
      }}
    </WidgetFrame>
  );
}
