// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { AlarmRow, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

// No globals config, so register testing-library's DOM teardown ourselves —
// otherwise renders leak across tests and text matches find stale elements.
afterEach(cleanup);

import type { AlarmStreamState } from './hooks';
import { AlarmCount } from './widgets/alarm-count';
import { AlarmTable } from './widgets/alarm-table';

const widget = (
  type: WidgetInstance['type'],
  options: Record<string, unknown> = {},
): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { x: 0, y: 0, w: 2, h: 2, z: 1 } },
  datasource: undefined,
  options,
});

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'a-1',
  originatorType: 'device',
  originatorToken: 'thermostat-01',
  alarmKey: 'over-temperature',
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: '2026-07-05T12:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 87.4,
  message: 'Temperature above 85°C',
  ...over,
});

const state = (over: Partial<AlarmStreamState> = {}): AlarmStreamState => ({
  alarms: [],
  total: 0,
  loading: false,
  error: null,
  ...over,
});

describe('AlarmTable', () => {
  it('renders a row per alarm with severity label, originator, key and value', () => {
    const alarms = [
      alarm({ token: 'a-1', severity: 'CRITICAL', alarmKey: 'over-temperature', originatorToken: 'thermostat-01', lastValue: 87.4 }),
      alarm({ token: 'a-2', severity: 'MAJOR', alarmKey: 'low-battery', originatorToken: 'sensor-14', lastValue: 12 }),
    ];
    render(<AlarmTable widget={widget('alarm-table')} data={state({ alarms, total: 2 })} />);

    expect(screen.getByText('Critical')).toBeTruthy();
    expect(screen.getByText('Major')).toBeTruthy();
    expect(screen.getByText('over-temperature')).toBeTruthy();
    expect(screen.getByText('low-battery')).toBeTruthy();
    expect(screen.getByText('thermostat-01')).toBeTruthy();
    expect(screen.getByText('sensor-14')).toBeTruthy();
    expect(screen.getByText('87.4')).toBeTruthy();
    expect(screen.getByText('12')).toBeTruthy();
  });

  it('shows "No alarms" when the (loaded) list is empty', () => {
    render(<AlarmTable widget={widget('alarm-table')} data={state({ alarms: [], loading: false })} />);
    expect(screen.getByText('No alarms')).toBeTruthy();
  });

  it('shows "Loading…" while loading with no rows yet', () => {
    render(<AlarmTable widget={widget('alarm-table')} data={state({ alarms: [], loading: true })} />);
    expect(screen.getByText('Loading…')).toBeTruthy();
  });
});

describe('AlarmCount', () => {
  it('shows the server total', () => {
    render(
      <AlarmCount
        widget={widget('alarm-count')}
        data={state({ alarms: [alarm({ severity: 'MAJOR' })], total: 7 })}
      />,
    );
    expect(screen.getByText('7')).toBeTruthy();
  });

  it('pluralizes the label: "alarm" at one, "alarms" otherwise', () => {
    const { rerender } = render(
      <AlarmCount widget={widget('alarm-count')} data={state({ alarms: [alarm()], total: 1 })} />,
    );
    expect(screen.getByText('alarm')).toBeTruthy();

    rerender(<AlarmCount widget={widget('alarm-count')} data={state({ alarms: [], total: 2 })} />);
    expect(screen.getByText('alarms')).toBeTruthy();
  });

  it('shows an em dash while loading before any total is known', () => {
    render(<AlarmCount widget={widget('alarm-count')} data={state({ total: 0, loading: true })} />);
    expect(screen.getByText('—')).toBeTruthy();
  });
});
