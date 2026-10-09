// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board-recording format: one captured run of a board, as a plain JSON document a
// RecordedDataSource can play back. This file owns the SHAPE and the parser; playback is in
// recorded.ts. The document is produced by the Sitepulse recording converter
// (demos/sitepulse-unity/tools/recording_to_board.py), whose module docstring is the
// writer's side of this contract; the two must change together.
//
// 🔴 THE PARSER IS FAIL-CLOSED, AND THAT IS ITS WHOLE JOB. A recording is data produced by
// another tool and published as a static file, so the reader is the last place a bad one
// can be stopped before it is shown to a person as "what the platform reported". An
// unknown key, a wrong kind or version, a ragged column, an unsorted time axis, a device or
// metric index out of range, or a channel claim the document cannot honour is a thrown
// RecordingFormatError naming the path, never a best-effort read. In particular an unknown
// key is refused rather than ignored: an ignored key is a field somebody believed was being
// honoured.
//
// Layout (formatVersion 1). Every time is `t`/`tMs`, in milliseconds from the START of the
// run, as the recording viewer APPLIED the update (the first moment a viewer could have
// seen it). Measurements are COLUMNS (one entry per row, parallel arrays): `d` indexes
// `devices`, `n` indexes `channels.measurements`, `t` is the time, `v` the value and `s` is
// 1 for a row the platform itself seeded at the start of the run. Alarms are a fold over
// history: `snapshots` (the whole alarm set at a moment) plus `events` (one alarm's new
// state), with ids re-minted as alarm-N. An excerpt carries `excerpt{fromMs,toMs}`: it
// holds only the measurement rows of that window, but ALL alarm history up to its end.
//
// What a recording does NOT hold is declared, not implied: `channels` says which channels
// were recorded at all. This version records no positions and no command data
// (`channels.locations` and `channels.commands` are false and `locations` is empty), and a
// player must answer those channels with "not in this recording" rather than with empty
// data, which would read as a fact about the machines.

export const RECORDING_FORMAT_VERSION = 1;
export const RECORDING_KIND = 'sitepulse-board-recording';

const ALARM_STATES = ['ACTIVE', 'CLEARED'] as const;
const ALARM_SEVERITIES = ['CRITICAL', 'MAJOR', 'MINOR', 'WARNING'] as const;

export class RecordingFormatError extends Error {
  constructor(
    public readonly path: string,
    reason: string,
  ) {
    super(`invalid recording at ${path}: ${reason}`);
    this.name = 'RecordingFormatError';
  }
}

// One segment of the platform's simulation clock: from `fromMs` on, it ran in `mode` at
// `scale` times real time (1 = real time). Disclosed by the host; playback does not use it.
export interface RecordedClockSegment {
  fromMs: number;
  mode: string;
  scale: number;
}

export interface RecordedDevice {
  id: string;
  token: string;
  kind: string;
}

export interface RecordedChannels {
  measurements: string[];
  alarms: boolean;
  // Always false in this version: the format carries no positions and no command data.
  locations: false;
  commands: false;
}

export interface RecordedChapter {
  tMs: number;
  name: string;
  note: string;
}

// The measurement rows, as parallel columns sorted by `t`.
export interface RecordedMeasurementColumns {
  d: number[];
  n: number[];
  t: number[];
  v: number[];
  s: Array<0 | 1>;
}

// One alarm as the recording saw it. `occ` is the platform's own time for THIS state: when
// the alarm was raised for an ACTIVE row, when it cleared for a CLEARED row. `ack` is
// present only when the alarm was acknowledged. `dev` is a device token.
export interface RecordedAlarm {
  id: string;
  dev: string;
  key: string;
  metric: string;
  state: (typeof ALARM_STATES)[number];
  sev: (typeof ALARM_SEVERITIES)[number];
  occ: string;
  ack?: true;
}

// A snapshot is the whole alarm set as the recorder's query returned it. `requestedAtMs` is
// when that query was SENT (it answers at `tMs`); an event applied after it is newer than
// anything the answer can say about that alarm. `total` always equals the alarms listed: a
// truncated answer cannot be played as if it were complete.
export interface RecordedAlarmSnapshot {
  tMs: number;
  requestedAtMs: number;
  total: number;
  alarms: RecordedAlarm[];
}

export interface RecordedAlarmEvent extends RecordedAlarm {
  tMs: number;
}

