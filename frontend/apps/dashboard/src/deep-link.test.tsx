// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The /dash viewer honours `?bind.<slot>=<device>`: the device is checked against the viewer's
// own scope, bound before the board first subscribes, and a link that cannot be honoured shows
// an explicit state. A pick rewrites the address in place (replaceState, never pushState).

import type {
  AlarmStreamSink,
  AlarmSubscription,
  CommandStreamSink,
  CommandSubscription,
  DashboardDefinition,
  DatasourceSelector,
  LocationStreamSink,
  LocationSubscription,
  WidgetDataSource,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { hostWidgetRegistry } from '@devicechain/host-widgets';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  seen: [] as Array<{ channel: string; token: string | undefined }>,
  source: undefined as WidgetDataSource | undefined,
  exists: (async () => true) as (token: string) => Promise<boolean>,
  select: undefined as ((t: { slot: string; binding: { kind: 'device'; deviceToken: string } }) => void) | undefined,
}));

class RecordingSource implements WidgetDataSource {
  subscribeWidget(datasource: DatasourceSelector, _sink: WidgetStreamSink) {
    h.seen.push({ channel: 'measurement', token: datasource.kind === 'device' ? datasource.deviceToken : undefined });
    return () => {};
  }
  subscribeAlarms(_sub: AlarmSubscription, sink: AlarmStreamSink) {
    sink.next({ alarms: [], total: 0 });
    return () => {};
  }
  subscribeCommands(_sub: CommandSubscription, sink: CommandStreamSink) {
    sink.next({ deviceToken: null, commands: [], total: 0 });
    return () => {};
  }
  subscribeLocations(_sub: LocationSubscription, sink: LocationStreamSink) {
    sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
    return () => {};
  }
  async isDatasourceAvailable() {
    return true;
  }
}

vi.mock('@devicechain/widgets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widgets')>();
  return {
  ...actual,
  // The real renderer, with its selection callback captured so a test can pick as a widget would.
  DashboardRenderer: (props: Parameters<typeof actual.DashboardRenderer>[0]) => {
    h.select = props.select;
    return <actual.DashboardRenderer {...props} />;
  },
  useDashboardHub: () => (h.source ??= new RecordingSource()),
  useSlotCandidates: () => undefined,
  };
});
vi.mock('@devicechain/dashboards', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDeviceResolver: () => ({ deviceExists: (t: string) => h.exists(t), devicesForAnchor: async () => [] }),
  createEntityLister: () => ({}),
}));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: () => Promise.reject(new Error('offline')),
}));

import './i18n/config';
import { View } from './App';

const definition: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Embedded',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'hauler-1' } } },
  widgets: [
    {
      id: 'card',
      type: 'latest-card',
      layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } },
      datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] },
    },
  ],
};

const flush = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });

async function open(search: string) {
  window.history.replaceState(null, '', `/dash/${search}`);
  render(<View loaded={{ definition, manifest: {}, registry: hostWidgetRegistry() }} authorities={[]} onChange={() => {}} onSignOut={() => {}} />);
  await flush();
  await flush();
}

beforeEach(() => {
  h.seen.length = 0;
  h.exists = async () => true;
});
afterEach(() => {
  cleanup();
  window.history.replaceState(null, '', '/');
});

describe('the /dash viewer and a deep link', () => {
  it('binds the linked device and never subscribes the default first', async () => {
    await open('?dashboard=x&bind.machine=hauler-9');
    expect(h.seen).toContainEqual({ channel: 'measurement', token: 'hauler-9' });
    expect(h.seen.some((s) => s.token === 'hauler-1')).toBe(false);
    // The address is untouched: it already is the link.
    expect(window.location.search).toBe('?dashboard=x&bind.machine=hauler-9');
  });

  it('shows an explicit state for a device the viewer cannot see, and subscribes nothing', async () => {
    h.exists = async () => false;
    await open('?bind.machine=someone-elses');
    expect(screen.getByTestId('bound-devices-notice').textContent).toContain('someone-elses');
    expect(h.seen).toHaveLength(0);

    fireEvent.click(screen.getByTestId('bound-devices-dismiss'));
    await flush();
    expect(h.seen).toContainEqual({ channel: 'measurement', token: 'hauler-1' });
    expect(window.location.search).toBe('');
  });

  it('a pick rewrites the address by replacing the entry, and keeps the other parameters', async () => {
    await open('?dashboard=x&kiosk=1');
    const length = window.history.length;
    const push = vi.spyOn(window.history, 'pushState');

    act(() => h.select?.({ slot: 'machine', binding: { kind: 'device', deviceToken: 'hauler-2' } }));
    await flush();

    expect(window.location.search).toBe('?dashboard=x&kiosk=1&bind.machine=hauler-2');
    expect(push).not.toHaveBeenCalled();
    expect(window.history.length).toBe(length);
    expect(h.seen).toContainEqual({ channel: 'measurement', token: 'hauler-2' });
    push.mockRestore();
  });

  it('follows Back/Forward to an address that names another device', async () => {
    await open('?bind.machine=hauler-8');
    expect(h.seen).toContainEqual({ channel: 'measurement', token: 'hauler-8' });

    window.history.replaceState(null, '', '/dash/?bind.machine=hauler-7');
    act(() => {
      window.dispatchEvent(new PopStateEvent('popstate'));
    });
    await flush();
    await flush();
    expect(h.seen).toContainEqual({ channel: 'measurement', token: 'hauler-7' });
  });

  it('checks the address Back/Forward lands on, and refuses one the viewer cannot see', async () => {
    const asked: string[] = [];
    h.exists = async (t) => {
      asked.push(t);
      return t !== 'not-yours';
    };
    await open('?bind.machine=hauler-8');
    const pop = async (search: string) => {
      window.history.replaceState(null, '', `/dash/${search}`);
      act(() => {
        window.dispatchEvent(new PopStateEvent('popstate'));
      });
      await flush();
      await flush();
    };
    await pop('?bind.machine=hauler-7');
    await pop('?bind.machine=hauler-8');
    expect(asked).toEqual(['hauler-8', 'hauler-7', 'hauler-8']);

    await pop('?bind.machine=not-yours');
    expect(asked[asked.length - 1]).toBe('not-yours');
    expect(screen.getByTestId('bound-devices-notice')).toBeTruthy();
    expect(h.seen.some((x) => x.token === 'not-yours')).toBe(false);
  });

  it('refuses a link that names a slot twice, and asks about neither device', async () => {
    const asked: string[] = [];
    h.exists = async (t) => {
      asked.push(t);
      return true;
    };
    await open('?bind.machine=hauler-7&bind.machine=hauler-8');
    expect(asked).toEqual([]);
    expect(screen.getByTestId('bound-devices-notice').textContent).toContain('more than once');
    expect(h.seen).toHaveLength(0);
  });
});
