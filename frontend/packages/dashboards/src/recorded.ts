// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// RecordedDataSource — plays one recorded run (recording.ts) through the SAME
// WidgetDataSource contract the live DashboardHub and the SyntheticDataSource implement,
// so the dashboard renderer and every widget consume it unchanged.
//
// It is deliberately NOT a mode of SyntheticDataSource. Synthetic data is canned and
// labelled synthetic; this is observed data, and folding the two together would blur the
// one boundary a viewer relies on. It also implements WidgetDataSource ONLY, not
// WidgetActions: a recording cannot acknowledge, clear or send anything, so no write
// control may render (the alarm table shows its action buttons only when an actions seam
// exists).
//
// ── Time ────────────────────────────────────────────────────────────────────
// A RecordedClock owns the cursor (play / pause / seek / rate). A source VIEW follows the
// clock forward: as the cursor passes a row's `t` the row is delivered to the widgets
// that asked for it.
//
// 🔴 A SEEK ENDS A VIEW; IT DOES NOT REWIND ONE. Widget streams only append (a chart
// window, a "latest" card keep what they were given), so a backwards jump cannot be
// retracted through a sink. The host therefore builds a NEW RecordedDataSource over the
// same parsed recording after every seek: the renderer sees a new data source, every
// widget re-subscribes and resets its buffer, and the new view back-fills the preceding
// window at the new cursor. The old view goes quiet on the seek event (it never emits
// another row, in either direction) and `isStale` reports it, so a host that forgets to
// swap it freezes visibly in a test instead of replaying the wrong moment.
//
// ── What the recording does not hold ────────────────────────────────────────
// A channel the recording did not capture raises NotInRecordingError through the sink's
// error path. It NEVER answers with an empty snapshot: an empty map would read as "no
// machine has a position" when the truth is "this recording has no positions". The widget
// frame already renders a sink error as "Data unavailable" plus the error text, so the
// message below is what a viewer reads.
//
// No network: this module touches no fetch, socket or timer of its own beyond the clock's
// optional frame ticker, and the tests trap any attempt.

import type {
  AlarmSnapshot,
  AlarmStreamSink,
  AlarmSubscription,
  CommandStreamSink,
  CommandSubscription,
  DeviceResolver,
  LocationStreamSink,
  LocationSubscription,
  WidgetDataSource,
  WidgetStreamSink,
} from './hub';
import type {
  AlarmRow,
  AnchorTarget,
  DatasourceSelector,
  EntityCandidateLister,
  EntityListKind,
  MeasurementSample,
} from './types';
import {
  compareInstants,
  recordingBounds,
  type BoardRecording,
  type RecordedAlarm,
  type RecordedAlarmEvent,
  type RecordedAlarmSnapshot,
} from './recording';

// How far back from the cursor a fresh subscription back-fills measurement rows. Raw rows,
// not buckets: a recording has no aggregate to show honestly.
export const RECORDED_HISTORY_WINDOW_MS = 60 * 60 * 1000;

// ---- the typed "not in this recording" error ------------------------------

export type RecordedChannel = 'measurements' | 'alarms' | 'locations' | 'commands';

export class NotInRecordingError extends Error {
  readonly channel: RecordedChannel;
  readonly detail: string;

  constructor(channel: RecordedChannel, detail = '') {
    super(`Not in this recording${detail ? `: ${detail}` : ''}`);
    this.name = 'NotInRecordingError';
    this.channel = channel;
    this.detail = detail;
  }

  // The widget error frame prints String(error). The default would read
  // "NotInRecordingError: Not in this recording", which says the name twice; the message
  // alone is what a viewer should see.
  toString(): string {
    return this.message;
  }
}

// ---- the replay clock -----------------------------------------------------

export const PLAYBACK_RATES = [1, 4, 16] as const;
export type PlaybackRate = (typeof PLAYBACK_RATES)[number];

// 'tick' = the cursor advanced during playback; 'seek' = the cursor was moved (any
// direction); 'state' = play / pause / rate changed with the cursor where it was.
export interface ClockEvent {
  timeMs: number;
  reason: 'tick' | 'seek' | 'state';
}

