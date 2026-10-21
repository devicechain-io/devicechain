// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Deep links and marker picks, end to end through the pieces a host assembles: the map widget,
// useBoundDevices, useResolvedBindings and the renderer. The data source records what each
// widget subscribes to, so "the other widgets rebind" and "the board never showed the wrong
// device" are asserted on subscriptions, not on pixels.

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
import { effectiveBindings } from '@devicechain/dashboards';
import { useResolvedBindings } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useMemo, useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const m = vi.hoisted(() => ({
  markers: [] as Array<{ element: HTMLElement }>,
}));

vi.mock('maplibre-gl', () => {
  class FakeMap {
    constructor(options: { container: HTMLElement }) {
      options.container.appendChild(document.createElement('div'));
    }
    jumpTo() {}
    fitBounds() {}
    remove() {}
  }
  class FakeMarker {
    constructor(options: { element: HTMLElement }) {
      m.markers.push({ element: options.element });
    }
    setLngLat() {
      return this;
    }
    addTo() {
      return this;
    }
    remove() {}
  }
  class FakeLngLatBounds {
    extend() {
      return this;
    }
  }
  return { Map: FakeMap, Marker: FakeMarker, LngLatBounds: FakeLngLatBounds, setWorkerUrl: () => {} };
});

import { BoundDevicesNotice } from './bound-devices-notice';
import { DashboardRenderer } from './dashboard-renderer';
import { MapRuntimeProvider } from './map-runtime-context';
import { useBoundDevices } from './use-bound-devices';

afterEach(cleanup);
beforeEach(() => {
  m.markers.length = 0;
});

class RecordingSource implements WidgetDataSource {
  readonly opened: Array<{ channel: string; token: string | undefined; disposed: boolean }> = [];
  private open(channel: string, ds: DatasourceSelector | undefined) {
    const e = { channel, token: ds?.kind === 'device' ? ds.deviceToken : undefined, disposed: false };
    this.opened.push(e);
    return () => {
      e.disposed = true;
    };
  }
  subscribeWidget(ds: DatasourceSelector, _sink: WidgetStreamSink) {
    return this.open('measurement', ds);
  }
  subscribeAlarms(sub: AlarmSubscription, sink: AlarmStreamSink) {
    sink.next({ alarms: [], total: 0 });
    return this.open('alarm', sub.datasource);
  }
  subscribeCommands(_sub: CommandSubscription, sink: CommandStreamSink) {
    sink.next({ deviceToken: null, commands: [], total: 0 });
    return () => {};
  }
  subscribeLocations(_sub: LocationSubscription, sink: LocationStreamSink) {
    // The map shows the whole fleet whatever it is bound to: it is the picker here.
    const at = (deviceToken: string, latitude: number) => ({
      id: deviceToken,
      deviceToken,
      latitude,
      longitude: -84,
      elevation: null,
      accuracy: null,
      speed: null,
      heading: null,
      occurredTime: '2026-10-20T12:00:00Z',
    });
    sink.next({
      kind: 'positions',
      deviceTokens: ['hauler-1', 'hauler-2'],
      locations: [at('hauler-1', 33.7), at('hauler-2', 33.8)],
    });
    return () => {};
  }
  async isDatasourceAvailable() {
    return true;
  }
  live(token: string) {
    return this.opened.filter((o) => o.channel === 'measurement' && o.token === token && !o.disposed);
  }
  ever(token: string) {
    return this.opened.filter((o) => o.channel === 'measurement' && o.token === token);
  }
}

const box = (row: number) => ({ base: { mode: 'grid' as const, col: 0, row, cols: 6, rows: 3 } });
const definition: DashboardDefinition = {
  schemaVersion: 2,
  title: 'Pick',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: {
    machine: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'hauler-1' } },
    fleet: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'hauler-1' } },
  },
  widgets: [
    { id: 'card', type: 'latest-card', layout: box(0), datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] } },
    {
      id: 'map',
      type: 'map',
      layout: box(3),
      datasource: { kind: 'slot', slot: 'fleet', measurements: [], location: { series: 'latest' } },
      options: { selectionTarget: 'machine' },
    },
  ],
};

interface Handle {
  setSearch: (s: string) => void;
}

// Assembles what a host assembles. The "address" is local state, standing in for a router.
function Host({
  source,
  initialSearch,
  deviceExists,
  replaces,
  handle,
}: {
  source: RecordingSource;
  initialSearch: string;
  deviceExists: (token: string) => Promise<boolean>;
  replaces: string[];
  handle: Handle;
}) {
  const [search, setSearch] = useState(initialSearch);
  handle.setSearch = setSearch;
  const resolver = useMemo(() => ({ deviceExists, devicesForAnchor: async () => [] }), [deviceExists]);
  const address = useMemo(
    () => ({
      search,
      replaceSearch: (next: string) => {
        replaces.push(next);
        setSearch(next);
      },
    }),
    [search, replaces],
  );
  const base = useMemo(() => effectiveBindings(definition), []);
  const bound = useBoundDevices({ definition, resolver, address });
  const bindings = useResolvedBindings(definition, base, bound.selection, resolver);
  return (
    <MapRuntimeProvider runtime={{ workerUrl: 'blob:test', loadStyles: () => Promise.resolve() }}>
      {bound.phase === 'ready' ? (
        <DashboardRenderer definition={definition} hub={source} seedHistory={false} bindings={bindings} select={bound.select} />
      ) : bound.phase === 'refused' && bound.problems ? (
        <BoundDevicesNotice
          title="blocked"
          lines={[...bound.problems.missing.map((x) => `missing ${x.token}`), ...bound.problems.failed.map((x) => `failed ${x.token}`)]}
          dismissLabel="open without"
          onDismiss={bound.dismiss}
          retryLabel="retry"
          onRetry={bound.retry}
        />
      ) : (
        <p>checking</p>
      )}
    </MapRuntimeProvider>
  );
}