export interface BoardRecording {
  kind: typeof RECORDING_KIND;
  formatVersion: typeof RECORDING_FORMAT_VERSION;
  runId: string;
  startedAtUtc: string;
  durationMs: number;
  platformVersion: string;
  instance: string;
  tenant: string;
  build: { gitSha: string; sdkCommit: string };
  clock: RecordedClockSegment[];
  devices: RecordedDevice[];
  channels: RecordedChannels;
  board: { path: string; sourceCommit: string; sha256: string };
  sourceHashes: Record<string, string>;
  converter: { version: number };
  chapters: RecordedChapter[];
  excerpt?: { fromMs: number; toMs: number };
  measurements: RecordedMeasurementColumns;
  alarms: { snapshots: RecordedAlarmSnapshot[]; events: RecordedAlarmEvent[] };
  locations: [];
}

// ---- helpers that read a parsed recording ---------------------------------

// The span a player may cover: the whole run, or the excerpt's window.
export function recordingBounds(rec: BoardRecording): { fromMs: number; toMs: number } {
  return rec.excerpt ? { ...rec.excerpt } : { fromMs: 0, toMs: rec.durationMs };
}

// The wall-clock instant (epoch ms) a recording offset corresponds to. Offsets are
// measured in real elapsed time from the run's start, so this is the start plus the offset;
// an accelerated SIMULATION clock changes what the platform's own timestamps say, not how
// long the recording took (see simulationScaleAt for disclosing that).
export function recordedWallTimeMs(rec: BoardRecording, tMs: number): number {
  return Date.parse(rec.startedAtUtc) + tMs;
}

// The simulation clock's scale at a recording offset, or null before the first segment
// begins (the recorder had not yet reported its clock).
export function simulationScaleAt(rec: BoardRecording, tMs: number): { mode: string; scale: number } | null {
  let hit: RecordedClockSegment | null = null;
  for (const seg of rec.clock) {
    if (seg.fromMs <= tMs) hit = seg;
  }
  return hit ? { mode: hit.mode, scale: hit.scale } : null;
}

// ---- parser ---------------------------------------------------------------

type Obj = Record<string, unknown>;

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// strict returns `v` as an object after checking it carries every required key and no key
// outside required + optional.
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

function matching(v: unknown, path: string, re: RegExp, what: string): string {
  if (typeof v !== 'string' || !re.test(v)) throw new RecordingFormatError(path, `expected ${what}`);
  return v;
}

const HEX64 = /^[0-9a-f]{64}$/;
const HEX40 = /^[0-9a-f]{40}$/;
const HEX_SHORT = /^[0-9a-f]{7,40}$/;

const INSTANT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?Z$/;

// Strict: a date that does not exist (2026-02-30) is refused, not rolled over to March.
function instant(v: unknown, path: string): string {
  const s = str(v, path);
  const m = INSTANT.exec(s);
  if (m) {
    const [y, mo, d, h, mi, sec] = m.slice(1, 7).map(Number);
    const t = new Date(Date.UTC(y, mo - 1, d, h, mi, sec));
    if (
      t.getUTCFullYear() === y && t.getUTCMonth() === mo - 1 && t.getUTCDate() === d &&
      t.getUTCHours() === h && t.getUTCMinutes() === mi && t.getUTCSeconds() === sec
    ) {
      return s;
    }
  }
  throw new RecordingFormatError(path, 'expected an RFC 3339 UTC instant ending in Z that exists on the calendar');
}

// Orders two instants as parsed times, not as strings: "...:10Z" is BEFORE "...:10.5Z"
// although it sorts after it as text. Fractions compare to the full digits given (the
// platform writes up to 7), not just to the millisecond Date can hold.
export function compareInstants(a: string, b: string): number {
  const pa = INSTANT.exec(a);
  const pb = INSTANT.exec(b);
  if (!pa || !pb) throw new RangeError(`compareInstants: not an instant: ${pa ? b : a}`);
  const whole = (m: RegExpExecArray): number => Date.parse(`${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}Z`);
  const wa = whole(pa);
  const wb = whole(pb);
  if (wa !== wb) return wa < wb ? -1 : 1;
  const fa = (pa[7] ?? '').padEnd(12, '0');
  const fb = (pb[7] ?? '').padEnd(12, '0');
  return fa < fb ? -1 : fa > fb ? 1 : 0;
}

