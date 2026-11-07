// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// state-timeline -- discrete states over time: one lane per measurement, each lane a strip of runs
// ("running", "stopped", "fault") across the board's span. The states are the threshold bands the
// measurement's bucket averages fall in, so a 0/1/2 machine-state signal with three labelled
// thresholds reads as a Gantt of the machine, and a temperature with a warning band reads as the
// stretches it spent hot.
//
// 🔴 A segment is the state of a bucket AVERAGE, not an instant of change (the footer says so). A
// slot with no readings is NO DATA, drawn hollow and dashed; it is never merged into the state on
// either side of it, because a gap is not a continuation.
//
// All DOM (percent-width runs), so the readability gate measures the lane names and the legend.

import { useMemo } from 'react';

import {
  css,
  fontVar,
  formatDateTime,
  formatTimestamp,
  levelColor,
  optString,
  optThresholds,
  spaceVar,
  useSeverityColors,
  useWidgetTimeZone,
  WidgetFrame,
  type Threshold,
  type WidgetProps,
} from '@devicechain/widget-sdk';

import { bucketLabel, type HistoryView } from '../history-model';
import { LevelEmpty } from '../level-readout';
import {
  buildLane,
  laneNames,
  shares,
  statesUsed,
  type Lane,
  type StateKey,
} from '../state-timeline-model';
import { stateTimelineStrings, type StateTimelineStrings } from '../state-timeline-strings';

const LANE_NAME_PX = 112;
// The colours of states with no severity colour (a `normal` band, and the lead-in below the first
// threshold), in the order the states appear. They are the chart hues that no severity uses (the
// severity palette takes the red, the orange and the pink), so a Running lane is never mistaken for a
// Fault one; past three, a state is the muted foreground and is told apart by its label.
const NEUTRAL_ACCENTS = ['chart-2', 'chart-1', 'chart-4'] as const;

export function StateTimeline({ widget, history }: WidgetProps & { history?: HistoryView }) {
  const strings = stateTimelineStrings();
  const title = optString(widget.options, 'title');
  const message = (text: string) => (
    <WidgetFrame title={title} flush>
      <LevelEmpty message={text} />
    </WidgetFrame>
  );
  if (!history) return message(strings.loading);
  if (history.status !== 'ready') return message(strings[history.status]);
  return <Ready widget={widget} history={history} strings={strings} />;
}

// The name of a state: the band's own label, else its level, else (for the lead-in) "Normal".
export function stateLabel(key: StateKey, bands: readonly Threshold[], strings: StateTimelineStrings): string {
  if (key === 'none') return strings.noData;
  if (key === -1) return strings.baseline;
  const band = bands[key];
  return band?.label ?? (band ? (strings.levels[band.level] ?? band.level) : '');
}

// A lane's accessible description: how long each state lasted, then the runs in order (capped, so a
// flapping signal does not become a wall of speech). The segments' own `title`s are not reachable
// under role="img", so the sequence has to be said here.
const MAX_SPOKEN_RUNS = 24;
function laneDescription(lane: Lane, share: Map<StateKey, number>, bands: readonly Threshold[], strings: StateTimelineStrings, when: (ms: number) => string): string {
  const shares = [...share.entries()].map(([k, v]) => `${stateLabel(k, bands, strings)} ${Math.round(v * 100)}%`).join(', ');
  const runs = lane.segments.slice(0, MAX_SPOKEN_RUNS).map((s) => strings.segment(stateLabel(s.state, bands, strings), when(s.from), when(s.to))).join('; ');
  return strings.lane(lane.measurement, shares, runs);
}

