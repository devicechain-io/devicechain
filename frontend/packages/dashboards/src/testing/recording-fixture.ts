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

// A row's occurred-time is its applied time minus this, so the two are never equal and a
// reader that confuses them is caught.
export const FIXTURE_OCC_LAG_MS = 300;

const HASH = 'a'.repeat(64);

type Json = Record<string, unknown>;

function series(device: string, name: string, rows: Array<[number, number | null]>): Json {
  return {
    device,
    name,
    t: rows.map(([t]) => t),
    occ: rows.map(([t]) => t - FIXTURE_OCC_LAG_MS),
    v: rows.map(([, v]) => v),
  };
}

export function fixtureJson(options: { locations?: boolean } = {}): Json {
  const withLocations = options.locations ?? false;
  return {
    formatVersion: 1,
    runId: 'run-fixture',
    startedAtUtc: FIXTURE_START_UTC,
    platformVersion: '0.0.0-test',
    buildSha: 'abcdef1',
    durationMs: FIXTURE_DURATION_MS,
    clock: [{ fromMs: 0, scale: 1 }],
    devices: [...FIXTURE_DEVICES],
    anchors: [
      {
        relationship: 'assigned',
        targetType: 'customer',
        targetToken: 'acme-earthworks',
        members: [...FIXTURE_DEVICES],
      },
    ],
    channels: {
      measurements: ['fuel_pct', 'tyre_pressure_kpa', 'throughput_tph'],
      alarms: true,
      locations: withLocations,
      commands: false,
    },
    boardHash: HASH,
    sourceHashes: { 'observed.ndjson': HASH },
    chapters: [{ tMs: 20_000, title: 'Tyre alarm', note: 'presenter prepared' }],
    measurements: [
      series('sp-hauler-01', 'fuel_pct', [
        [0, 90],
        [10_000, 80],
        [20_000, 70],
        [30_000, 60],
        [40_000, 50],
      ]),
      series('sp-hauler-01', 'tyre_pressure_kpa', [
        [0, 600],
        [20_000, 599.6],
        [40_000, 560],
      ]),
      series('sp-hauler-02', 'fuel_pct', [
        [0, 95],
        [10_000, 94],
        [20_000, 93],
        [30_000, 92],
        [40_000, 91],
        [50_000, 90],
      ]),
      series('sp-plant-01', 'throughput_tph', [
        [0, 410],
        [5_000, null],
        [10_000, 415],
      ]),
    ],
    alarms: [
      {
        t: 10_000, token: 'alarm-1', device: 'sp-hauler-02', alarmKey: 'low-fuel', metricKey: 'fuel_pct',
        state: 'ACTIVE', severity: 'CRITICAL', acknowledged: false, raised: 9_700, cleared: null, acked: null,
      },
      {
        t: 20_000, token: 'alarm-2', device: 'sp-hauler-01', alarmKey: 'tyre-pressure-low', metricKey: 'tyre_pressure_kpa',
        state: 'ACTIVE', severity: 'MAJOR', acknowledged: false, raised: 19_700, cleared: null, acked: null,
      },
      {
        t: 30_000, token: 'alarm-1', device: 'sp-hauler-02', alarmKey: 'low-fuel', metricKey: 'fuel_pct',
        state: 'CLEARED', severity: 'CRITICAL', acknowledged: false, raised: 9_700, cleared: 29_700, acked: null,
      },
      {
        t: 45_000, token: 'alarm-3', device: 'sp-plant-01', alarmKey: 'overload', metricKey: 'throughput_tph',
        state: 'ACTIVE', severity: 'WARNING', acknowledged: true, raised: 44_700, cleared: null, acked: 44_900,
      },
    ],
    locations: withLocations
      ? ['sp-hauler-01', 'sp-hauler-02'].map((device, d) => ({
          device,
          t: [0, 20_000, 40_000],
          occ: [-300, 19_700, 39_700],
          lat: [40 + d, 40.001 + d, 40.002 + d],
          lon: [-117, -117.001, -117.002],
          elevation: [1000, null, 1002],
          speed: [0, 3.5, null],
          heading: [90, null, 180],
        }))
      : [],
  };
}

export function fixtureRecording(options: { locations?: boolean } = {}): BoardRecording {
  return parseBoardRecording(fixtureJson(options));
}