function column<T>(v: unknown, path: string, item: (x: unknown, p: string) => T, length?: number): T[] {
  const a = arr(v, path);
  if (length !== undefined && a.length !== length) {
    throw new RecordingFormatError(path, `length ${a.length} does not match the time column (${length})`);
  }
  return a.map((x, i) => item(x, `${path}[${i}]`));
}

function nonNegative(v: unknown, path: string): number {
  const n = num(v, path);
  if (n < 0) throw new RecordingFormatError(path, 'must not be negative');
  return n;
}

export function parseBoardRecording(input: unknown): BoardRecording {
  const root = strict(
    input,
    '$',
    [
      'kind', 'formatVersion', 'runId', 'startedAtUtc', 'durationMs', 'platformVersion', 'instance', 'tenant',
      'build', 'clock', 'devices', 'channels', 'board', 'sourceHashes', 'converter', 'chapters',
      'measurements', 'alarms', 'locations',
    ],
    ['excerpt'],
  );

  if (root.kind !== RECORDING_KIND) {
    throw new RecordingFormatError('$.kind', `expected ${JSON.stringify(RECORDING_KIND)}`);
  }
  if (root.formatVersion !== RECORDING_FORMAT_VERSION) {
    throw new RecordingFormatError(
      '$.formatVersion',
      `unsupported version ${JSON.stringify(root.formatVersion)}; this reader understands ${RECORDING_FORMAT_VERSION}`,
    );
  }
  const durationMs = num(root.durationMs, '$.durationMs');
  if (durationMs <= 0) throw new RecordingFormatError('$.durationMs', 'must be positive');

  const b = strict(root.build, '$.build', ['gitSha', 'sdkCommit']);
  const build = {
    gitSha: matching(b.gitSha, '$.build.gitSha', HEX_SHORT, 'a lowercase hex commit id'),
    sdkCommit: matching(b.sdkCommit, '$.build.sdkCommit', HEX_SHORT, 'a lowercase hex commit id'),
  };

  const clock = column(root.clock, '$.clock', (x, p): RecordedClockSegment => {
    const o = strict(x, p, ['fromMs', 'mode', 'scale']);
    const scale = num(o.scale, `${p}.scale`);
    if (scale <= 0) throw new RecordingFormatError(`${p}.scale`, 'must be positive');
    return { fromMs: nonNegative(o.fromMs, `${p}.fromMs`), mode: str(o.mode, `${p}.mode`), scale };
  });
  clock.forEach((seg, i) => {
    if (i > 0 && seg.fromMs <= clock[i - 1].fromMs) throw new RecordingFormatError(`$.clock[${i}].fromMs`, 'must increase');
  });

  const devices = column(root.devices, '$.devices', (x, p): RecordedDevice => {
    const o = strict(x, p, ['id', 'token', 'kind']);
    return { id: str(o.id, `${p}.id`), token: str(o.token, `${p}.token`), kind: str(o.kind, `${p}.kind`) };
  });
  const tokens = new Set(devices.map((d) => d.token));
  if (tokens.size !== devices.length || new Set(devices.map((d) => d.id)).size !== devices.length) {
    throw new RecordingFormatError('$.devices', 'duplicate device id or token');
  }

  const ch = strict(root.channels, '$.channels', ['measurements', 'alarms', 'locations', 'commands']);
  if (ch.locations !== false) {
    throw new RecordingFormatError('$.channels.locations', 'this version records no positions; must be false');
  }
  if (ch.commands !== false) {
    throw new RecordingFormatError('$.channels.commands', 'this version records no command data; must be false');
  }
  const channels: RecordedChannels = {
    measurements: column(ch.measurements, '$.channels.measurements', str),
    alarms: bool(ch.alarms, '$.channels.alarms'),
    locations: false,
    commands: false,
  };
  if (new Set(channels.measurements).size !== channels.measurements.length) {
    throw new RecordingFormatError('$.channels.measurements', 'duplicate measurement name');
  }

  const bd = strict(root.board, '$.board', ['path', 'sourceCommit', 'sha256']);
  const board = {
    path: str(bd.path, '$.board.path'),
    sourceCommit: matching(bd.sourceCommit, '$.board.sourceCommit', HEX40, 'a full lowercase hex commit id'),
    sha256: matching(bd.sha256, '$.board.sha256', HEX64, 'a lowercase hex SHA-256'),
  };

  const sh = root.sourceHashes;
  if (!isObj(sh)) throw new RecordingFormatError('$.sourceHashes', 'expected an object');
  const sourceHashes: Record<string, string> = {};
  for (const key of Object.keys(sh)) {
    // Assigning '__proto__' on a plain object swaps its prototype instead of adding a key.
    if (key === '__proto__') throw new RecordingFormatError(`$.sourceHashes.${key}`, 'reserved key');
    sourceHashes[key] = matching(sh[key], `$.sourceHashes.${key}`, HEX64, 'a lowercase hex SHA-256');
  }

  const cv = strict(root.converter, '$.converter', ['version']);
  const converter = { version: num(cv.version, '$.converter.version') };

  let excerpt: { fromMs: number; toMs: number } | undefined;
  if (root.excerpt !== undefined) {
    const e = strict(root.excerpt, '$.excerpt', ['fromMs', 'toMs']);
    excerpt = { fromMs: nonNegative(e.fromMs, '$.excerpt.fromMs'), toMs: nonNegative(e.toMs, '$.excerpt.toMs') };
    if (excerpt.fromMs >= excerpt.toMs || excerpt.toMs > durationMs) {
      throw new RecordingFormatError('$.excerpt', 'must be a non-empty window inside the run');
    }
  }
  const lo = excerpt ? excerpt.fromMs : 0;
  const hi = excerpt ? excerpt.toMs : durationMs;

  const chapters = column(root.chapters, '$.chapters', (x, p): RecordedChapter => {
    const o = strict(x, p, ['tMs', 'name', 'note']);
    const tMs = num(o.tMs, `${p}.tMs`);
    if (tMs < 0 || tMs > durationMs) throw new RecordingFormatError(`${p}.tMs`, 'is outside the run');
    if (excerpt && (tMs < lo || tMs > hi)) throw new RecordingFormatError(`${p}.tMs`, 'is outside the excerpt window');
    return { tMs, name: str(o.name, `${p}.name`), note: str(o.note, `${p}.note`) };
  });
  chapters.forEach((c, i) => {
    if (i > 0 && c.tMs < chapters[i - 1].tMs) throw new RecordingFormatError(`$.chapters[${i}].tMs`, 'chapters must not decrease in time');
  });

  const m = strict(root.measurements, '$.measurements', ['d', 'n', 't', 'v', 's']);
  const t = column(m.t, '$.measurements.t', num);
  let prev = lo;
  t.forEach((x, i) => {
    if (x < lo || x > hi) throw new RecordingFormatError(`$.measurements.t[${i}]`, `${x} is outside [${lo}, ${hi}]`);
    if (x < prev) throw new RecordingFormatError(`$.measurements.t[${i}]`, 'time column must not decrease');
    prev = x;
  });
  const index = (name: string, limit: number) => (x: unknown, p: string): number => {
    const n = num(x, p);
    if (!Number.isInteger(n) || n < 0 || n >= limit) throw new RecordingFormatError(p, `${name} index out of range`);
    return n;
  };
  const measurements: RecordedMeasurementColumns = {
    d: column(m.d, '$.measurements.d', index('device', devices.length), t.length),
    n: column(m.n, '$.measurements.n', index('measurement', channels.measurements.length), t.length),
    t,
    v: column(m.v, '$.measurements.v', num, t.length),
    s: column(
      m.s,
      '$.measurements.s',
      (x, p): 0 | 1 => {
        if (x !== 0 && x !== 1) throw new RecordingFormatError(p, 'expected 0 or 1');
        return x;
      },
      t.length,
    ),
  };

  const al = strict(root.alarms, '$.alarms', ['snapshots', 'events']);
  const alarm = (o: Obj, p: string): RecordedAlarm => {
    const id = matching(o.id, `${p}.id`, /^alarm-\d+$/, 'a re-minted id (alarm-N)');
    const dev = str(o.dev, `${p}.dev`);
    if (!tokens.has(dev)) throw new RecordingFormatError(`${p}.dev`, `device '${dev}' is not in $.devices`);
    if (o.ack !== undefined && o.ack !== true) throw new RecordingFormatError(`${p}.ack`, 'present only when true');
    const out: RecordedAlarm = {
      id,
      dev,
      key: str(o.key, `${p}.key`),
      metric: str(o.metric, `${p}.metric`),
      state: oneOf(o.state, `${p}.state`, ALARM_STATES),
      sev: oneOf(o.sev, `${p}.sev`, ALARM_SEVERITIES),
      occ: instant(o.occ, `${p}.occ`),
    };
    if (o.ack === true) out.ack = true;
    return out;
  };
  const ALARM_KEYS = ['id', 'dev', 'key', 'metric', 'state', 'sev', 'occ'];
  const checkTime = (tMs: number, p: string): number => {
    if (tMs < 0 || tMs > hi) throw new RecordingFormatError(p, `${tMs} is outside [0, ${hi}]`);
    return tMs;
  };
  const snapshots = column(al.snapshots, '$.alarms.snapshots', (x, p): RecordedAlarmSnapshot => {
    const o = strict(x, p, ['tMs', 'requestedAtMs', 'total', 'alarms']);
    const alarms = column(o.alarms, `${p}.alarms`, (a, ap) => alarm(strict(a, ap, ALARM_KEYS, ['ack']), ap));
    const ids = new Set<string>();
    alarms.forEach((a, i) => {
      if (ids.has(a.id)) throw new RecordingFormatError(`${p}.alarms[${i}].id`, `duplicate alarm ${a.id} in one snapshot`);
      ids.add(a.id);
    });
    // A snapshot that lists fewer alarms than its total is a truncated answer. Folding it
    // as the whole set would erase every alarm it left out.
    const total = nonNegative(o.total, `${p}.total`);
    if (total !== alarms.length) {
      throw new RecordingFormatError(`${p}.total`, `is ${total} but ${alarms.length} alarms are listed (truncated snapshot)`);
    }
    const tMs = checkTime(num(o.tMs, `${p}.tMs`), `${p}.tMs`);
    const requestedAtMs = nonNegative(o.requestedAtMs, `${p}.requestedAtMs`);
    if (!Number.isInteger(requestedAtMs)) throw new RecordingFormatError(`${p}.requestedAtMs`, 'expected an integer');
    if (requestedAtMs > tMs) throw new RecordingFormatError(`${p}.requestedAtMs`, 'a query cannot be sent after its answer');
    return { tMs, requestedAtMs, total, alarms };
  });
  const events = column(al.events, '$.alarms.events', (x, p): RecordedAlarmEvent => {
    const o = strict(x, p, [...ALARM_KEYS, 'tMs'], ['ack']);
    return { ...alarm(o, p), tMs: checkTime(num(o.tMs, `${p}.tMs`), `${p}.tMs`) };
  });
  for (const [name, list] of [['snapshots', snapshots], ['events', events]] as const) {
    list.forEach((e, i) => {
      if (i > 0 && e.tMs < list[i - 1].tMs) throw new RecordingFormatError(`$.alarms.${name}[${i}].tMs`, 'must not decrease');
    });
  }
  // An alarm id names one alarm on one device for the whole recording.
  const deviceOf = new Map<string, string>();
  const everyAlarm: Array<[string, RecordedAlarm]> = [
    ...snapshots.flatMap((s, i) => s.alarms.map((a, j): [string, RecordedAlarm] => [`$.alarms.snapshots[${i}].alarms[${j}]`, a])),
    ...events.map((e, i): [string, RecordedAlarm] => [`$.alarms.events[${i}]`, e]),
  ];
  for (const [path, a] of everyAlarm) {
    const seen = deviceOf.get(a.id);
    if (seen !== undefined && seen !== a.dev) {
      throw new RecordingFormatError(`${path}.dev`, `alarm ${a.id} was on '${seen}' and cannot move to '${a.dev}'`);
    }
    deviceOf.set(a.id, a.dev);
  }
  if (!channels.alarms && (snapshots.length > 0 || events.length > 0)) {
    throw new RecordingFormatError('$.alarms', 'history present but $.channels.alarms is false');
  }

  if (!Array.isArray(root.locations) || root.locations.length !== 0) {
    throw new RecordingFormatError('$.locations', 'this version carries no positions; must be an empty array');
  }

  const rec: BoardRecording = {
    kind: RECORDING_KIND,
    formatVersion: RECORDING_FORMAT_VERSION,
    runId: str(root.runId, '$.runId'),
    startedAtUtc: instant(root.startedAtUtc, '$.startedAtUtc'),
    durationMs,
    platformVersion: str(root.platformVersion, '$.platformVersion'),
    instance: str(root.instance, '$.instance'),
    tenant: str(root.tenant, '$.tenant'),
    build,
    clock,
    devices,
    channels,
    board,
    sourceHashes,
    converter,
    chapters,
    measurements,
    alarms: { snapshots, events },
    locations: [],
  };
  if (excerpt) rec.excerpt = excerpt;
  return rec;
}
