// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The recorded-run format: one captured run of a board, as a plain JSON document a
// RecordedDataSource can play back. This file owns the SHAPE and the parser; the playback
// machinery is in recorded.ts.
//
// 🔴 THE PARSER IS FAIL-CLOSED, AND THAT IS ITS WHOLE JOB. A recording is data produced by
// another tool and published as a static file, so the reader is the last place a bad one
// can be stopped before it is shown to a person as "what the platform reported". An
// unknown key, a wrong version, a ragged column, an unsorted time axis or a value of the
// wrong type is therefore a thrown RecordingFormatError that names the path, never a
// best-effort read. In particular an unknown key is refused rather than ignored: an
// ignored key is a field somebody believed was being honoured.
//
// Layout. Everything is keyed by `t`, in milliseconds since the run started, which is the
// moment the recording viewer APPLIED an update (the first moment a viewer could have seen
// it). That is not the event's own time: each row also carries `occ`, the platform's
// occurred-time as a millisecond offset from the same origin, and that is what a widget
// shows on its time axis. Series are columnar (parallel arrays) so a long run stays small.
//
// What a recording does NOT hold is declared, not implied: `channels` says which data
// channels were recorded at all, and a player must treat an undeclared channel as "not in
// this recording" rather than as empty. An empty answer would read as a fact about the
// machines ("none has a position") when it is a fact about the recording.

export const RECORDING_FORMAT_VERSION = 1;

const ALARM_STATES = ['ACTIVE', 'CLEARED'] as const;
const ALARM_SEVERITIES = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'] as const;
const ANCHOR_TARGET_TYPES = ['customer', 'area', 'asset'] as const;

export class RecordingFormatError extends Error {
  constructor(
    public readonly path: string,
    reason: string,
  ) {
    super(`invalid recording at ${path}: ${reason}`);
    this.name = 'RecordingFormatError';
  }
}

// One simulated-clock segment of the run: from `fromMs` on, the platform's clock advanced
// at `scale` times real time (1 = real time). Disclosed by the host; no playback logic
// depends on it.
export interface RecordedClockSegment {
  fromMs: number;
  scale: number;
}

// An anchor (customer / area / asset) and the devices that were its members during the run.
// This is what a dashboard's anchor-bound widgets expand to.
export interface RecordedAnchor {
  relationship: string;
  targetType: 'customer' | 'area' | 'asset';
  targetToken: string;
  members: string[];
}

// Which channels the run recorded. `measurements` lists the series NAMES recorded;
// `alarms` / `locations` say whether that channel was recorded at all. `commands` is
// always false in this version: the recording holds no command data, and the parser
// refuses a document that claims otherwise rather than play back a channel it cannot.
export interface RecordedChannels {
  measurements: string[];
  alarms: boolean;
  locations: boolean;
  commands: false;
}

// One measurement series: every row of one named measurement of one device.
export interface RecordedMeasurementSeries {
  device: string;
  name: string;
  t: number[];
  occ: number[];
  v: Array<number | null>;
}

// One row of alarm history: the FULL state of one alarm as of `t`. The alarms visible at
// a cursor are the latest row per token with `t <= cursor`; the initial set at the start
// of the run is simply rows with `t = 0`.
export interface RecordedAlarmRow {
  t: number;
  token: string;
  device: string;
  alarmKey: string;
  metricKey: string;
  state: (typeof ALARM_STATES)[number];
  severity: (typeof ALARM_SEVERITIES)[number];
  acknowledged: boolean;
  raised: number;
  cleared: number | null;
  acked: number | null;
}

// One device's position track. Latitude and longitude are required (a recording that has
// no coordinates declares `channels.locations: false` instead of writing nulls); the
// other fields are nullable because a receiver reports what it knows.
export interface RecordedLocationSeries {
  device: string;
  t: number[];
  occ: number[];
  lat: number[];
  lon: number[];
  elevation: Array<number | null>;
  speed: Array<number | null>;
  heading: Array<number | null>;
}

export interface RecordedChapter {
  tMs: number;
  title: string;
  note?: string;
}

export interface BoardRecording {
  formatVersion: typeof RECORDING_FORMAT_VERSION;
  runId: string;
  startedAtUtc: string;
  platformVersion: string;
  buildSha: string;
  durationMs: number;
  clock: RecordedClockSegment[];
  devices: string[];
  anchors: RecordedAnchor[];
  channels: RecordedChannels;
  boardHash: string;
  sourceHashes: Record<string, string>;
  chapters: RecordedChapter[];
  measurements: RecordedMeasurementSeries[];
  alarms: RecordedAlarmRow[];
  locations: RecordedLocationSeries[];
}

