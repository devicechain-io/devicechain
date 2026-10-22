// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// device-status -- one device's connectivity: Online or Offline in words, and when it was last seen.
// It reads device-state through the presence feed (presence-feed.tsx), not the device's readings.
//
//   - ISA-101: the normal state is neutral. Online keeps the foreground; only Offline takes a colour,
//     and the word says which, so colour is never the only signal;
//   - a device with no state row has never sent anything: it reads "No status yet", muted, and is not
//     painted as an outage;
//   - "last seen" is the device's last activity. The frame's age readout is the age of the STATUS
//     (when device-state was last read), with a stale badge past `staleAfterSeconds` -- never the
//     device's silence, which would grey out the very Offline it explains (presence-feed.tsx);
//   - the card shows ONE device: a group reaching it is refused in words, with where the group can be shown.
//
// DOM text, so the readability gate sees it.

import {
  css,
  FIT_MIN_FONT_PX,
  fontVar,
  levelColor,
  optString,
  spaceVar,
  useSeverityColors,
  viewerLocale,
  WidgetFrame,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { fleetStrings } from '../fleet-strings';
import { LevelEmpty, LevelReadout } from '../level-readout';
import { lastActivityMs, presenceKind } from '../presence-model';
import { presenceStrings } from '../presence-strings';

// The level an offline device is drawn at: attention, short of the alarm a rule would raise.
export const OFFLINE_LEVEL = 'major' as const;

// The room the caption line needs, before the density scale.
const CAPTION_ROOM_PX = 22;

// "Oct 21, 2026, 10:32 AM" in the viewer's language; an unparseable <html lang> falls back to the runtime's.
export function formatLastSeen(at: number, locale: string | undefined = viewerLocale()): string {
  try {
    return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(at);
  } catch {
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(at);
  }
}

export function DeviceStatus({ widget, data }: WidgetProps) {
  const strings = presenceStrings();
  const fleet = fleetStrings();
  const severity = useSeverityColors();
  const feed = data.members;
  const title = optString(widget.options, 'title');

  const empty = (message: string) => (
    <WidgetFrame title={title}>
      <LevelEmpty message={message} />
    </WidgetFrame>
  );
  if (!feed || feed.status === 'loading') return empty(fleet.loading);
  if (feed.status === 'unbound') return empty(strings.unbound);
  if (feed.status === 'forbidden') return empty(fleet.forbidden);
  if (feed.status === 'unsupported') return empty(strings.unsupported);
  if (feed.members.length === 0) return empty(fleet.noMembers);
  if (feed.members.length > 1 || feed.total > 1) return empty(`${strings.oneDevice} ${strings.useDoughnut}`);

  const presence = feed.members[0].presence;
  const kind = presenceKind(presence);
  if (kind === 'pending') return empty(fleet.loading);
  if (kind === 'failed') return empty(strings.failed);

  const figure = kind === 'online' ? strings.online : kind === 'offline' ? strings.offline : strings.unknown;
  const color = kind === 'offline' ? levelColor(severity, OFFLINE_LEVEL) : kind === 'unknown' ? css('muted-foreground') : undefined;
  const seen = lastActivityMs(presence);
  const caption = seen !== undefined ? strings.lastSeen(formatLastSeen(seen)) : strings.neverSeen;

  return (
    <WidgetFrame title={title}>
      {({ size, scale }) => {
        const room = Math.round(CAPTION_ROOM_PX * scale);
        const fit = { width: size.width, height: Math.max(Math.round(FIT_MIN_FONT_PX * scale), size.height - room) };
        return (
          <div
            data-device-status={kind}
            style={{ display: 'flex', flexDirection: 'column', justifyContent: 'safe center', height: '100%', overflow: 'hidden' }}
          >
            <div style={{ flex: '1 1 0', minHeight: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
              <LevelReadout text={figure} color={color} size={fit} scale={scale} />
            </div>
            <div
              data-device-status-caption=""
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
          </div>
        );
      }}
    </WidgetFrame>
  );
}
