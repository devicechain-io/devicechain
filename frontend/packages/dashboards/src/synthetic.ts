// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// SyntheticDataSource — an offline, client-side data source for the dashboard
// PREVIEW mode (ADR-039). It implements the same WidgetDataSource contract the live
// DashboardHub does, so the renderer and widgets consume it unchanged — but instead
// of subscribing to backend telemetry it generates values from a chosen waveform.
// This lets an author validate layout, scales, and thresholds before any device has
// reported (and it works for ANY selector, including a slot with no binding — which
// the live hub renders empty — because it only reads the datasource's measurement
// names, never resolves
// a device).

import type {
  AlarmStreamSink,
  AlarmSubscription,
  CommandDispatch,
  CommandStreamSink,
  CommandSubscription,
  LocationStreamSink,
  LocationSubscription,
  WidgetActions,
  WidgetDataSource,
  WidgetStreamSink,
} from './hub';
import type {
  AlarmRow,
  CommandRow,
  DatasourceSelector,
  LocationSample,
  MeasurementSample,
} from './types';

// The waveforms an author can preview with. Sine is the default (smooth, obviously
// synthetic); ramp is a sawtooth; random-walk drifts within range.
export type SyntheticGenerator = 'sine' | 'ramp' | 'random-walk';

// Presentation list for a generator picker (value + human label).
export const SYNTHETIC_GENERATORS: ReadonlyArray<{ value: SyntheticGenerator; label: string }> = [
  { value: 'sine', label: 'Sine wave' },
  { value: 'ramp', label: 'Ramp' },
  { value: 'random-walk', label: 'Random walk' },
];

export interface SyntheticDataSourceConfig {
  generator?: SyntheticGenerator;
  // Emit cadence in ms (also the backfill spacing). Default 1s.
  intervalMs?: number;
  // How many past points to backfill on subscribe so a chart shows a full waveform
  // immediately instead of drawing in one tick at a time. Default 60.
  backfill?: number;
  // The value range the waveforms span. Default 0..100.
  min?: number;
  // Default 100.
  max?: number;
  // Period of one sine/ramp cycle in ms. Default 60s.
  periodMs?: number;
}

// A measurement name for a selector that lists none (empty = "all" on the live hub);
// gives cards/gauges/charts something to render in preview.
const DEFAULT_NAME = 'value';

// A canonical spread of synthetic alarms (one per severity, mixed state/ack) so an
// author previewing an alarm table/count sees populated, representative rows before any
// real alarm has raised. The filter on the subscription is applied so the preview
// reflects the widget's configured scope-of-interest.
const SYNTHETIC_ALARMS: ReadonlyArray<
  Pick<
    AlarmRow,
    'severity' | 'state' | 'acknowledged' | 'alarmKey' | 'metricKey' | 'lastValue' | 'originatorToken' | 'message'
  >
> = [
  { severity: 'CRITICAL', state: 'ACTIVE', acknowledged: false, alarmKey: 'over-temperature', metricKey: 'temperature', lastValue: 87.4, originatorToken: 'thermostat-01', message: 'Temperature above 85°C' },
  { severity: 'MAJOR', state: 'ACTIVE', acknowledged: true, alarmKey: 'low-battery', metricKey: 'battery', lastValue: 12, originatorToken: 'sensor-14', message: 'Battery below 15%' },
  { severity: 'MINOR', state: 'ACTIVE', acknowledged: false, alarmKey: 'humidity-high', metricKey: 'humidity', lastValue: 78, originatorToken: 'sensor-03', message: 'Relative humidity above threshold' },
  { severity: 'WARNING', state: 'CLEARED', acknowledged: true, alarmKey: 'signal-weak', metricKey: 'rssi', lastValue: -89, originatorToken: 'gateway-02', message: 'Weak uplink signal' },
  { severity: 'INDETERMINATE', state: 'ACTIVE', acknowledged: false, alarmKey: 'self-test', metricKey: 'status', lastValue: null, originatorToken: 'device-99', message: null },
];

// A canonical spread of synthetic commands (one per lifecycle stage) so an author
// previewing a command-button sees a populated, representative history — an in-flight
// command, one withheld for an absent device, a completed one, a failure, a cancellation
// — before any real command has been issued.
//
// `dispatched` says whether this command was ever actually put on the wire, which is
// what decides sentTime below. It is a per-fixture fact, not something derivable from
// the status: a CANCELLED command may have been called off while still held (never
// sent) or after dispatch, and the preview picks the former.
const SYNTHETIC_COMMANDS: ReadonlyArray<
  Pick<CommandRow, 'name' | 'status' | 'payload' | 'responsePayload' | 'error'> & { dispatched: boolean }