// ---- parser ---------------------------------------------------------------

type Obj = Record<string, unknown>;

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// strict returns `v` as an object after checking it carries every required key, no key
// outside required + optional. Own-key checks only, so a prototype name cannot satisfy one.
function strict(v: unknown, path: string, required: readonly string[], optional: readonly string[] = []): Obj {
  if (!isObj(v)) throw new RecordingFormatError(path, 'expected an object');
  const allowed = new Set([...required, ...optional]);
  for (const key of Object.keys(v)) {
    if (!allowed.has(key)) throw new RecordingFormatError(`${path}.${key}`, 'unknown key');
  }
  for (const key of required) {
    if (!Object.prototype.hasOwnProperty.call(v, key)) {
      throw new RecordingFormatError(`${path}.${key}`, 'missing required key');
    }
  }
  return v;
}

function str(v: unknown, path: string): string {
  if (typeof v !== 'string' || v === '') throw new RecordingFormatError(path, 'expected a non-empty string');
  return v;
}

function bool(v: unknown, path: string): boolean {
  if (typeof v !== 'boolean') throw new RecordingFormatError(path, 'expected a boolean');
  return v;
}

function num(v: unknown, path: string): number {
  if (typeof v !== 'number' || !Number.isFinite(v)) throw new RecordingFormatError(path, 'expected a finite number');
  return v;
}

function numOrNull(v: unknown, path: string): number | null {
  return v === null ? null : num(v, path);
}

function arr(v: unknown, path: string): unknown[] {
  if (!Array.isArray(v)) throw new RecordingFormatError(path, 'expected an array');
  return v;
}

function oneOf<T extends string>(v: unknown, path: string, allowed: readonly T[]): T {
  if (typeof v !== 'string' || !(allowed as readonly string[]).includes(v)) {
    throw new RecordingFormatError(path, `expected one of ${allowed.join(', ')}`);
  }
  return v as T;
}

const HASH = /^[0-9a-f]{64}$/;
function hash(v: unknown, path: string): string {
  if (typeof v !== 'string' || !HASH.test(v)) throw new RecordingFormatError(path, 'expected a lowercase hex SHA-256');
  return v;
}

function instant(v: unknown, path: string): string {
  const s = str(v, path);
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/.test(s) || Number.isNaN(Date.parse(s))) {
    throw new RecordingFormatError(path, 'expected an RFC 3339 UTC instant ending in Z');
  }
  return s;
}

// column parses one parallel array and checks its length against the others in its record.
function column<T>(v: unknown, path: string, item: (x: unknown, p: string) => T, length?: number): T[] {
  const a = arr(v, path);
  if (length !== undefined && a.length !== length) {
    throw new RecordingFormatError(path, `length ${a.length} does not match the time axis (${length})`);
  }
  return a.map((x, i) => item(x, `${path}[${i}]`));
}

// timeAxis parses a `t` column: finite, within the run, and never decreasing.
function timeAxis(v: unknown, path: string, durationMs: number): number[] {
  const t = column(v, path, num);
  let prev = 0;
  t.forEach((x, i) => {
    if (x < 0 || x > durationMs) throw new RecordingFormatError(`${path}[${i}]`, `${x} is outside the run [0, ${durationMs}]`);
    if (x < prev) throw new RecordingFormatError(`${path}[${i}]`, 'time axis must not decrease');
    prev = x;
  });
  return t;
}

