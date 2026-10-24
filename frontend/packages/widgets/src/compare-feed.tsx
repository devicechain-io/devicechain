// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The multi-device compare chart: ONE measurement, a line per member of a group, over the board's
// span. Its own lazy chunk, loaded from the fleet feed's entry only when a time-series chart is set
// to one line per device -- a board without one never downloads it.
//
//   - ONE live subscription (hub.subscribeMembers), then one history read per DRAWN member through
//     hub.readMemberSeeds: bounded hub-wide (MEMBER_SEED_CONCURRENCY) and shared with any widget
//     asking the same question. There is no batched history read in the platform (the store
//     aggregates one device at a time), so the cost of the chart is the number of lines it draws --
//     which is why that number is capped (`maxSeries`) and the cap is said on the chart.
//   - A history read is the measurement alone (the store narrows by name), over the board's span (or
//     the chart's pinned one) ending at the last minute boundary, in buckets of bucketSecondsFor(span):
//     one point per bucket, the bucket's AVERAGE. The chart says so (the badge), and says that
//     threshold lines are references, never a reading of a bucket.
//   - Live readings extend each line past the history's end. While the board is paused the view is
//     cut at the freeze instant; the buffer keeps filling and the chart catches up on resume.
//   - A member stream that resumes after an interruption re-reads the histories, at once and again
//     just after the next minute boundary (as the fleet feed does).
//
// Rebuilding the state per sample would re-render the chart hundreds of times a second, so changes
// are folded into a store and flushed at most once per FLUSH_MS.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  spanLabel,
  type MemberRoster,
  type MeasurementSample,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import {
  css,
  fontVar,
  optNumber,
  optString,
  optThresholds,
  resolveStaleAfterSeconds,
  spaceVar,
  useWidgetScale,
  useWidgetTimeRange,
  WidgetFrame,
  WidgetFreshnessProvider,
} from '@devicechain/widget-sdk';
import { buildLineOption, EChart, useChartTheme } from '@devicechain/widget-sdk/echarts';
import { useEffect, useMemo, useReducer, useRef, useState, type ComponentType } from 'react';

import { measurementSpan } from './binding-policy';
import { bucketSamples, capMembers, composeSeries, memberLabels, pushLive, seriesLimit, type CappedMembers, type MemberHistory } from './compare-model';
import { compareStrings } from './compare-strings';
import { seedWindow } from './fleet-model';
import { fleetStrings } from './fleet-strings';
import { samplesExport } from './export-rows';
import { LevelEmpty } from './level-readout';
import { Explained } from './no-reading';

export const FLUSH_MS = 250;
const MINUTE_MS = 60_000;
// How long past a minute boundary the second re-read after an interruption waits (the fleet feed's).
const RESEED_SETTLE_MS = 2_000;
// A reading ages out of the span with no event to say so.
const AGE_OUT_MS = 30_000;
// Below this width or height (px) the notes under the chart are said in a line.
const COMPACT_WIDTH = 320;
const COMPACT_HEIGHT = 200;

interface Store {
  key: string | null;
  roster: MemberRoster | null;
  live: Map<string, MeasurementSample[]>;
  history: Map<string, MemberHistory | 'failed'>;
  forbidden: boolean;
  error: unknown | null;
}

const emptyStore = (key: string | null): Store => ({ key, roster: null, live: new Map(), history: new Map(), forbidden: false, error: null });

export interface CompareFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  ErrorFrame: ComponentType<{ widget: WidgetInstance; error: unknown }>;
}