> = [
  { name: 'reboot', status: 'SENT', payload: '{"delaySeconds":5}', responsePayload: null, error: null, dispatched: true },
  // Early in the list on purpose: an author can cap the widget's row count, and a
  // withheld command is the state a preview most needs to show.
  { name: 'self-test', status: 'HELD', payload: null, responsePayload: null, error: null, dispatched: false },
  { name: 'set-interval', status: 'SUCCESSFUL', payload: '{"seconds":30}', responsePayload: '{"ok":true}', error: null, dispatched: true },
  { name: 'calibrate', status: 'QUEUED', payload: null, responsePayload: null, error: null, dispatched: false },
  { name: 'firmware-update', status: 'FAILED', payload: '{"version":"2.1.0"}', responsePayload: null, error: 'device offline', dispatched: true },
  { name: 'open-valve', status: 'CANCELLED', payload: '{"percent":100}', responsePayload: null, error: null, dispatched: false },
];

// The device a synthetic command-button reports as its target, so the Send control
// renders (a real widget needs a bound device to issue against).
const SYNTHETIC_COMMAND_DEVICE = 'synthetic-device';

// A canonical spread of synthetic positions so an author previewing a map sees markers
// laid out the way a real fleet would be — several devices a short distance apart, at a
// zoom a map actually renders at, rather than one dot in the middle.
//
// 🔴 THE OPTIONALS ARE DELIBERATELY MIXED, INCLUDING NULLS AND A GENUINE ZERO. Preview
// is where an author decides what a marker looks like, so it must show the real range:
// `sp-loader-03` reports no heading and no speed at all (a receiver that does not supply
// them), while `sp-dozer-01` reports speed 0 — parked, which is a reading, not an
// absence. A preview that filled every optional in would teach an author that a marker
// always has a heading, and the first real fleet would prove otherwise.
const SYNTHETIC_LOCATIONS: ReadonlyArray<
  Pick<LocationSample, 'deviceToken' | 'latitude' | 'longitude' | 'elevation' | 'accuracy' | 'speed' | 'heading'>
> = [
  { deviceToken: 'sp-dozer-01', latitude: 33.749, longitude: -84.388, elevation: 320.5, accuracy: 4.2, speed: 0, heading: 271.5 },
  { deviceToken: 'sp-excavator-02', latitude: 33.7512, longitude: -84.3858, elevation: 318.1, accuracy: 3.1, speed: 1.4, heading: 88 },
  { deviceToken: 'sp-loader-03', latitude: 33.7468, longitude: -84.3903, elevation: null, accuracy: 9.8, speed: null, heading: null },
  { deviceToken: 'sp-truck-04', latitude: 33.7481, longitude: -84.3841, elevation: 315.9, accuracy: 2.5, speed: 8.3, heading: 12.25 },
];

// Deterministic small hash of a name → a stable phase offset, so multiple series on
// one dashboard are visibly out of phase rather than overlapping identically.
function hashName(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (Math.imul(h, 31) + name.charCodeAt(i)) | 0;
  return h >>> 0;
}

function clamp(v: number, min: number, max: number): number {
  return v < min ? min : v > max ? max : v;
}

export class SyntheticDataSource implements WidgetDataSource, WidgetActions {
  private readonly generator: SyntheticGenerator;
  private readonly intervalMs: number;
  private readonly backfill: number;
  private readonly min: number;
  private readonly max: number;
  private readonly periodMs: number;
  // Live timers, tracked so disposeAll() can stop every widget's stream at once.
  private readonly timers = new Set<ReturnType<typeof setInterval>>();

  constructor(config: SyntheticDataSourceConfig = {}) {
    this.generator = config.generator ?? 'sine';
    // Guard the divisors/counts so a misconfigured host can't produce NaN values
    // (periodMs:0 → tMs/0) or a zero-delay flood: intervalMs/periodMs floor at 1ms,
    // backfill can't go negative.
    this.intervalMs = Math.max(1, config.intervalMs ?? 1000);
    this.backfill = Math.max(0, config.backfill ?? 60);
    this.min = config.min ?? 0;
    this.max = config.max ?? 100;
    this.periodMs = Math.max(1, config.periodMs ?? 60_000);
  }