function Ready({ widget, history, strings }: { widget: WidgetProps['widget']; history: Extract<HistoryView, { status: 'ready' }>; strings: StateTimelineStrings }) {
  const colors = useSeverityColors();
  const timeZone = useWidgetTimeZone();
  const title = optString(widget.options, 'title');
  const bands = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const names = useMemo(
    () => laneNames([], widget.datasource?.measurements ?? [], history.buckets.map((b) => b.name)),
    [widget.datasource, history.buckets],
  );
  const lanes = useMemo(() => names.map((n) => buildLane(n, history.buckets, history.window, bands)), [names, history.buckets, history.window, bands]);
  const used = useMemo(() => statesUsed(lanes), [lanes]);
  const bucket = bucketLabel(history.window.intervalSeconds);
  const start = Date.parse(history.window.startTime);
  const end = Date.parse(history.window.endTime);
  const span = end - start;
  const dated = span > 24 * 3600 * 1000;
  const when = (ms: number): string => (dated ? formatDateTime(new Date(ms).toISOString(), timeZone) : formatTimestamp(new Date(ms).toISOString(), timeZone));
  const hasData = lanes.some((l) => l.segments.some((s) => s.state !== 'none'));

  // Which neutral accent each colourless state takes, by order of appearance among the states used.
  const neutral = new Map<StateKey, string>();
  for (const k of used) {
    if (k === 'none') continue;
    if (levelColor(colors, k === -1 ? undefined : bands[k]?.level) === undefined) {
      const i = neutral.size;
      neutral.set(k, i < NEUTRAL_ACCENTS.length ? css(NEUTRAL_ACCENTS[i]) : css('muted-foreground'));
    }
  }
  const fill = (key: StateKey): { background: string; border: string } => {
    if (key === 'none') return { background: 'transparent', border: `1px dashed ${css('muted-foreground')}` };
    const color = levelColor(colors, key === -1 ? undefined : bands[key]?.level) ?? neutral.get(key) ?? css('muted-foreground');
    return { background: color, border: `1px solid ${color}` };
  };

  return (
    <WidgetFrame title={title} flush>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0, boxSizing: 'border-box', padding: spaceVar(2), gap: spaceVar(2), color: css('foreground') }}>
        {!hasData ? (
          <LevelEmpty message={strings.empty} />
        ) : (
          <div style={{ flex: '1 1 0', minHeight: 0, display: 'flex', flexDirection: 'column', gap: spaceVar(2), overflow: 'auto' }}>
            {lanes.map((lane) => {
              const share = shares(lane);
              return (
                <div key={lane.measurement} data-lane={lane.measurement} style={{ display: 'flex', alignItems: 'center', gap: spaceVar(2), minHeight: '1.75rem', flex: '1 1 0' }}>
                  <span style={{ width: LANE_NAME_PX, flexShrink: 0, fontSize: fontVar('sm'), overflowWrap: 'anywhere', lineHeight: 1.15 }}>{lane.measurement}</span>
                  <div role="img" aria-label={laneDescription(lane, share, bands, strings, when)} style={{ position: 'relative', flex: '1 1 0', minWidth: 0, height: '100%', minHeight: '1.25rem', maxHeight: '3rem' }}>
                    {lane.segments.map((s) => (
                      <div
                        key={s.from}
                        data-segment={String(s.state)}
                        data-threshold-level={typeof s.state === 'number' && s.state >= 0 ? bands[s.state]?.level : undefined}
                        title={strings.segment(stateLabel(s.state, bands, strings), when(s.from), when(s.to))}
                        style={{
                          position: 'absolute',
                          top: 0,
                          bottom: 0,
                          left: `${((s.from - start) / span) * 100}%`,
                          width: `${((s.to - s.from) / span) * 100}%`,
                          boxSizing: 'border-box',
                          ...fill(s.state),
                        }}
                      />
                    ))}
                  </div>
                </div>
              );
            })}
            <div style={{ display: 'flex', justifyContent: 'space-between', paddingLeft: LANE_NAME_PX + 8, fontSize: fontVar('xs'), color: css('muted-foreground'), fontVariantNumeric: 'tabular-nums' }}>
              <span>{when(start)}</span>
              <span>{when(end)}</span>
            </div>
          </div>
        )}
        <div data-state-legend="" style={{ display: 'flex', flexWrap: 'wrap', gap: spaceVar(3), alignItems: 'center', fontSize: fontVar('xs'), color: css('muted-foreground') }} title={strings.basisTitle(bucket)}>
          {used.map((k) => (
            <span key={String(k)} data-legend-state={String(k)} style={{ display: 'inline-flex', alignItems: 'center', gap: spaceVar(1) }}>
              <span aria-hidden="true" style={{ width: '0.8em', height: '0.8em', borderRadius: 2, boxSizing: 'border-box', ...fill(k) }} />
              {stateLabel(k, bands, strings)}
            </span>
          ))}
          <span data-state-basis={bucket} style={{ marginLeft: 'auto' }}>
            {strings.basis(bucket)}
          </span>
        </div>
      </div>
    </WidgetFrame>
  );
}