export default function CompareFeed({ hub, widget, descriptor, ErrorFrame }: CompareFeedProps) {
  const range = useWidgetTimeRange();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? range?.spanSeconds ?? DEFAULT_TIME_RANGE_SECONDS;
  const pausedAt = range?.paused ? range.pausedAt : undefined;
  const measurement = optString(widget.options, 'measurement') ?? widget.datasource?.measurements[0];
  const limit = seriesLimit(optNumber(widget.options, 'maxSeries'));
  // The datasource the live feed is asked for: the one measurement, so the hub delivers no other.
  const ds = widget.datasource && measurement ? { ...widget.datasource, measurements: [measurement] } : undefined;
  const dsKey = JSON.stringify(ds ?? null);
  const supported = hub.subscribeMembers !== undefined && hub.readMemberSeeds !== undefined;

  const store = useRef<Store>(emptyStore(dsKey));
  const [version, bump] = useReducer((n: number) => n + 1, 0);
  const [rosterKey, setRosterKey] = useState<string | null>(null);
  const generation = useRef(0);
  const flushTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const boundaryTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [resumes, resumed] = useReducer((n: number) => n + 1, 0);
  // The tokens drawn, which the live feed filters by.
  const drawn = useRef<Set<string>>(new Set());

  const schedule = (): void => {
    if (flushTimer.current !== undefined) return;
    flushTimer.current = setTimeout(() => {
      flushTimer.current = undefined;
      bump();
    }, FLUSH_MS);
  };

  useEffect(() => {
    const s = emptyStore(dsKey);
    store.current = s;
    drawn.current = new Set();
    setRosterKey(null);
    bump();
    const gen = ++generation.current;
    if (!ds || !supported) return;
    const dispose = hub.subscribeMembers!(ds, {
      roster: (roster) => {
        s.roster = roster;
        drawn.current = new Set(capMembers(roster, limit).shown.map((m) => m.token));
        setRosterKey(`${gen}\n${[...drawn.current].join('\n')}`);
        schedule();
      },
      next: (sample) => {
        if (!drawn.current.has(sample.deviceToken)) return;
        let buffer = s.live.get(sample.deviceToken);
        if (!buffer) s.live.set(sample.deviceToken, (buffer = []));
        pushLive(buffer, sample);
        schedule();
      },
      forbidden: () => {
        s.forbidden = true;
        schedule();
      },
      error: (err) => {
        s.error = err;
        schedule();
      },
      reconnected: () => {
        resumed();
        if (boundaryTimer.current !== undefined) return;
        const at = now();
        boundaryTimer.current = setTimeout(() => {
          boundaryTimer.current = undefined;
          resumed();
        }, MINUTE_MS - (at % MINUTE_MS) + RESEED_SETTLE_MS);
      },
    });
    return () => {
      dispose();
      if (flushTimer.current !== undefined) clearTimeout(flushTimer.current);
      flushTimer.current = undefined;
      if (boundaryTimer.current !== undefined) clearTimeout(boundaryTimer.current);
      boundaryTimer.current = undefined;
    };
    // `ds` is read through `dsKey` (value identity); a new limit re-draws from the same roster below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, dsKey, supported, limit]);

  // One history read per drawn member, again when the span or the freeze instant changes.
  useEffect(() => {
    const s = store.current;
    if (!supported || !s.roster || rosterKey === null || !measurement) return;
    const tokens = capMembers(s.roster, limit).shown.map((m) => m.token);
    if (tokens.length === 0) return;
    const window = seedWindow(spanSeconds, pausedAt ?? now());
    const end = Date.parse(window.endTime);
    let cancelled = false;
    void hub.readMemberSeeds!(
      tokens,
      {
        // The plan reads the one measurement over the span and keeps its buckets; the seed it returns
        // is empty because this chart draws the history itself, not a "latest" cell.
        seed: async (token, read) => {
          const buckets = await read({ startTime: window.startTime, endTime: window.endTime, intervalSeconds: window.intervalSeconds, name: measurement });
          // A read this effect no longer wants (the span, the freeze instant or the roster moved on
          // while it was in flight) must not overwrite the newer one's history when it lands last.
          if (!cancelled) s.history.set(token, { samples: bucketSamples(token, measurement, buckets, window.intervalSeconds), end });
          return { latest: {}, observed: [] };
        },
      },
      (token, seed, failure) => {
        if (cancelled) return;
        if (failure === 'forbidden') s.forbidden = true;
        // A re-read that failed keeps the history it would have replaced: those points are still true.
        else if (!seed && !s.history.has(token)) s.history.set(token, 'failed');
        schedule();
      },
      () => cancelled,
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, supported, dsKey, rosterKey, spanSeconds, pausedAt, limit, resumes]);

  // A reading ages out of the span with no event to say so.
  useEffect(() => {
    if (pausedAt !== undefined) return;
    const id = setInterval(bump, AGE_OUT_MS);
    return () => clearInterval(id);
  }, [pausedAt]);

  const view = useMemo(() => {
    const s = store.current.key === dsKey ? store.current : emptyStore(dsKey);
    const capped = s.roster ? capMembers(s.roster, limit) : undefined;
    const labels = capped ? memberLabels(capped.shown) : new Map<string, string>();
    const until = pausedAt ?? now();
    const composed =
      capped && measurement
        ? composeSeries({ shown: capped.shown, labels, measurement, history: s.history, live: s.live, since: until - spanSeconds * 1000, until })
        : undefined;
    return { s, capped, labels, composed };
    // `version` is the store's change counter; the store itself is mutable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version, dsKey, limit, spanSeconds, pausedAt, now, measurement]);

  const { s, capped, labels, composed } = view;
  const freshness = useMemo(
    () => (descriptor.freshness === true ? { at: composed?.newest, staleAfterSeconds: resolveStaleAfterSeconds(widget.options) } : undefined),
    [descriptor, widget.options, composed?.newest],
  );
  if (s.error) return <ErrorFrame widget={widget} error={s.error} />;

  const title = optString(widget.options, 'title');
  const strings = fleetStrings();
  const empty = (message: string, kind?: 'unbound' | 'forbidden') => (
    <WidgetFrame title={title}>
      {kind ? <Explained kind={kind} message={message} /> : <LevelEmpty message={message} />}
    </WidgetFrame>
  );
  if (!measurement) return empty(strings.noMeasurement);
  if (!supported) return empty(strings.unsupported);
  if (s.forbidden) return empty(strings.forbidden, 'forbidden');
  if (ds?.kind === 'unbound') return empty(strings.unbound, 'unbound');
  if (!capped) return empty(strings.loading);
  if (capped.shown.length === 0) return empty(strings.noMembers);

  return (
    <WidgetFreshnessProvider freshness={freshness}>
      <WidgetFrame
        title={title}
        flush
        exportRows={() =>
          samplesExport({
            subject: title ?? measurement,
            samples: composed?.samples ?? [],
            unit: optString(widget.options, 'unit'),
            precision: optNumber(widget.options, 'precision'),
            seriesIs: 'device',
            spanSeconds,
          })
        }
      >
        <CompareBody
          widget={widget}
          names={capped.shown.map((m) => labels.get(m.token) ?? m.token)}
          samples={composed?.samples ?? []}
          bucketSeconds={seedWindow(spanSeconds, pausedAt ?? now()).intervalSeconds}
          capped={capped}
          historyFailed={composed?.historyFailed ?? 0}
        />
      </WidgetFrame>
    </WidgetFreshnessProvider>
  );
}

function CompareBody({
  widget,
  names,
  samples,
  bucketSeconds,
  capped,
  historyFailed,
}: {
  widget: WidgetInstance;
  names: string[];
  samples: MeasurementSample[];
  bucketSeconds: number;
  capped: CappedMembers;
  historyFailed: number;
}) {
  const theme = useChartTheme();
  const { size, scale } = useWidgetScale();
  const strings = compareStrings();
  const unit = optString(widget.options, 'unit');
  const precision = optNumber(widget.options, 'precision');
  const thresholds = useMemo(() => optThresholds(widget.options, 'thresholds'), [widget.options]);
  const min = optNumber(widget.options, 'min');
  const max = optNumber(widget.options, 'max');
  const namesKey = names.join('\n');
  const option = useMemo(
    () => buildLineOption(samples, theme, { measurements: names, unit, precision, thresholds, scale, min, max }, size),
    // `names` is read through `namesKey` (value identity): the array is rebuilt on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [samples, theme, namesKey, unit, precision, thresholds, scale, size, min, max],
  );
  const bucket = spanLabel(bucketSeconds);
  // The sentences, or (in a box too narrow for them: the widget's smallest span is 4 cells) the same
  // facts in a line, with the sentences in its title. Nothing is dropped, only shortened.
  const compact = (size.width > 0 && size.width < COMPACT_WIDTH) || (size.height > 0 && size.height < COMPACT_HEIGHT);
  const facts: Array<{ full: string; short: string }> = [];
  if (capped.capped) {
    const full = capped.pagedBySource ? strings.cappedPage(capped.shown.length, capped.read, capped.total) : strings.capped(capped.shown.length, capped.total);
    facts.push({ full, short: strings.cappedShort(capped.shown.length, capped.total) });
  }
  if (historyFailed > 0) facts.push({ full: strings.historyFailed(historyFailed), short: strings.historyFailedShort(historyFailed) });
  if (thresholds.length > 0) facts.push({ full: strings.thresholdNote, short: strings.thresholdShort });
  const badge = (
    <span
      data-compare-bucket={bucket}
      title={strings.bucketedTitle(bucket)}
      style={{
        padding: `0 ${spaceVar(1)}`,
        fontSize: fontVar('xs'),
        whiteSpace: 'nowrap',
        color: css('muted-foreground'),
        background: css('muted'),
        borderRadius: 4,
      }}
    >
      {strings.bucketed(bucket)}
    </span>
  );
  const noteStyle = { flexShrink: 0, padding: `${spaceVar(1)} ${spaceVar(2)}`, fontSize: fontVar('xs'), color: css('muted-foreground') } as const;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', width: '100%', height: '100%' }} data-compare-chart="">
      <div style={{ position: 'relative', flex: 1, minHeight: 0 }}>
        <EChart option={option} />
      </div>
      {/* Under the chart, never over it: a corner of the plot is where the legend runs once it fills
          the width (five devices on a phone) and where a threshold's label sits, and the badge
          covered both. */}
      {compact ? (
        <div data-compare-notes={facts.length > 0 ? '' : undefined} data-compare-compact="" title={facts.map((f) => f.full).join(' ')} style={noteStyle}>
          {badge}
          {facts.map((f) => ` · ${f.short}`).join('')}
        </div>
      ) : (
        <div data-compare-notes={facts.length > 0 ? '' : undefined} title={facts.length > 0 ? facts.map((f) => f.full).join(' ') : undefined} style={noteStyle}>
          <div>{badge}</div>
          {facts.map((f) => (
            <div key={f.full}>{f.full}</div>
          ))}
        </div>
      )}
    </div>
  );
}
