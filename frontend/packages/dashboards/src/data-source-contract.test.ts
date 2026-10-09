// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs the shared WidgetDataSource contract against all three implementations: the live
// hub (over a fake wire), the synthetic preview source, and the recorded source.

import { vi } from 'vitest';

import { DashboardHub } from './hub';
import { ALARMS_QUERY } from './internal/alarm-doc';
import { COMMANDS_QUERY } from './internal/command-doc';
import { LATEST_LOCATIONS_QUERY } from './internal/location-doc';
import { MEASUREMENT_STREAM } from './internal/measurement-doc';
import { RecordedClock, RecordedDataSource } from './recorded';
import { SyntheticDataSource } from './synthetic';
import {
  CONTRACT_DEVICE,
  CONTRACT_MEASUREMENT,
  describeDataSourceContract,
  settle,
  type ContractSubject,
} from './testing/data-source-contract';
import { FIXTURE_DURATION_MS, SITE, fixtureRecording } from './testing/recording-fixture';
import type { AlarmRow, LocationSample } from './types';

// The SDK's WIRE only: gql() is answered by a stand-in for the services, and subscribe()
// is recorded so the test can push samples down it. Everything else is the real module.
const wire = vi.hoisted(() => ({
  gql: vi.fn(),
  streams: [] as Array<{ doc: unknown; vars: { deviceToken: string }; adapter: { next: (d: unknown) => void } }>,
}));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    subscribe: (_area: string, doc: unknown, vars: { deviceToken: string }, adapter: { next: (d: unknown) => void }) => {
      wire.streams.push({ doc, vars, adapter });
      return () => {};
    },
    gql: (...args: unknown[]) => wire.gql(...args),
  };
});

// ---- live hub -------------------------------------------------------------

function alarmRow(n: number, over: Partial<AlarmRow>): AlarmRow {
  return {
    token: `alarm-${n}`,
    originatorType: 'device',
    originatorToken: CONTRACT_DEVICE,
    alarmKey: 'k',
    metricKey: 'm',
    state: 'ACTIVE',
    acknowledged: false,
    severity: 'MAJOR',
    raisedTime: null,
    clearedTime: null,
    acknowledgedTime: null,
    acknowledgedBy: null,
    lastValue: null,
    ...over,
  };
}

const HUB_ALARMS: AlarmRow[] = [
  alarmRow(1, { raisedTime: '2026-01-02T03:04:10.000Z' }),
  alarmRow(2, { raisedTime: '2026-01-02T03:04:20.000Z' }),
  alarmRow(3, { raisedTime: '2026-01-02T03:04:30.000Z' }),
];

function hubSubject(): ContractSubject {
  wire.streams.length = 0;
  wire.gql.mockReset();
  // A stand-in for the services: it honours the page size and sorts newest first, as the
  // real alarms query does, because the hub passes a tenant-wide page straight through.
  wire.gql.mockImplementation(async (_area: string, doc: unknown, vars: Record<string, unknown>) => {
    if (doc === ALARMS_QUERY) {
      const criteria = vars.criteria as { pageSize: number };
      const sorted = [...HUB_ALARMS].sort((a, b) => (b.raisedTime ?? '').localeCompare(a.raisedTime ?? ''));
      return { alarms: { results: sorted.slice(0, criteria.pageSize), pagination: { totalRecords: sorted.length } } };
    }
    if (doc === LATEST_LOCATIONS_QUERY) {
      const tokens = vars.deviceTokens as string[];
      const rows: LocationSample[] = tokens.map((deviceToken) => ({
        id: `loc-${deviceToken}`,
        deviceToken,
        latitude: 40,
        longitude: -117,
        elevation: null,
        accuracy: null,
        speed: null,
        heading: null,
        occurredTime: '2026-01-02T03:04:05.000Z',
      }));
      return { latestLocations: rows };
    }
    if (doc === COMMANDS_QUERY) {
      return { commands: { results: [], pagination: { totalRecords: 0 } } };
    }
    throw new Error('unexpected gql document in the contract wire');
  });
  const hub = new DashboardHub({
    resolver: { devicesForAnchor: async () => [CONTRACT_DEVICE], deviceExists: async () => true },
  });
  return {
    source: hub,
    deliver: async () => {
      await settle();
      // The device's stream carries every measurement; the hub filters per subscriber.
      for (const stream of wire.streams.filter((s) => s.doc === MEASUREMENT_STREAM)) {
        for (const name of [CONTRACT_MEASUREMENT, 'noise']) {
          stream.adapter.next({
            measurementStream: {
              id: `${name}-1`,
              deviceToken: stream.vars.deviceToken,
              eventType: 0,
              occurredTime: '2026-01-02T03:04:06.000Z',
              name,
              value: 42,
              classifier: null,
            },
          });
        }
      }
      await settle();
    },
    close: () => hub.disposeAll(),
  };
}

describeDataSourceContract('DashboardHub', hubSubject, { scopesToSelector: true, rejectsUnresolvedSlot: true, recordsPositions: true });

// ---- synthetic ------------------------------------------------------------

describeDataSourceContract(
  'SyntheticDataSource',
  () => {
    const source = new SyntheticDataSource();
    return { source, deliver: async () => {}, close: () => source.disposeAll() };
  },
  // Preview never resolves a device (it generates data for any selector), so it cannot
  // honour scope and does not refuse an unresolved slot. Both are deliberate.
  { scopesToSelector: false, rejectsUnresolvedSlot: false, recordsPositions: true },
);

// ---- recorded -------------------------------------------------------------

describeDataSourceContract(
  'RecordedDataSource',
  () => {
    const rec = fixtureRecording();
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: { start: () => () => {} } });
    const source = new RecordedDataSource(rec, clock, { siteAnchors: [SITE] });
    return {
      source,
      deliver: async () => {
        await settle();
        clock.play();
        clock.advance(FIXTURE_DURATION_MS);
        await settle();
      },
      close: () => source.disposeAll(),
    };
  },
  // This format version records no positions: a located selector is an error here.
  { scopesToSelector: true, rejectsUnresolvedSlot: true, recordsPositions: false },
);