// A frame source for the clock. The default drives requestAnimationFrame (or a timer where
// there is none); a test injects its own, or drives the clock by hand with advance().
export interface ClockTicker {
  // Start calling onFrame(wallDeltaMs) until the returned stopper is called.
  start(onFrame: (wallDeltaMs: number) => void): () => void;
}

function defaultTicker(): ClockTicker {
  return {
    start(onFrame) {
      const now = (): number => (typeof performance !== 'undefined' ? performance.now() : Date.now());
      let last = now();
      const step = (): void => {
        const t = now();
        const dt = t - last;
        last = t;
        onFrame(dt);
      };
      if (typeof requestAnimationFrame === 'function') {
        let handle = 0;
        let stopped = false;
        const loop = (): void => {
          if (stopped) return;
          step();
          handle = requestAnimationFrame(loop);
        };
        handle = requestAnimationFrame(loop);
        return () => {
          stopped = true;
          if (typeof cancelAnimationFrame === 'function') cancelAnimationFrame(handle);
        };
      }
      const timer = setInterval(step, 100);
      return () => clearInterval(timer);
    },
  };
}

export class RecordedClock {
  // The end of the playable span, and its beginning (0 for a whole run; an excerpt starts
  // at its window).
  readonly durationMs: number;
  readonly minMs: number;
  private time: number;
  private isPlaying = false;
  private playbackRate: PlaybackRate = 1;
  private stopTicker: (() => void) | null = null;
  private readonly ticker: ClockTicker;
  private readonly listeners = new Set<(ev: ClockEvent) => void>();

  constructor(durationMs: number, options: { ticker?: ClockTicker; startMs?: number; minMs?: number } = {}) {
    if (!Number.isFinite(durationMs) || durationMs <= 0) {
      throw new RangeError(`RecordedClock: durationMs must be positive and finite, got ${durationMs}`);
    }
    this.durationMs = durationMs;
    this.minMs = options.minMs ?? 0;
    if (!Number.isFinite(this.minMs) || this.minMs < 0 || this.minMs >= durationMs) {
      throw new RangeError(`RecordedClock: minMs must be in [0, ${durationMs}), got ${this.minMs}`);
    }
    this.ticker = options.ticker ?? defaultTicker();
    this.time = this.clampTime(options.startMs ?? this.minMs);
  }

  get timeMs(): number {
    return this.time;
  }

  get playing(): boolean {
    return this.isPlaying;
  }

  get rate(): PlaybackRate {
    return this.playbackRate;
  }

  get ended(): boolean {
    return this.time >= this.durationMs;
  }

  // play starts the cursor moving. At the end of the run it does nothing: playback stops
  // there (no loop seam) and a host that wants to start over seeks to 0 first.
  play(): void {
    if (this.isPlaying || this.ended) return;
    this.isPlaying = true;
    this.stopTicker = this.ticker.start((dt) => this.advance(dt));
    this.emit('state');
  }

  pause(): void {
    if (!this.isPlaying) return;
    this.halt();
    this.emit('state');
  }

  // seek moves the cursor, clamped to [minMs, durationMs]. Playback state is unchanged, except
  // that seeking to the very end stops it (there is nothing left to play).
  seek(tMs: number): void {
    if (!Number.isFinite(tMs)) throw new RangeError(`RecordedClock.seek: not a finite time: ${tMs}`);
    this.time = this.clampTime(tMs);
    if (this.isPlaying && this.ended) this.halt();
    this.emit('seek');
  }

  setRate(rate: PlaybackRate): void {
    if (!(PLAYBACK_RATES as readonly number[]).includes(rate)) {
      throw new RangeError(`RecordedClock.setRate: ${rate} is not one of ${PLAYBACK_RATES.join(', ')}`);
    }
    this.playbackRate = rate;
    this.emit('state');
  }

