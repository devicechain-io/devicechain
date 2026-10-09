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
  LocationSnapshot,
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
  LocationSample,
  MeasurementSample,
} from './types';
import type { BoardRecording, RecordedAlarmRow, RecordedLocationSeries, RecordedMeasurementSeries } from './recording';

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
  readonly durationMs: number;
  private time: number;
  private isPlaying = false;
  private playbackRate: PlaybackRate = 1;
  private stopTicker: (() => void) | null = null;
  private readonly ticker: ClockTicker;
  private readonly listeners = new Set<(ev: ClockEvent) => void>();

  constructor(durationMs: number, options: { ticker?: ClockTicker; startMs?: number } = {}) {
    if (!Number.isFinite(durationMs) || durationMs <= 0) {
      throw new RangeError(`RecordedClock: durationMs must be positive and finite, got ${durationMs}`);
    }
    this.durationMs = durationMs;
    this.ticker = options.ticker ?? defaultTicker();
    this.time = this.clampTime(options.startMs ?? 0);
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

  // seek moves the cursor, clamped to [0, durationMs]. Playback state is unchanged, except
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
    return t < 0 ? 0 : t > this.durationMs ? this.durationMs : t;
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

function anchorKey(a: AnchorTarget): string {
  return `${a.relationship}|${a.targetType}|${a.targetToken}`;
}

function anchorMembers(rec: BoardRecording, anchor: AnchorTarget): string[] {
  const hit = rec.anchors.find((a) => anchorKey(a) === anchorKey(anchor));
  if (!hit) {
    throw new Error(
      `anchor ${anchor.targetType} '${anchor.targetToken}' (${anchor.relationship}) is not in this recording`,
    );
  }
  return [...hit.members];
}

// ---- resolver and lister --------------------------------------------------

// createRecordedResolver answers the hub's DeviceResolver questions from the recording:
// an anchor expands to the members the run recorded, and a device exists if the run has
// it. An anchor the recording does not know THROWS (an empty answer would read as "the
// site had no machines").
export function createRecordedResolver(rec: BoardRecording): DeviceResolver {
  const devices = new Set(rec.devices);
  return {
    devicesForAnchor: async (anchor) => anchorMembers(rec, anchor),
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
// list the anchor targets the run recorded; a kind with none yields an empty list, which
// is true of the recording.
export function createRecordedLister(rec: BoardRecording): EntityCandidateLister {
  return async (kind: EntityListKind) => {
    if (kind === 'device') return rec.devices.map((token) => ({ token, name: null }));
    const seen = new Set<string>();
    const rows: Array<{ token: string; name: null }> = [];
    for (const a of rec.anchors) {
      if (a.targetType !== kind || seen.has(a.targetToken)) continue;
      seen.add(a.targetToken);
      rows.push({ token: a.targetToken, name: null });
    }
    return rows;
  };
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
  private readonly startMs: number;
  private readonly series = new Map<string, RecordedMeasurementSeries[]>();
  private readonly locationSeries = new Map<string, RecordedLocationSeries>();
  private readonly devices: ReadonlySet<string>;
  private readonly subs = new Set<Subscription>();
  private readonly unlisten: () => void;
  private cursor: number;
  private stale = false;

  constructor(rec: BoardRecording, clock: RecordedClock) {
    this.rec = rec;
    this.cursor = clock.timeMs;
    this.startMs = Date.parse(rec.startedAtUtc);
    this.devices = new Set(rec.devices);
    for (const s of rec.measurements) {
      const list = this.series.get(s.device);
      if (list) list.push(s);
      else this.series.set(s.device, [s]);
    }
    for (const s of rec.locations) this.locationSeries.set(s.device, s);
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
        return anchorMembers(this.rec, ds.anchor).map((deviceToken) => ({
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

    // Each selected series keeps its own next-row index, so a tick delivers exactly the
    // rows the cursor has newly passed.
    const picked: Array<{ s: RecordedMeasurementSeries; next: number }> = [];
    for (const g of groups) {
      for (const s of this.series.get(g.deviceToken) ?? []) {
        if (g.names.size === 0 || g.names.has(s.name)) picked.push({ s, next: 0 });
      }
    }

    const flush = (cursor: number, isDisposed: () => boolean): void => {
      const due: Array<{ t: number; sample: MeasurementSample }> = [];
      for (const p of picked) {
        const { s } = p;
        while (p.next < s.t.length && s.t[p.next] <= cursor) {
          const i = p.next++;
          due.push({
            t: s.t[i],
            sample: {
              id: `${s.device}|${s.name}|${i}`,
              deviceToken: s.device,
              eventType: 0,
              occurredTime: this.iso(s.occ[i]),
              name: s.name,
              value: s.v[i],
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
        for (const p of picked) p.next = lowerBound(p.s.t, cursor - RECORDED_HISTORY_WINDOW_MS);
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

    // The fold is monotone because a view only moves forward: apply each alarm row once,
    // in order, keeping the latest row per token.
    const rows = this.rec.alarms;
    const times = rows.map((r) => r.t);
    const latest = new Map<string, RecordedAlarmRow>();
    let applied = 0;
    let lastSignature: string | null = null;

    const emit = (cursor: number, isDisposed: () => boolean): void => {
      const upTo = upperBound(times, cursor);
      for (; applied < upTo; applied++) latest.set(rows[applied].token, rows[applied]);
      const matching = [...latest.values()].filter(
        (r) =>
          (scope === null || scope.has(r.device)) &&
          (!subscription.state || r.state === subscription.state) &&
          (!subscription.severity || r.severity === subscription.severity) &&
          (subscription.acknowledged == null || r.acknowledged === subscription.acknowledged),
      );
      // Newest first by raised time; the token breaks ties so the order is deterministic.
      matching.sort((a, b) => b.raised - a.raised || (a.token < b.token ? -1 : a.token > b.token ? 1 : 0));
      const page = matching.slice(0, subscription.pageSize);
      // Whole snapshots, re-emitted only when the fold actually changed.
      const signature = JSON.stringify([matching.length, page]);
      if (signature === lastSignature || isDisposed()) return;
      lastSignature = signature;
      const snapshot: AlarmSnapshot = { alarms: page.map((r) => this.alarmRow(r)), total: matching.length };
      sink.next(snapshot);
    };

    return this.open({ start: emit, advance: emit });
  }

  private alarmRow(r: RecordedAlarmRow): AlarmRow {
    return {
      token: r.token,
      originatorType: 'device',
      originatorToken: r.device,
      alarmKey: r.alarmKey,
      metricKey: r.metricKey,
      state: r.state,
      acknowledged: r.acknowledged,
      severity: r.severity,
      raisedTime: this.iso(r.raised),
      clearedTime: r.cleared === null ? null : this.iso(r.cleared),
      acknowledgedTime: r.acked === null ? null : this.iso(r.acked),
      acknowledgedBy: null,
      // The stored alarm's last value is not recorded, and a nearby measurement would
      // invent the integrator's state. Null renders as an em dash.
      lastValue: null,
    };
  }

  // ── commands ──────────────────────────────────────────────────────────────

  // The recording holds no command data, so this channel is never available.
  subscribeCommands(_subscription: CommandSubscription, sink: CommandStreamSink): () => void {
    return this.failLater(new NotInRecordingError('commands'), sink);
  }

  // ── locations ─────────────────────────────────────────────────────────────

  subscribeLocations(subscription: LocationSubscription, sink: LocationStreamSink): () => void {
    const ds = subscription.datasource;
    // A selector that names no location series asks for nothing: the empty snapshot, as
    // the hub gives it. Naming one against a recording without positions is NOT empty.
    if (!ds?.location) {
      return this.open({
        start: (_cursor, isDisposed) => {
          if (!isDisposed()) sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
        },
        advance: () => {},
      });
    }
    let tokens: string[];
    try {
      if (!this.rec.channels.locations) throw new NotInRecordingError('locations', 'no positions were recorded');
      tokens = this.resolve(ds).map((g) => g.deviceToken);
    } catch (err) {
      return this.failLater(err, sink);
    }

    // Positions are stepped, never interpolated: the last row at or before the cursor.
    let lastSignature: string | null = null;
    const emit = (cursor: number, isDisposed: () => boolean): void => {
      const locations: LocationSample[] = [];
      const picks: string[] = [];
      for (const token of tokens) {
        const s = this.locationSeries.get(token);
        if (!s) continue;
        const i = upperBound(s.t, cursor) - 1;
        if (i < 0) continue;
        picks.push(`${token}:${i}`);
        locations.push({
          id: `${token}|location|${i}`,
          deviceToken: token,
          latitude: s.lat[i],
          longitude: s.lon[i],
          elevation: s.elevation[i],
          // Accuracy is not recorded; absent is not zero.
          accuracy: null,
          speed: s.speed[i],
          heading: s.heading[i],
          occurredTime: this.iso(s.occ[i]),
        });
      }
      const signature = picks.join(',');
      if (signature === lastSignature || isDisposed()) return;
      lastSignature = signature;
      const snapshot: LocationSnapshot = { kind: 'positions', deviceTokens: tokens, locations };
      sink.next(snapshot);
    };
    return this.open({ start: emit, advance: emit });
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
