// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// online-doughnut -- how many of a group's devices are online: a ring of online, offline and
// "no status yet", with the online count in the middle and every figure in a legend. Reads
// device-state through the presence feed (presence-feed.tsx).
//
//   - ISA-101: the normal state is neutral. Online is the quiet grey that fills most of a healthy
//     ring; only the offline slice takes a colour, and the legend names every count, so colour is
//     never the only signal. A group with nothing offline draws no colour at all;
//   - a device with no state row has never sent anything: its own pale slice ("no status yet"), not
//     offline. A device whose read failed is noted apart and left out of the ring;
//   - while any member's read is pending there is no ring: a share over the members read so far would
//     be an answer that looks final;
//   - a group larger than the feed reads says so ("Showing 500 of 812 devices") instead of presenting
//     the subset as the fleet.
//
// Drawn as SVG strokes with the figures as DOM text (the readability gate sees it).

import {
  css,
  FIT_MIN_FONT_PX,
  fontVar,
  levelColor,
  optString,
  spaceVar,
  useSeverityColors,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { arcPath } from '../category-geometry';
import { fleetStrings } from '../fleet-strings';
import { LevelEmpty } from '../level-readout';
import { Explained } from '../no-reading';
import { tallyPresence } from '../presence-model';
import { presenceStrings } from '../presence-strings';
import { OFFLINE_LEVEL } from './device-status';

// The ring, in a 100-unit box.
const RADIUS = 38;
const THICKNESS = 14;
// How much of the online grey the no-status slice keeps.
const UNKNOWN_OPACITY = 0.3;
// The room one legend line needs below the ring, before the density scale.
const LEGEND_LINE_PX = 18;

export function OnlineDoughnut({ widget, data }: WidgetProps) {
  const strings = presenceStrings();
  const fleet = fleetStrings();
  const severity = useSeverityColors();
  const feed = data.members;
  const title = optString(widget.options, 'title');

  const empty = (message: string, kind?: 'unbound' | 'forbidden') => (
    <WidgetFrame title={title}>
      {kind ? <Explained kind={kind} message={message} /> : <LevelEmpty message={message} />}
    </WidgetFrame>
  );
  if (!feed || feed.status === 'loading') return empty(fleet.loading);
  if (feed.status === 'unbound') return empty(fleet.unbound, 'unbound');
  if (feed.status === 'forbidden') return empty(fleet.forbidden, 'forbidden');
  if (feed.status === 'unsupported') return empty(strings.unsupported);
  if (feed.members.length === 0) return empty(fleet.noMembers);
  const tally = tallyPresence(feed.members);
  // A share over the members read so far would be an undercount that looks like an answer.
  if (tally.pending > 0) return empty(fleet.loading);
  const counted = tally.online + tally.offline + tally.unknown;
  if (counted === 0) return empty(strings.failed);

  const offlineColor = levelColor(severity, OFFLINE_LEVEL) ?? css('foreground');
  const segments = [
    { kind: 'online', count: tally.online, stroke: css('muted-foreground'), opacity: 1, label: strings.online },
    { kind: 'offline', count: tally.offline, stroke: offlineColor, opacity: 1, label: strings.offline },
    // The same neutral as online, pale: a device we know nothing of is less than one that is connected.
    { kind: 'unknown', count: tally.unknown, stroke: css('muted-foreground'), opacity: UNKNOWN_OPACITY, label: strings.unknown },
  ];
  let at = 0;
  const arcs = segments.map((s) => {
    const from = at;
    at += s.count / counted;
    return { ...s, from, to: at };
  });
  // Online and offline always have a line (a zero is an answer); no-status only when some device has none.
  const legend = arcs.filter((a) => a.kind !== 'unknown' || a.count > 0);
  const notes: string[] = [];
  if (tally.failed > 0) notes.push(strings.notRead(tally.failed));
  if (feed.truncated) notes.push(fleet.truncated(feed.members.length, feed.total));

  return (
    <WidgetFrame title={title}>
      {({ size, scale }) => {
        const legendRoom = Math.round(LEGEND_LINE_PX * scale) * (legend.length + notes.length);
        const box = Math.max(0, Math.min(size.width, size.height - legendRoom));
        const figure = Math.max(Math.round(FIT_MIN_FONT_PX * scale), Math.round(box * 0.2));
        return (
          <div
            data-online-doughnut=""
            data-online={tally.online}
            data-offline={tally.offline}
            data-no-status={tally.unknown}
            style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden', gap: spaceVar(1) }}
          >
            <div style={{ position: 'relative', flex: '1 1 0', minHeight: 0 }}>
              <svg
                data-doughnut=""
                viewBox="0 0 100 100"
                preserveAspectRatio="xMidYMid meet"
                role="img"
                aria-label={strings.chartLabel(tally.online, tally.offline, tally.unknown)}
                style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', display: 'block' }}
              >
                {arcs.map((a) => {
                  if (a.count === 0) return null;
                  const common = { fill: 'none', stroke: a.stroke, strokeWidth: THICKNESS, strokeOpacity: a.opacity } as const;
                  const d = arcPath(50, 50, RADIUS, a.from, a.to);
                  return d ? (
                    <path key={a.kind} data-segment={a.kind} data-count={a.count} d={d} style={common} />
                  ) : (
                    <circle key={a.kind} data-segment={a.kind} data-count={a.count} cx={50} cy={50} r={RADIUS} style={common} />
                  );
                })}
              </svg>
              <div
                data-doughnut-figure=""
                style={{
                  position: 'absolute',
                  inset: 0,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                  fontVariantNumeric: 'tabular-nums',
                  pointerEvents: 'none',
                }}
              >
                <span style={{ fontSize: figure, fontWeight: 600, lineHeight: 1.1, color: css('foreground') }}>{tally.online}</span>
                <span style={{ fontSize: fontVar('xs'), color: css('muted-foreground') }}>{`${strings.ofTotal(counted)} ${strings.onlineCaption}`}</span>
              </div>
            </div>
            <ul
              data-doughnut-legend=""
              style={{ flex: '0 0 auto', margin: 0, padding: 0, listStyle: 'none', fontSize: fontVar('xs'), color: css('muted-foreground') }}
            >
              {legend.map((a) => (
                <li key={a.kind} data-legend={a.kind} style={{ display: 'flex', alignItems: 'center', gap: spaceVar(1), whiteSpace: 'nowrap', overflow: 'hidden' }}>
                  <span
                    aria-hidden="true"
                    style={{
                      flex: '0 0 auto',
                      width: '0.7em',
                      height: '0.7em',
                      borderRadius: '50%',
                      background: a.stroke,
                      opacity: a.opacity,
                    }}
                  />
                  <span style={{ flex: '1 1 auto', overflow: 'hidden', textOverflow: 'ellipsis' }}>{a.label}</span>
                  <span style={{ color: a.kind === 'offline' && a.count > 0 ? offlineColor : css('foreground'), fontVariantNumeric: 'tabular-nums' }}>{a.count}</span>
                </li>
              ))}
              {notes.map((note) => (
                <li key={note} data-doughnut-note="" style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {note}
                </li>
              ))}
            </ul>
          </div>
        );
      }}
    </WidgetFrame>
  );
}