  // advance moves the cursor by wallDeltaMs of real time (scaled by the playback rate).
  // The ticker calls this each frame; a test calls it directly. It does nothing while
  // paused, and stops at the end of the run.
  advance(wallDeltaMs: number): void {
    if (!this.isPlaying) return;
    if (!Number.isFinite(wallDeltaMs) || wallDeltaMs < 0) {
      throw new RangeError(`RecordedClock.advance: expected a non-negative finite delta, got ${wallDeltaMs}`);
    }
    this.time = this.clampTime(this.time + wallDeltaMs * this.playbackRate);
    if (this.ended) this.halt();
    this.emit('tick');
  }

  subscribe(listener: (ev: ClockEvent) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private halt(): void {
    this.isPlaying = false;
    this.stopTicker?.();
    this.stopTicker = null;
  }

  private clampTime(t: number): number {
    return t < this.minMs ? this.minMs : t > this.durationMs ? this.durationMs : t;
  }

  private emit(reason: ClockEvent['reason']): void {
    for (const listener of [...this.listeners]) listener({ timeMs: this.time, reason });
  }
}

// ---- helpers --------------------------------------------------------------

// First index whose value is >= x (the array is non-decreasing).
function lowerBound(a: readonly number[], x: number): number {
  let lo = 0;
  let hi = a.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (a[mid] < x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// First index whose value is > x.
function upperBound(a: readonly number[], x: number): number {
  let lo = 0;
  let hi = a.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (a[mid] <= x) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// createRecordedClock builds a clock covering exactly what the recording can play: the
// whole run, or an excerpt's window (starting at its beginning).
export function createRecordedClock(rec: BoardRecording, options: { ticker?: ClockTicker } = {}): RecordedClock {
  const { fromMs, toMs } = recordingBounds(rec);
  return new RecordedClock(toMs, { minMs: fromMs, ticker: options.ticker });
}

// ---- anchors, resolver and lister -----------------------------------------

// The recording does not carry the anchor a board is bound to; it carries the devices. The
// host names the anchors (taken from the board definition it plays) whose members are the
// run's devices. Every device of a Sitepulse run is a member of the one site CUSTOMER, so
// that anchor expands to ALL of the run's devices. Exactly one anchor may be declared, and
// it must be a customer: an area or asset anchor would claim every device is in it, which
// the recording cannot know. An anchor that was not declared is unknown and is refused,
// never answered with an empty set. Declaring nothing declares no anchor at all.
export interface RecordedSiteOptions {
  siteAnchors?: readonly AnchorTarget[];
}

function sameAnchor(a: AnchorTarget, b: AnchorTarget): boolean {
  return a.relationship === b.relationship && a.targetType === b.targetType && a.targetToken === b.targetToken;
}

// The one declared site anchor, or null when none was declared. Anything else is refused
// when the source, resolver or lister is built, not at some later lookup.
function declaredSite(options: RecordedSiteOptions): AnchorTarget | null {
  const anchors = options.siteAnchors;
  if (anchors === undefined) return null;
  if (anchors.length !== 1) {
    throw new Error(`RecordedSiteOptions.siteAnchors must declare exactly one anchor, got ${anchors.length}`);
  }
  if (anchors[0].targetType !== 'customer') {
    throw new Error(
      `RecordedSiteOptions.siteAnchors must be a customer anchor, got '${anchors[0].targetType}': ` +
        'a recording cannot say that every device belongs to an area or an asset',
    );
  }
  return anchors[0];
}

function anchorMembers(rec: BoardRecording, site: AnchorTarget | null, anchor: AnchorTarget): string[] {
  if (!site || !sameAnchor(site, anchor)) {
    throw new Error(
      `anchor ${anchor.targetType} '${anchor.targetToken}' (${anchor.relationship}) is not in this recording`,
    );
  }
  return rec.devices.map((d) => d.token);
}

// createRecordedResolver answers the hub's DeviceResolver questions from the recording: a
// site anchor expands to the run's devices, and a device exists if the run has it. An
// anchor the host did not declare THROWS (an empty answer would read as "the site had no
// machines").
export function createRecordedResolver(rec: BoardRecording, options: RecordedSiteOptions = {}): DeviceResolver {
  const devices = new Set(rec.devices.map((d) => d.token));
  const site = declaredSite(options);
  return {
    devicesForAnchor: async (anchor) => anchorMembers(rec, site, anchor),
    deviceExists: async (token) => devices.has(token),
  };
}

// createRecordedLister backs a root entity-selector's candidate list from the recording.
//
// 🔴 WITHOUT A LISTER THE ENTITY SELECTOR SAYS "Selection is available on the live
// dashboard." A replay must never say that. The lister is what turns the widget's
// selection on, so a host that mounts a recording passes this one.
//
// Devices are listed by token (the recording carries no display names). The other kinds
// list the declared site anchors of that type; a kind with none yields an empty list,
// which is true of the recording.
export function createRecordedLister(rec: BoardRecording, options: RecordedSiteOptions = {}): EntityCandidateLister {
  const site = declaredSite(options);
  return async (kind: EntityListKind) => {
    if (kind === 'device') return rec.devices.map((d) => ({ token: d.token, name: null }));
    return site && site.targetType === kind ? [{ token: site.targetToken, name: null }] : [];
  };
}

// ---- the recording, indexed -----------------------------------------------

interface Series {
  device: string;
  name: string;
  // Row indices into the measurement columns, ascending in time, and the same rows' times.
  rows: number[];
  times: number[];
}

// One alarm record on the merged history timeline. A snapshot REPLACES the whole alarm set;
// an event sets one alarm. At equal times a snapshot comes first.
type TimelineEntry =
  | { tMs: number; snapshot: RecordedAlarmSnapshot }
  | { tMs: number; event: RecordedAlarmEvent };

interface Indexed {
  byDevice: Map<string, Series[]>;
  timeline: TimelineEntry[];
  timelineTimes: number[];
}

// A view is rebuilt after every seek, so the index is built once per parsed recording.
const indexCache = new WeakMap<BoardRecording, Indexed>();

function indexOf(rec: BoardRecording): Indexed {
  const cached = indexCache.get(rec);
  if (cached) return cached;

  const bySeries = new Map<string, Series>();
  const byDevice = new Map<string, Series[]>();
  const { d, n, t } = rec.measurements;
  for (let i = 0; i < t.length; i++) {
    const key = d[i] * rec.channels.measurements.length + n[i];
    let s = bySeries.get(String(key));
    if (!s) {
      s = { device: rec.devices[d[i]].token, name: rec.channels.measurements[n[i]], rows: [], times: [] };
      bySeries.set(String(key), s);
      const list = byDevice.get(s.device);
      if (list) list.push(s);
      else byDevice.set(s.device, [s]);
    }
    s.rows.push(i);
    s.times.push(t[i]);
  }

  const timeline: TimelineEntry[] = [
    ...rec.alarms.snapshots.map((snapshot): TimelineEntry => ({ tMs: snapshot.tMs, snapshot })),
    ...rec.alarms.events.map((event): TimelineEntry => ({ tMs: event.tMs, event })),
  ];
  // Array.prototype.sort is stable: snapshots were listed first, so they stay first at ties.
  timeline.sort((a, b) => a.tMs - b.tMs);

  const indexed: Indexed = { byDevice, timeline, timelineTimes: timeline.map((e) => e.tMs) };
  indexCache.set(rec, indexed);
  return indexed;
}

// An alarm as the fold holds it.
interface FoldedAlarm {
  id: string;
  dev: string;
  key: string;
  metric: string;
  state: string;
  sev: string;
  raised: string | null;
  cleared: string | null;
  ack: boolean;
  // The recording time this state is known as of: the event's time, or for an alarm taken
  // from a snapshot, when that snapshot's query was sent.
  asOfMs: number;
}

// ---- the data source ------------------------------------------------------

interface Subscription {
  // Called with the new cursor whenever the clock ticks forward.
  advance(cursor: number): void;
  dispose(): void;
}

interface DeviceNames {
  deviceToken: string;
  names: Set<string>;
}

export class RecordedDataSource implements WidgetDataSource {
  private readonly rec: BoardRecording;
  private readonly site: AnchorTarget | null;
  private readonly index: Indexed;
  private readonly startMs: number;
  private readonly devices: ReadonlySet<string>;
  private readonly subs = new Set<Subscription>();
  private readonly unlisten: () => void;
  private cursor: number;
  private stale = false;

  // `clock` is the shared replay clock; the view starts at its current time. `options`
  // names the site anchors this recording's devices belong to (see RecordedSiteOptions).
  constructor(rec: BoardRecording, clock: RecordedClock, options: RecordedSiteOptions = {}) {
    this.rec = rec;
    this.site = declaredSite(options);
    this.index = indexOf(rec);
    this.cursor = clock.timeMs;
    this.startMs = Date.parse(rec.startedAtUtc);
    this.devices = new Set(rec.devices.map((d) => d.token));
    this.unlisten = clock.subscribe((ev) => this.onClock(ev));
  }

  // True once the clock has been seeked since this view was built. A stale view emits
  // nothing further; the host builds a new view at the new cursor.
  get isStale(): boolean {
    return this.stale;
  }

  private onClock(ev: ClockEvent): void {
    if (this.stale) return;
    if (ev.reason === 'seek') {
      this.stale = true;
      this.unlisten();
      return;
    }
    if (ev.reason !== 'tick') return;
    this.cursor = ev.timeMs;
    for (const sub of [...this.subs]) sub.advance(this.cursor);
  }

  private iso(offsetMs: number): string {
    return new Date(this.startMs + offsetMs).toISOString();
  }

  // open tracks a subscription and returns its disposer. `start` runs once, in a
  // microtask, so the disposer is in the caller's hands before anything is emitted (the
  // contract every source keeps: a synchronous disposer, and nothing emitted after it).
  // `advance` runs on each forward tick after that.
  private open(handlers: {
    start(cursor: number, isDisposed: () => boolean): void;
    advance(cursor: number, isDisposed: () => boolean): void;
  }): () => void {
    let disposed = false;
    let started = false;
    const isDisposed = (): boolean => disposed;
    const sub: Subscription = {
      advance: (cursor) => {
        if (started && !disposed) handlers.advance(cursor, isDisposed);
      },
      dispose: () => {
        disposed = true;
        this.subs.delete(sub);
      },
    };
    this.subs.add(sub);
    queueMicrotask(() => {
      if (disposed || this.stale) return;
      started = true;
      handlers.start(this.cursor, isDisposed);
    });
    return sub.dispose;
  }

  // Resolve a selector to the devices it names, synchronously and from the recording.
  // Mirrors the hub: unbound is zero devices, an unresolved slot is a loud error.
  private resolve(ds: DatasourceSelector): DeviceNames[] {
    switch (ds.kind) {
      case 'device':
        return [{ deviceToken: ds.deviceToken, names: new Set(ds.measurements) }];
      case 'anchor':
        return anchorMembers(this.rec, this.site, ds.anchor).map((deviceToken) => ({
          deviceToken,
          names: new Set(ds.measurements),
        }));
      case 'unbound':
        return [];
      case 'slot':
        throw new Error(
          `dashboard slot '${ds.slot}' reached the data source unresolved; ` +
            'resolve it through the bindings first (resolveWidgetDatasource)',
        );
      default:
        throw new Error(`dashboard selector kind '${ds.kind}' is not supported yet`);
    }
  }

  // Report a failure on a sink, asynchronously and only if the subscription is still live.
  private failLater(err: unknown, sink: { error?: (e: unknown) => void }): () => void {
    let disposed = false;
    queueMicrotask(() => {
      if (!disposed) sink.error?.(err);
    });
    return () => {
      disposed = true;
    };
  }

  // ── measurements ──────────────────────────────────────────────────────────

  subscribeWidget(datasource: DatasourceSelector, sink: WidgetStreamSink): () => void {
    let groups: DeviceNames[];
    try {
      groups = this.resolve(datasource);
      for (const g of groups) {
        for (const name of g.names) {
          if (!this.rec.channels.measurements.includes(name)) {
            throw new NotInRecordingError('measurements', `no '${name}' series was recorded`);
          }
        }
      }
    } catch (err) {
      return this.failLater(err, sink);
    }

    // Each selected series keeps its own next-row position, so a tick delivers exactly the
    // rows the cursor has newly passed.
    const picked: Array<{ s: Series; next: number }> = [];
    for (const g of groups) {
      for (const s of this.index.byDevice.get(g.deviceToken) ?? []) {
        if (g.names.size === 0 || g.names.has(s.name)) picked.push({ s, next: 0 });
      }
    }

    const { t, v } = this.rec.measurements;
    const flush = (cursor: number, isDisposed: () => boolean): void => {
      const due: Array<{ t: number; sample: MeasurementSample }> = [];
      for (const p of picked) {
        const { s } = p;
        while (p.next < s.times.length && s.times[p.next] <= cursor) {
          const row = s.rows[p.next++];
          due.push({
            t: t[row],
            sample: {
              id: `${s.device}|${s.name}|${row}`,
              deviceToken: s.device,
              eventType: 0,
              // The time the viewer applied the row: the recording holds no separate
              // occurred time for measurements.
              occurredTime: this.iso(t[row]),
              name: s.name,
              value: v[row],
              classifier: null,
            },
          });
        }
      }
      // Chronological across series (stable for equal times), so a widget's window and a
      // "latest" card agree on what arrived last.
      due.sort((a, b) => a.t - b.t);
      for (const d of due) {
        if (isDisposed()) return;
        sink.next(d.sample);
      }
    };

    return this.open({
      // Back-fill: rows from the preceding window up to the cursor, then play on.
      start: (cursor, isDisposed) => {
        for (const p of picked) p.next = lowerBound(p.s.times, cursor - RECORDED_HISTORY_WINDOW_MS);
        flush(cursor, isDisposed);
      },
      advance: flush,
    });
  }

  // ── alarms ────────────────────────────────────────────────────────────────

  subscribeAlarms(subscription: AlarmSubscription, sink: AlarmStreamSink): () => void {
    let scope: Set<string> | null = null; // null = tenant-wide (no datasource)
    try {
      if (!this.rec.channels.alarms) throw new NotInRecordingError('alarms');
      if (subscription.datasource) {
        scope = new Set(this.resolve(subscription.datasource).map((g) => g.deviceToken));
      }
    } catch (err) {
      return this.failLater(err, sink);
    }

    // The fold is monotone because a view only moves forward: apply each history entry
    // once, in order. A snapshot replaces the set; an event sets one alarm.
    const { timeline, timelineTimes } = this.index;
    const alarms = new Map<string, FoldedAlarm>();
    let applied = 0;
    let lastSignature: string | null = null;

    const apply = (a: RecordedAlarm, asOfMs: number): void => {
      const prior = alarms.get(a.id);
      alarms.set(a.id, {
        id: a.id,
        dev: a.dev,
        key: a.key,
        metric: a.metric,
        state: a.state,
        sev: a.sev,
        // `occ` is the time of THIS state: the raise time for an ACTIVE row, the clear time
        // for a CLEARED one. A cleared alarm keeps the raise time its ACTIVE row gave it
        // (null if the history never showed it being raised).
        raised: a.state === 'ACTIVE' ? a.occ : (prior?.raised ?? null),
        cleared: a.state === 'CLEARED' ? a.occ : null,
        // Each record's own `ack` decides; acknowledgement is not carried over from an
        // earlier record of the same alarm.
        ack: a.ack === true,
        asOfMs,
      });
    };

    const emit = (cursor: number, isDisposed: () => boolean): void => {
      const upTo = upperBound(timelineTimes, cursor);
      for (; applied < upTo; applied++) {
        const entry = timeline[applied];
        if ('snapshot' in entry) {
          // A snapshot is the answer to a query sent at requestedAtMs, so it knows nothing
          // of an alarm that changed after that: those keep the event's (newer) state, even
          // when the snapshot omits or contradicts them. Everything else is replaced.
          const { requestedAtMs } = entry.snapshot;
          const newer = [...alarms.values()].filter((a) => a.asOfMs > requestedAtMs);
          alarms.clear();
          for (const a of entry.snapshot.alarms) apply(a, requestedAtMs);
          for (const a of newer) alarms.set(a.id, a);
        } else {
          apply(entry.event, entry.event.tMs);
        }
      }
      const matches = [...alarms.values()].filter(
        (a) =>
          (scope === null || scope.has(a.dev)) &&
          (!subscription.state || a.state === subscription.state) &&
          (!subscription.severity || a.sev === subscription.severity) &&
          (subscription.acknowledged == null || a.ack === subscription.acknowledged),
      );
      // Newest first by raise time, compared as instants (not as text); the id breaks ties
      // so the order is deterministic.
      const when = (a: FoldedAlarm): string | null => a.raised ?? a.cleared;
      const byTime = (a: FoldedAlarm, b: FoldedAlarm): number => {
        const wa = when(a);
        const wb = when(b);
        if (wa === null || wb === null) return wa === wb ? 0 : wa === null ? 1 : -1;
        return compareInstants(wb, wa);
      };
      matches.sort((a, b) => byTime(a, b) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
      const page = matches.slice(0, subscription.pageSize);
      // Whole snapshots, re-emitted only when the fold actually changed.
      const signature = JSON.stringify([matches.length, page]);
      if (signature === lastSignature || isDisposed()) return;
      lastSignature = signature;
      const snapshot: AlarmSnapshot = { alarms: page.map(toAlarmRow), total: matches.length };
      sink.next(snapshot);
    };

    return this.open({ start: emit, advance: emit });
  }

  // ── commands ──────────────────────────────────────────────────────────────

  // The recording holds no command data, so this channel is never available.
  subscribeCommands(_subscription: CommandSubscription, sink: CommandStreamSink): () => void {
    return this.failLater(new NotInRecordingError('commands'), sink);
  }

  // ── locations ─────────────────────────────────────────────────────────────

  // This format version records no positions. A selector that names no location series
  // asks for nothing, and one that resolves to no device has nothing to place: both get the
  // empty snapshot, as from the hub. Naming a location series for devices that exist is a
  // request for data the recording does not have, which is an error, never an empty map.
  subscribeLocations(subscription: LocationSubscription, sink: LocationStreamSink): () => void {
    const ds = subscription.datasource;
    let tokens: string[] = [];
    if (ds?.location) {
      try {
        tokens = this.resolve(ds).map((g) => g.deviceToken);
      } catch (err) {
        return this.failLater(err, sink);
      }
      if (tokens.length > 0) {
        return this.failLater(new NotInRecordingError('locations', 'no positions were recorded'), sink);
      }
    }
    return this.open({
      start: (_cursor, isDisposed) => {
        if (!isDisposed()) sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
      },
      advance: () => {},
    });
  }

  // ── availability ──────────────────────────────────────────────────────────

  // A device selector is available iff the run has that device. Anchors, unbound slots and
  // no datasource have a legitimate empty state and are available, as on the hub.
  async isDatasourceAvailable(datasource: DatasourceSelector | undefined): Promise<boolean> {
    if (datasource?.kind === 'device' && datasource.deviceToken) return this.devices.has(datasource.deviceToken);
    return true;
  }

  // Stop every subscription and leave the clock. Call when the view is replaced.
  disposeAll(): void {
    this.unlisten();
    for (const sub of [...this.subs]) sub.dispose();
  }
}

function toAlarmRow(a: FoldedAlarm): AlarmRow {
  return {
    token: a.id,
    originatorType: 'device',
    originatorToken: a.dev,
    alarmKey: a.key,
    metricKey: a.metric,
    state: a.state,
    acknowledged: a.ack,
    severity: a.sev,
    raisedTime: a.raised,
    clearedTime: a.cleared,
    // The recording notes THAT an alarm was acknowledged, not when or by whom.
    acknowledgedTime: null,
    acknowledgedBy: null,
    // The stored alarm's last value is not recorded, and a nearby measurement would
    // invent the integrator's state. Null renders as an em dash.
    lastValue: null,
  };
}