  subscribeWidget(datasource: DatasourceSelector, sink: WidgetStreamSink): () => void {
    const names = datasource.measurements.length > 0 ? datasource.measurements : [DEFAULT_NAME];
    // Per-name random-walk state, private to this subscription so two widgets don't
    // share (and corrupt) each other's walk.
    const walk = new Map<string, number>();
    let seq = 0;

    const emit = (name: string, tMs: number): void => {
      const value = this.valueFor(name, tMs, walk);
      const s: MeasurementSample = {
        id: `syn-${seq++}`,
        deviceToken: 'synthetic',
        eventType: 0,
        occurredTime: new Date(tMs).toISOString(),
        name,
        value,
        classifier: null,
      };
      sink.next(s);
    };

    // Backfill oldest → newest so the widget window is chronological (and the
    // random-walk advances forward through the backfilled points).
    const now = Date.now();
    for (let i = this.backfill - 1; i >= 0; i--) {
      const tMs = now - i * this.intervalMs;
      for (const name of names) emit(name, tMs);
    }

    const timer = setInterval(() => {
      const tMs = Date.now();
      for (const name of names) emit(name, tMs);
    }, this.intervalMs);
    this.timers.add(timer);

    return () => {
      if (this.timers.delete(timer)) clearInterval(timer);
    };
  }

  // subscribeAlarms emits a synthetic alarm snapshot for preview. The canonical set is
  // filtered by the subscription (state/severity/ack) so the preview reflects what the
  // widget is configured to show; scope (datasource) is ignored — preview never resolves
  // a device. Re-emits on the same cadence with advancing raised times so the table
  // looks live. Returns whole snapshots, matching the live hub's contract.
  subscribeAlarms(subscription: AlarmSubscription, sink: AlarmStreamSink): () => void {
    const matches = SYNTHETIC_ALARMS.filter(
      (a) =>
        (!subscription.state || a.state === subscription.state) &&
        (!subscription.severity || a.severity === subscription.severity) &&
        (subscription.acknowledged == null || a.acknowledged === subscription.acknowledged),
    );

    const emit = (): void => {
      const now = Date.now();
      const rows: AlarmRow[] = matches.map((a, i) => {
        const raised = new Date(now - i * 45_000).toISOString();
        return {
          token: `syn-alarm-${i}`,
          originatorType: 'device',
          alarmKey: a.alarmKey,
          metricKey: a.metricKey,
          state: a.state,
          acknowledged: a.acknowledged,
          severity: a.severity,
          originatorToken: a.originatorToken,
          lastValue: a.lastValue,
          message: a.message,
          raisedTime: raised,
          clearedTime: a.state === 'CLEARED' ? raised : null,
          acknowledgedTime: a.acknowledged ? raised : null,
          acknowledgedBy: a.acknowledged ? 'preview@devicechain' : null,
        };
      });
      sink.next({ alarms: rows.slice(0, subscription.pageSize), total: rows.length });
    };

    emit();
    const timer = setInterval(emit, this.intervalMs);
    this.timers.add(timer);
    return () => {
      if (this.timers.delete(timer)) clearInterval(timer);
    };
  }

  // subscribeCommands emits a synthetic command history for preview so a command-button
  // shows a populated, lifecycle-varied list (and a bound target device, so its Send
  // control renders). Re-emits on the same cadence with advancing queued times. Scope
  // (datasource) is ignored — preview never resolves a device. Returns whole snapshots,
  // matching the live hub's contract.
  subscribeCommands(subscription: CommandSubscription, sink: CommandStreamSink): () => void {
    const emit = (): void => {
      const now = Date.now();
      const commands: CommandRow[] = SYNTHETIC_COMMANDS.map((c, i) => {
        const queued = new Date(now - i * 20_000).toISOString();
        // respondedTime is NOT "terminal" — it is "the DEVICE answered", which is a
        // narrower thing. TIMEOUT, EXPIRED and CANCELLED are all terminal with nothing
        // ever coming back from the device, so stamping them with a response time would
        // teach an author's layout to expect a value the live hub never supplies.
        const answered = c.status === 'SUCCESSFUL' || c.status === 'FAILED';
        return {
          token: `syn-command-${i}`,
          name: c.name,
          status: c.status,
          payload: c.payload,
          responsePayload: c.responsePayload,
          error: c.error,
          queuedTime: queued,
          // Likewise: only a command that actually went out has a sentTime. QUEUED and
          // HELD never did, and neither did the cancelled one (called off while held).
          sentTime: c.dispatched ? queued : null,
          respondedTime: answered ? queued : null,
        };
      });
      sink.next({
        deviceToken: SYNTHETIC_COMMAND_DEVICE,
        commands: commands.slice(0, subscription.pageSize),
        total: commands.length,
      });
    };

    emit();
    const timer = setInterval(emit, this.intervalMs);
    this.timers.add(timer);
    return () => {
      if (this.timers.delete(timer)) clearInterval(timer);
    };
  }

