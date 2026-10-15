// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// jsdom cannot lay anything out, so these pin the STYLES and CONDITIONS that keep a widget
// inside its slot: the measured size is injected through a stubbed ResizeObserver, and the
// assertions read what the widget decided to render at that size.

import type { AlarmRow, MeasurementSample, WidgetInstance, WidgetType } from '@devicechain/dashboards';
import type { AlarmStreamState, MeasurementStreamState } from '@devicechain/widget-sdk';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ALARM_COUNT_CAPTION_ROOM_PX, AlarmCount } from './widgets/alarm-count';
import { ALARM_TABLE_NARROW_PX, AlarmTable } from './widgets/alarm-table';
import { LatestCard } from './widgets/latest-card';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Every observed element reports this content-box size on observe().
function stubElementSize(width: number, height: number) {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(private cb: (entries: unknown[]) => void) {}
      observe() {
        this.cb([{ contentRect: { width, height } }]);
      }
      disconnect() {}
    },
  );
}

const widget = (type: WidgetType, options: Record<string, unknown> = {}): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 2, rows: 2 } },
  datasource: undefined,
  options,
});

const sample: MeasurementSample = {
  id: 's',
  deviceToken: 'd',
  eventType: 0,
  occurredTime: '2026-07-05T12:00:00Z',
  name: 'fuel',
  value: 42.5,
  classifier: null,
};
const measurements: MeasurementStreamState = { latest: { fuel: sample }, samples: [sample], error: null };

const alarm: AlarmRow = {
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
};
const alarms: AlarmStreamState = { alarms: [alarm], total: 1, loading: false, error: null };

// The widget's own column: the first child of the frame's content column.
const columnOf = (container: HTMLElement) =>
  container.querySelector('[data-widget-content]')!.firstElementChild as HTMLElement;

describe('latest-card', () => {
  it('hides the timestamp when the measured box has no room for it (sitepulse card)', () => {
    stubElementSize(119, 32);
    render(<LatestCard widget={widget('latest-card')} data={measurements} />);
    expect(screen.getByText('42.5')).toBeTruthy();
    expect(screen.queryByText(/\d:\d\d/)).toBeNull();
  });

  it('shows the timestamp in a box that has room', () => {
    stubElementSize(400, 300);
    render(<LatestCard widget={widget('latest-card')} data={measurements} />);
    expect(screen.queryByText(/\d:\d\d/)).not.toBeNull();
  });

  it('shows the timestamp before the first measure', () => {
    render(<LatestCard widget={widget('latest-card')} data={measurements} />);
    expect(screen.queryByText(/\d:\d\d/)).not.toBeNull();
  });

  it('centres its column with safe alignment so excess is cut at one end, not both', () => {
    const { container } = render(<LatestCard widget={widget('latest-card')} data={measurements} />);
    expect(columnOf(container).style.justifyContent).toBe('safe center');
    expect(columnOf(container).style.overflow).toBe('hidden');
  });

  it('grows the value past the old 44 px cap in a large box', () => {
    stubElementSize(900, 600);
    render(<LatestCard widget={widget('latest-card')} data={measurements} />);
    expect(parseInt(screen.getByText('42.5').style.fontSize, 10)).toBeGreaterThan(44);
  });
});

describe('alarm-count', () => {
  it('centres its column safely and clips', () => {
    const { container } = render(<AlarmCount widget={widget('alarm-count')} data={alarms} />);
    expect(columnOf(container).style.justifyContent).toBe('safe center');
    expect(columnOf(container).style.overflow).toBe('hidden');
  });

  it('fits the count to the room the caption leaves in a short box', () => {
    stubElementSize(200, 40);
    render(<AlarmCount widget={widget('alarm-count')} data={alarms} />);
    const font = parseInt(screen.getByText('1').style.fontSize, 10);
    // lineHeight is 1, so the count's line is its font size; the caption sits under it.
    expect(font + ALARM_COUNT_CAPTION_ROOM_PX).toBeLessThanOrEqual(40);
  });

  it('grows the count past the old 56 px cap in a large box', () => {
    stubElementSize(900, 600);
    render(<AlarmCount widget={widget('alarm-count')} data={alarms} />);
    expect(parseInt(screen.getByText('1').style.fontSize, 10)).toBeGreaterThan(56);
  });
});

describe('alarm-table', () => {
  it('drops Status and Raised when narrow, keeping the columns an operator reads first', () => {
    stubElementSize(ALARM_TABLE_NARROW_PX - 1, 300);
    render(<AlarmTable widget={widget('alarm-table')} data={alarms} />);
    expect(screen.queryByRole('columnheader', { name: 'Status' })).toBeNull();
    expect(screen.queryByRole('columnheader', { name: 'Raised' })).toBeNull();
    for (const name of ['Severity', 'Originator', 'Alarm', 'Value']) {
      expect(screen.getByRole('columnheader', { name })).toBeTruthy();
    }
    // Header and body stay in step: the stripe plus four data cells per row.
    expect(screen.getByText('over-temperature').closest('tr')!.querySelectorAll('td')).toHaveLength(5);
  });

  it('keeps every column at the threshold width', () => {
    stubElementSize(ALARM_TABLE_NARROW_PX, 300);
    render(<AlarmTable widget={widget('alarm-table')} data={alarms} />);
    expect(screen.getByRole('columnheader', { name: 'Status' })).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: 'Raised' })).toBeTruthy();
  });

  it('keeps every column before it is measured', () => {
    render(<AlarmTable widget={widget('alarm-table')} data={alarms} />);
    expect(screen.getByRole('columnheader', { name: 'Status' })).toBeTruthy();
  });
});
