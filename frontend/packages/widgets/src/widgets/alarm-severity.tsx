// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// alarm-severity -- how many alarms are at each severity: a horizontal stacked bar over a legend
// of counts. The counts are device-management's own per-severity counts, so they are exact however
// many alarms there are. Unbound, they cover EVERY alarm the viewer can see, whatever raised it (a
// device, an asset, an area, a customer, a group), and sum to an alarm count's total; set to a
// device type, only the alarms on that type's devices, and the caption says so. The viewer's own
// scope (a customer's alarms) is applied by the server.
//
//   - EVERY severity is listed, most severe first, so the scale does not move as alarms come and
//     go. A severity with none is DIMMED, not hidden, and has no segment in the bar. Its TEXT is
//     dimmed by colour (muted-foreground, which keeps 4.5:1 on the card in both themes), never by
//     opacity (foreground at 45% falls to ~3.2:1 on a light card); only the decorative swatch fades;
//   - a severity with alarms takes its severity colour. INDETERMINATE (and any severity this
//     build does not know) takes the neutral muted colour, as everywhere else: no hue it has not
//     earned. There is no "normal" colour, and an empty scope is a calm, empty track;
//   - the bar is decoration (hidden from assistive tech). The legend is the content: a real list
//     whose items read "Critical: 2", so the colour is never the only carrier of the signal;
//   - a source that cannot count by severity says so rather than drawing a zero.
//
// DOM text, so the readability gate sees it. Lives in the alarm chunk; nothing here is eager.

import {
  css,
  fontVar,
  optString,
  spaceVar,
  useSeverityColors,
  WidgetFrame,
  type AlarmStreamState,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { alarmStrings } from '../alarm-strings';
import { severityColor } from './severity';

// The scale, most severe first. Everything a source returns that is not one of these (a future or
// hand-edited severity) is counted with INDETERMINATE: counted, never dropped.
export const SEVERITY_SCALE = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'] as const;
export type ScaleSeverity = (typeof SEVERITY_SCALE)[number];

// severityCounts folds the source's per-severity counts onto the scale. Zero severities are present
// with a 0.
export function severityCounts(source: readonly { severity: string; count: number }[]): Record<ScaleSeverity, number> {
  const counts: Record<ScaleSeverity, number> = { CRITICAL: 0, MAJOR: 0, MINOR: 0, WARNING: 0, INDETERMINATE: 0 };
  for (const c of source) {
    if (!(c.count > 0)) continue;
    const key = (SEVERITY_SCALE as readonly string[]).includes(c.severity) ? (c.severity as ScaleSeverity) : 'INDETERMINATE';
    counts[key] += c.count;
  }
  return counts;
}

function Message({ title, text }: { title: string | undefined; text: string }) {
  return (
    <WidgetFrame title={title}>
      <div
        data-alarm-severity="message"
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          height: '100%',
          textAlign: 'center',
          fontSize: fontVar('sm'),
          color: css('muted-foreground'),
        }}
      >
        {text}
      </div>
    </WidgetFrame>
  );
}

export function AlarmSeverity({ widget, data }: WidgetProps<AlarmStreamState>) {
  const strings = alarmStrings();
  const colors = useSeverityColors();
  const title = optString(widget.options, 'title');
  const deviceType = optString(widget.options, 'deviceType') || undefined;

  if (!data.severityCounts) {
    const text = data.loading
      ? strings.breakdown.loading
      : data.error
        ? strings.breakdown.failed
        : strings.breakdown.unsupported;
    return <Message title={title} text={text} />;
  }
  const counts = severityCounts(data.severityCounts);
  const total = SEVERITY_SCALE.reduce((n, s) => n + counts[s], 0);
  const words = (s: ScaleSeverity) => strings.severity[s] ?? s;
  // A type-scoped breakdown counts device alarms of that type only, and says so in its caption.
  const caption = deviceType ? strings.breakdown.ofType(total, deviceType) : strings.breakdown.total(total);
  const listLabel = `${strings.breakdown.label}, ${caption}`;

  return (
    <WidgetFrame title={title}>
      <div
        data-alarm-severity="breakdown"
        data-total={total}
        style={{
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'safe center',
          gap: spaceVar(2),
          height: '100%',
          boxSizing: 'border-box',
          overflow: 'hidden',
        }}
      >
        <div
          aria-hidden="true"
          data-alarm-severity-bar=""
          style={{
            display: 'flex',
            flex: '0 0 auto',
            height: spaceVar(3),
            borderRadius: spaceVar(1),
            overflow: 'hidden',
            background: css('muted'),
          }}
        >
          {SEVERITY_SCALE.filter((s) => counts[s] > 0).map((s) => (
            <div key={s} data-segment={s} style={{ flex: `${counts[s]} 1 0`, minWidth: 2, background: severityColor(colors, s) }} />
          ))}
        </div>
        <div
          data-alarm-severity-total=""
          style={{ flex: '0 0 auto', fontSize: fontVar('sm'), color: css('muted-foreground'), fontVariantNumeric: 'tabular-nums' }}
        >
          {caption}
        </div>
        <ul
          aria-label={listLabel}
          style={{
            listStyle: 'none',
            margin: 0,
            padding: 0,
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 7.5rem), 1fr))',
            gap: `${spaceVar(1)} ${spaceVar(3)}`,
            minHeight: 0,
            overflow: 'hidden',
          }}
        >
          {SEVERITY_SCALE.map((s) => {
            const zero = counts[s] === 0;
            return (
              <li
                key={s}
                data-severity={s}
                data-zero={zero ? 'true' : undefined}
                aria-label={strings.breakdown.item(words(s), counts[s])}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: spaceVar(2),
                  minWidth: 0,
                  fontSize: fontVar('md'),
                  color: css(zero ? 'muted-foreground' : 'foreground'),
                }}
              >
                <span
                  aria-hidden="true"
                  style={{
                    flex: '0 0 auto',
                    width: spaceVar(3),
                    height: spaceVar(3),
                    borderRadius: '50%',
                    background: severityColor(colors, s),
                    opacity: zero ? 0.35 : 1,
                  }}
                />
                <span style={{ flex: '1 1 auto', minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {words(s)}
                </span>
                <span style={{ flex: '0 0 auto', fontWeight: zero ? 400 : 700, fontVariantNumeric: 'tabular-nums' }}>{counts[s]}</span>
              </li>
            );
          })}
        </ul>
      </div>
    </WidgetFrame>
  );
}