  // subscribeLocations emits a synthetic position snapshot for preview so a map shows a
  // populated, representative fleet before any device has reported one. Scope
  // (datasource) is ignored — preview never resolves a device — but the LOCATION SERIES
  // is honored: a selector that names none gets the empty snapshot, exactly as the live
  // hub gives it, so an author who has not bound the map sees preview agree with
  // production rather than paper over the omission with fake markers.
  //
  // Preview NEVER reports `forbidden`: it reaches no backend, so there is no authority
  // to be refused, and inventing a permission state would show an author a wall their
  // viewers may not actually hit.
  subscribeLocations(subscription: LocationSubscription, sink: LocationStreamSink): () => void {
    if (!subscription.datasource?.location) {
      sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
      return () => {};
    }

    const emit = (): void => {
      const now = Date.now();
      const locations: LocationSample[] = SYNTHETIC_LOCATIONS.map((l, i) => ({
        id: `syn-location-${i}`,
        deviceToken: l.deviceToken,
        latitude: l.latitude,
        longitude: l.longitude,
        elevation: l.elevation,
        accuracy: l.accuracy,
        speed: l.speed,
        heading: l.heading,
        occurredTime: new Date(now - i * 30_000).toISOString(),
      }));
      sink.next({
        kind: 'positions',
        deviceTokens: locations.map((l) => l.deviceToken),
        locations,
      });
    };

    emit();
    const timer = setInterval(emit, this.intervalMs);
    this.timers.add(timer);
    return () => {
      if (this.timers.delete(timer)) clearInterval(timer);
    };
  }

  // isDatasourceAvailable — preview always resolves data (it generates it), so every
  // datasource is "available"; an author previewing a template never sees the
  // deleted-device state.
  async isDatasourceAvailable(): Promise<boolean> {
    return true;
  }

  // ── WidgetActions (preview stubs) ────────────────────────────────────────
  // Preview shows action controls (so an author sees the real layout), so can() is
  // always true; the actions themselves are no-ops — preview never mutates the backend.
  can(): boolean {
    return true;
  }

  async acknowledgeAlarm(): Promise<void> {
    // no-op in preview
  }

  async clearAlarm(): Promise<void> {
    // no-op in preview
  }

  async sendCommand(): Promise<CommandDispatch> {
    // no-op in preview — return a stub dispatch token so the widget's optimistic UI works.
    // Preview never refuses: there is no enqueue gate behind it to decide a rejection,
    // and inventing one would be a verdict nothing actually reached.
    return { status: 'sent', token: 'syn-dispatch' };
  }

  // disposeAll stops every live stream (e.g. when preview is turned off). Individual
  // widget disposers already clear their own timer; this is the belt-and-braces
  // teardown for the whole source.
  disposeAll(): void {
    for (const timer of this.timers) clearInterval(timer);
    this.timers.clear();
  }

  private valueFor(name: string, tMs: number, walk: Map<string, number>): number {
    const span = this.max - this.min;
    const phase = (hashName(name) % 1000) / 1000; // 0..1 of a cycle
    switch (this.generator) {
      case 'ramp': {
        // Sawtooth: fraction of the period (offset per name), rising min→max.
        const frac = ((tMs / this.periodMs + phase) % 1 + 1) % 1;
        return this.min + span * frac;
      }
      case 'random-walk': {
        const prev = walk.get(name) ?? this.min + span / 2;
        const next = clamp(prev + (Math.random() - 0.5) * span * 0.1, this.min, this.max);
        walk.set(name, next);
        return next;
      }
      case 'sine':
      default: {
        const angle = 2 * Math.PI * (tMs / this.periodMs + phase);
        return this.min + span * (0.5 + 0.5 * Math.sin(angle));
      }
    }
  }
}
