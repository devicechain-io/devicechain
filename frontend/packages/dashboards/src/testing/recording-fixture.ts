// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A tiny synthetic recording for tests: 60 s, three devices, a handful of rows chosen so
// each assertion can name the row it expects. It is invented data, not an excerpt of any
// real run, and it is returned UNPARSED (plain JSON) so a test can corrupt it and feed it
// to parseBoardRecording.

import { parseBoardRecording, type BoardRecording } from '../recording';

export const FIXTURE_DEVICES = ['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01'] as const;
export const FIXTURE_DURATION_MS = 60_000;
export const FIXTURE_START_UTC = '2026-01-02T03:04:05.000Z';
export const FIXTURE_START_MS = Date.parse(FIXTURE_START_UTC);

// The site anchor a board binds to; every fixture device is a member.
export const SITE = { relationship: 'assigned', targetType: 'customer', targetToken: 'acme-earthworks' } as const;

const HASH = 'a'.repeat(64);
const COMMIT = 'b'.repeat(40);

type Json = Record<string, any>;

export const FIXTURE_NAMES = ['fuel_pct', 'tyre_pressure_kpa', 'throughput_tph'] as const;

// Every row, as [device index, metric name, applied time ms, value, seed flag].
const ROWS: Array<[number, (typeof FIXTURE_NAMES)[number], number, number, 0 | 1]> = [
  [0, 'fuel_pct', 0, 90, 1],
  [0, 'fuel_pct', 10_000, 80, 0],
  [0, 'fuel_pct', 20_000, 70, 0],
  [0, 'fuel_pct', 30_000, 60, 0],
  [0, 'fuel_pct', 40_000, 50, 0],
  [0, 'tyre_pressure_kpa', 0, 600, 1],
  [0, 'tyre_pressure_kpa', 20_000, 599.6, 0],
  [0, 'tyre_pressure_kpa', 40_000, 560, 0],
  [1, 'fuel_pct', 0, 95, 1],
  [1, 'fuel_pct', 10_000, 94, 0],
  [1, 'fuel_pct', 20_000, 93, 0],
  [1, 'fuel_pct', 30_000, 92, 0],
  [1, 'fuel_pct', 40_000, 91, 0],
  [1, 'fuel_pct', 50_000, 90, 0],
  [2, 'throughput_tph', 0, 410, 1],
  [2, 'throughput_tph', 5_000, 0, 0],
  [2, 'throughput_tph', 10_000, 415, 0],
];

export function fixtureInstant(offsetMs: number): string {
  return new Date(FIXTURE_START_MS + offsetMs).toISOString();
}

// A recording of the shape the converter writes. Returned UNPARSED (plain JSON) so a test
// can corrupt it and feed it to parseBoardRecording.
export function fixtureJson(): Json {
  const sorted = [...ROWS].sort((a, b) => a[2] - b[2]);
  const alarm = (id: string, dev: string, key: string, metric: string, state: string, sev: string, occ: number, ack?: true) => ({
    id, dev, key, metric, state, sev, occ: fixtureInstant(occ), ...(ack ? { ack } : {}),
  });
  return {
    kind: 'sitepulse-board-recording',
    formatVersion: 1,
    runId: 'run-fixture',
    startedAtUtc: FIXTURE_START_UTC,
    durationMs: FIXTURE_DURATION_MS,
    platformVersion: '0.0.0-test',
    instance: 'fixture',
    tenant: 'fixture-tenant',
    build: { gitSha: 'abcdef123456', sdkCommit: 'fedcba654321' },
    clock: [{ fromMs: 11, mode: 'real', scale: 1 }],
    devices: [
      { id: 'SP-HL-0001', token: 'sp-hauler-01', kind: 'Hauler' },
      { id: 'SP-HL-0002', token: 'sp-hauler-02', kind: 'Hauler' },
      { id: 'SP-PL-0001', token: 'sp-plant-01', kind: 'Plant' },
    ],
    channels: { measurements: [...FIXTURE_NAMES], alarms: true, locations: false, commands: false },
    board: { path: 'board.json', sourceCommit: COMMIT, sha256: HASH },
    sourceHashes: { 'run.json': HASH, 'observed.ndjson': HASH },
    converter: { version: 1 },
    chapters: [{ tMs: 20_000, name: 'tyre alarm', note: 'presenter prepared' }],
    measurements: {
      d: sorted.map((r) => r[0]),
      n: sorted.map((r) => FIXTURE_NAMES.indexOf(r[1])),
      t: sorted.map((r) => r[2]),
      v: sorted.map((r) => r[3]),
      s: sorted.map((r) => r[4]),
    },
    alarms: {
      snapshots: [{ tMs: 0, total: 0, alarms: [] }],
      events: [
        { ...alarm('alarm-1', 'sp-hauler-02', 'low-fuel', 'fuel_pct', 'ACTIVE', 'CRITICAL', 9_700), tMs: 10_000 },
        { ...alarm('alarm-2', 'sp-hauler-01', 'tyre-pressure-low', 'tyre_pressure_kpa', 'ACTIVE', 'MAJOR', 19_700), tMs: 20_000 },
        { ...alarm('alarm-1', 'sp-hauler-02', 'low-fuel', 'fuel_pct', 'CLEARED', 'CRITICAL', 29_700), tMs: 30_000 },
        { ...alarm('alarm-3', 'sp-plant-01', 'overload', 'throughput_tph', 'ACTIVE', 'WARNING', 44_700, true), tMs: 45_000 },
      ],
    },
    locations: [],
  };
}

export function fixtureRecording(): BoardRecording {
  return parseBoardRecording(fixtureJson());
}