export function parseBoardRecording(input: unknown): BoardRecording {
  const root = strict(input, '$', [
    'formatVersion',
    'runId',
    'startedAtUtc',
    'platformVersion',
    'buildSha',
    'durationMs',
    'clock',
    'devices',
    'anchors',
    'channels',
    'boardHash',
    'sourceHashes',
    'chapters',
    'measurements',
    'alarms',
    'locations',
  ]);

  if (root.formatVersion !== RECORDING_FORMAT_VERSION) {
    throw new RecordingFormatError(
      '$.formatVersion',
      `unsupported version ${JSON.stringify(root.formatVersion)}; this reader understands ${RECORDING_FORMAT_VERSION}`,
    );
  }
  const durationMs = num(root.durationMs, '$.durationMs');
  if (durationMs <= 0) throw new RecordingFormatError('$.durationMs', 'must be positive');

  const buildSha = str(root.buildSha, '$.buildSha');
  if (!/^[0-9a-f]{7,40}$/.test(buildSha)) throw new RecordingFormatError('$.buildSha', 'expected a lowercase hex commit id');

  const clock = column(root.clock, '$.clock', (x, p): RecordedClockSegment => {
    const o = strict(x, p, ['fromMs', 'scale']);
    const scale = num(o.scale, `${p}.scale`);
    if (scale <= 0) throw new RecordingFormatError(`${p}.scale`, 'must be positive');
    return { fromMs: num(o.fromMs, `${p}.fromMs`), scale };
  });
  if (clock.length === 0 || clock[0].fromMs !== 0) {
    throw new RecordingFormatError('$.clock', 'must start with a segment at fromMs 0');
  }
  clock.forEach((seg, i) => {
    if (i > 0 && seg.fromMs <= clock[i - 1].fromMs) throw new RecordingFormatError(`$.clock[${i}].fromMs`, 'must increase');
  });

  const devices = column(root.devices, '$.devices', str);
  const deviceSet = new Set(devices);
  if (deviceSet.size !== devices.length) throw new RecordingFormatError('$.devices', 'duplicate device token');
  const requireDevice = (token: string, path: string): void => {
    if (!deviceSet.has(token)) throw new RecordingFormatError(path, `device '${token}' is not in $.devices`);
  };

  const anchors = column(root.anchors, '$.anchors', (x, p): RecordedAnchor => {
    const o = strict(x, p, ['relationship', 'targetType', 'targetToken', 'members']);
    const members = column(o.members, `${p}.members`, str);
    members.forEach((m, i) => requireDevice(m, `${p}.members[${i}]`));
    return {
      relationship: str(o.relationship, `${p}.relationship`),
      targetType: oneOf(o.targetType, `${p}.targetType`, ANCHOR_TARGET_TYPES),
      targetToken: str(o.targetToken, `${p}.targetToken`),
      members,
    };
  });

  const ch = strict(root.channels, '$.channels', ['measurements', 'alarms', 'locations', 'commands']);
  if (ch.commands !== false) {
    throw new RecordingFormatError('$.channels.commands', 'this format records no command data; must be false');
  }
  const channels: RecordedChannels = {
    measurements: column(ch.measurements, '$.channels.measurements', str),
    alarms: bool(ch.alarms, '$.channels.alarms'),
    locations: bool(ch.locations, '$.channels.locations'),
    commands: false,
  };
  const measurementNames = new Set(channels.measurements);

  const sh = root.sourceHashes;
  if (!isObj(sh)) throw new RecordingFormatError('$.sourceHashes', 'expected an object');
  const sourceHashes: Record<string, string> = {};
  for (const key of Object.keys(sh)) {
    // Assigning '__proto__' on a plain object swaps its prototype instead of adding a key.
    if (key === '__proto__') throw new RecordingFormatError(`$.sourceHashes.${key}`, 'reserved key');
    sourceHashes[key] = hash(sh[key], `$.sourceHashes.${key}`);
  }

  const chapters = column(root.chapters, '$.chapters', (x, p): RecordedChapter => {
    const o = strict(x, p, ['tMs', 'title'], ['note']);
    const tMs = num(o.tMs, `${p}.tMs`);
    if (tMs < 0 || tMs > durationMs) throw new RecordingFormatError(`${p}.tMs`, 'is outside the run');
    const chapter: RecordedChapter = { tMs, title: str(o.title, `${p}.title`) };
    if (o.note !== undefined) chapter.note = str(o.note, `${p}.note`);
    return chapter;
  });

  const seen = new Set<string>();
  const measurements = column(root.measurements, '$.measurements', (x, p): RecordedMeasurementSeries => {
    const o = strict(x, p, ['device', 'name', 't', 'occ', 'v']);
    const device = str(o.device, `${p}.device`);
    requireDevice(device, `${p}.device`);
    const name = str(o.name, `${p}.name`);
    if (!measurementNames.has(name)) {
      throw new RecordingFormatError(`${p}.name`, `'${name}' is not declared in $.channels.measurements`);
    }
    const key = `${device}\u0000${name}`;
    if (seen.has(key)) throw new RecordingFormatError(p, `duplicate series for ${device}/${name}`);
    seen.add(key);
    const t = timeAxis(o.t, `${p}.t`, durationMs);
    return {
      device,
      name,
      t,
      occ: column(o.occ, `${p}.occ`, num, t.length),
      v: column(o.v, `${p}.v`, numOrNull, t.length),
    };
  });

  const alarms = column(root.alarms, '$.alarms', (x, p): RecordedAlarmRow => {
    const o = strict(x, p, [
      't', 'token', 'device', 'alarmKey', 'metricKey', 'state', 'severity', 'acknowledged', 'raised', 'cleared', 'acked',
    ]);
    const t = num(o.t, `${p}.t`);
    if (t < 0 || t > durationMs) throw new RecordingFormatError(`${p}.t`, 'is outside the run');
    const device = str(o.device, `${p}.device`);
    requireDevice(device, `${p}.device`);
    const state = oneOf(o.state, `${p}.state`, ALARM_STATES);
    const acknowledged = bool(o.acknowledged, `${p}.acknowledged`);
    const cleared = numOrNull(o.cleared, `${p}.cleared`);
    const acked = numOrNull(o.acked, `${p}.acked`);
    if ((state === 'CLEARED') !== (cleared !== null)) {
      throw new RecordingFormatError(`${p}.cleared`, 'must be set exactly when the state is CLEARED');
    }
    if (acknowledged !== (acked !== null)) {
      throw new RecordingFormatError(`${p}.acked`, 'must be set exactly when acknowledged is true');
    }
    return {
      t,
      token: str(o.token, `${p}.token`),
      device,
      alarmKey: str(o.alarmKey, `${p}.alarmKey`),
      metricKey: str(o.metricKey, `${p}.metricKey`),
      state,
      severity: oneOf(o.severity, `${p}.severity`, ALARM_SEVERITIES),
      acknowledged,
      raised: num(o.raised, `${p}.raised`),
      cleared,
      acked,
    };
  });
  alarms.forEach((row, i) => {
    if (i > 0 && row.t < alarms[i - 1].t) throw new RecordingFormatError(`$.alarms[${i}].t`, 'alarm rows must not decrease in time');
  });
  if (alarms.length > 0 && !channels.alarms) {
    throw new RecordingFormatError('$.alarms', 'rows present but $.channels.alarms is false');
  }

  const seenLoc = new Set<string>();
  const locations = column(root.locations, '$.locations', (x, p): RecordedLocationSeries => {
    const o = strict(x, p, ['device', 't', 'occ', 'lat', 'lon', 'elevation', 'speed', 'heading']);
    const device = str(o.device, `${p}.device`);
    requireDevice(device, `${p}.device`);
    if (seenLoc.has(device)) throw new RecordingFormatError(p, `duplicate location series for ${device}`);
    seenLoc.add(device);
    const t = timeAxis(o.t, `${p}.t`, durationMs);
    const lat = column(o.lat, `${p}.lat`, num, t.length);
    const lon = column(o.lon, `${p}.lon`, num, t.length);
    lat.forEach((x, i) => {
      if (x < -90 || x > 90) throw new RecordingFormatError(`${p}.lat[${i}]`, 'latitude outside [-90, 90]');
    });
    lon.forEach((x, i) => {
      if (x < -180 || x > 180) throw new RecordingFormatError(`${p}.lon[${i}]`, 'longitude outside [-180, 180]');
    });
    return {
      device,
      t,
      occ: column(o.occ, `${p}.occ`, num, t.length),
      lat,
      lon,
      elevation: column(o.elevation, `${p}.elevation`, numOrNull, t.length),
      speed: column(o.speed, `${p}.speed`, numOrNull, t.length),
      heading: column(o.heading, `${p}.heading`, numOrNull, t.length),
    };
  });
  if (locations.length > 0 && !channels.locations) {
    throw new RecordingFormatError('$.locations', 'series present but $.channels.locations is false');
  }

  return {
    formatVersion: RECORDING_FORMAT_VERSION,
    runId: str(root.runId, '$.runId'),
    startedAtUtc: instant(root.startedAtUtc, '$.startedAtUtc'),
    platformVersion: str(root.platformVersion, '$.platformVersion'),
    buildSha,
    durationMs,
    clock,
    devices,
    anchors,
    channels,
    boardHash: hash(root.boardHash, '$.boardHash'),
    sourceHashes,
    chapters,
    measurements,
    alarms,
    locations,
  };
}