const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
const settle = async () => {
  await flush();
  await flush();
};

function mount(over: { initialSearch?: string; deviceExists?: (t: string) => Promise<boolean> } = {}) {
  const source = new RecordingSource();
  const replaces: string[] = [];
  const handle: Handle = { setSearch: () => {} };
  const deviceExists = over.deviceExists ?? (async () => true);
  render(<Host source={source} initialSearch={over.initialSearch ?? ''} deviceExists={deviceExists} replaces={replaces} handle={handle} />);
  return { source, replaces, handle };
}

const markerFor = (device: string) => m.markers.find((x) => x.element.getAttribute('data-device') === device)!.element;

describe('a map marker click', () => {
  it('selects that device: the other widgets rebind and the address follows', async () => {
    const { source, replaces } = mount();
    await settle();
    expect(source.live('hauler-1').length).toBeGreaterThan(0);
    expect(m.markers.length).toBeGreaterThanOrEqual(2);

    fireEvent.click(markerFor('hauler-2'));
    await settle();

    expect(source.live('hauler-2').length).toBeGreaterThan(0);
    expect(source.live('hauler-1')).toHaveLength(0);
    expect(replaces).toEqual(['bind.machine=hauler-2']);
  });
});

describe('a deep link', () => {
  it('binds a valid device, and the board never paints the default one first', async () => {
    const { source, replaces } = mount({ initialSearch: 'kiosk=1&bind.machine=hauler-2' });
    await settle();

    expect(source.live('hauler-2').length).toBeGreaterThan(0);
    expect(source.ever('hauler-1')).toHaveLength(0);
    // The link was already the address: nothing is rewritten.
    expect(replaces).toEqual([]);
  });

  it('keeps other parameters when a pick rewrites the address', async () => {
    const { replaces } = mount({ initialSearch: 'kiosk=1&theme=dark' });
    await settle();
    fireEvent.click(markerFor('hauler-2'));
    await settle();
    expect(replaces).toEqual(['kiosk=1&theme=dark&bind.machine=hauler-2']);
  });

  it('refuses a device the caller cannot see, and shows nobody else data', async () => {
    const asked: string[] = [];
    const { source } = mount({
      initialSearch: 'bind.machine=someone-elses',
      deviceExists: async (t) => {
        asked.push(t);
        return false;
      },
    });
    await settle();

    expect(asked).toEqual(['someone-elses']);
    expect(screen.getByTestId('bound-devices-notice').textContent).toContain('missing someone-elses');
    // Neither the named device nor the board default was ever subscribed.
    expect(source.opened).toHaveLength(0);
  });

  it('refuses a slot the board does not have and a malformed token, without asking the server', async () => {
    const asked: string[] = [];
    mount({
      initialSearch: 'bind.nope=hauler-2&bind.machine=bad%20token',
      deviceExists: async (t) => {
        asked.push(t);
        return true;
      },
    });
    await settle();
    expect(asked).toEqual([]);
    expect(screen.getByTestId('bound-devices-notice')).toBeTruthy();
  });

  it('dismiss opens the board on its own defaults and cleans the address', async () => {
    const { source, replaces } = mount({ initialSearch: 'kiosk=1&bind.machine=gone', deviceExists: async () => false });
    await settle();
    fireEvent.click(screen.getByTestId('bound-devices-dismiss'));
    await settle();

    expect(source.live('hauler-1').length).toBeGreaterThan(0);
    expect(replaces).toEqual(['kiosk=1']);
  });

  it('a failed check is retryable and is not reported as a missing device', async () => {
    let calls = 0;
    const { source } = mount({
      initialSearch: 'bind.machine=hauler-2',
      deviceExists: async () => {
        calls += 1;
        if (calls === 1) throw new Error('network');
        return true;
      },
    });
    await settle();
    const text = screen.getByTestId('bound-devices-notice').textContent ?? '';
    expect(text).toContain('failed hauler-2');
    expect(text).not.toContain('missing');

    fireEvent.click(screen.getByTestId('bound-devices-retry'));
    await settle();
    expect(source.live('hauler-2').length).toBeGreaterThan(0);
  });

  it('an address that changes from outside is read as a new link; the hook own write is not', async () => {
    const { source, replaces, handle } = mount();
    await settle();
    fireEvent.click(markerFor('hauler-2'));
    await settle();
    // Our own write came back through the address: it was not re-checked or re-written.
    expect(replaces).toEqual(['bind.machine=hauler-2']);

    // Back/forward or a pasted link: the address now says hauler-1 again.
    await act(async () => handle.setSearch('bind.machine=hauler-1'));
    await settle();
    expect(source.live('hauler-1').length).toBeGreaterThan(0);
    expect(source.live('hauler-2')).toHaveLength(0);
    expect(replaces).toEqual(['bind.machine=hauler-2']);

    // An address with no link at all returns the board to its defaults.
    await act(async () => handle.setSearch(''));
    await settle();
    expect(source.live('hauler-1').length).toBeGreaterThan(0);
    expect(replaces).toEqual(['bind.machine=hauler-2']);
  });
});
