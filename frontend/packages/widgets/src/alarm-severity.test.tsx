// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm severity breakdown: one stacked bar and a legend of counts. The folding of the
// per-severity counts is tested on its own; the widget with a hand-built snapshot; and the
// connected half through the real ConnectedWidget with a fake alarm channel, which is where
// "one grouped count read, no rows", "every originator" and "one type, asked of the server" are
// decided.

import type { AlarmSeverityCount, AlarmSnapshot, AlarmStreamSink, AlarmSubscription, DashboardHub, WidgetInstance } from '@devicechain/dashboards';
import type { AlarmStreamState } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest';

import { builtinWidgetCatalog } from './builtin-descriptors';
import { alarmSubscription } from './binding-policy';
import { ConnectedWidget } from './connected-widget';
import { loadAlarmWidgets } from './widgets/alarm-lazy';
import { AlarmSeverity, SEVERITY_SCALE, severityCounts } from './widgets/alarm-severity';

beforeAll(async () => {
  await loadAlarmWidgets();
});
beforeEach(() => {
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const count = (severity: string, n: number): AlarmSeverityCount => ({ severity, count: n });

const widget = (options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'sev',
    type: 'alarm-severity',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 2 } },
    options: { state: 'ACTIVE', ...options },
  }) as unknown as WidgetInstance;

const state = (severityCounts: AlarmSeverityCount[] | undefined, over: Partial<AlarmStreamState> = {}): AlarmStreamState => ({
  alarms: [],
  total: (severityCounts ?? []).reduce((n, c) => n + c.count, 0),
  loading: false,
  error: null,
  severityCounts,
  ...over,
});

const MIXED = [count('CRITICAL', 3), count('MINOR', 5), count('WARNING', 4)];

// The legend, as [severity, count, dimmed] in the order drawn.
const legend = (c: HTMLElement) =>
  [...c.querySelectorAll('li[data-severity]')].map((li) => [
    li.getAttribute('data-severity'),
    Number(li.lastElementChild?.textContent),
    li.getAttribute('data-zero') === 'true',
  ]);

describe('severityCounts', () => {
  it('puts each count on the scale, with every severity of the scale present', () => {
    expect(severityCounts(MIXED)).toEqual({ CRITICAL: 3, MAJOR: 0, MINOR: 5, WARNING: 4, INDETERMINATE: 0 });
    expect(Object.keys(severityCounts([]))).toEqual([...SEVERITY_SCALE]);
  });

  it('adds up repeated entries of one severity', () => {
    expect(severityCounts([count('MAJOR', 2), count('MINOR', 1), count('MAJOR', 3)])).toEqual({
      CRITICAL: 0,
      MAJOR: 5,
      MINOR: 1,
      WARNING: 0,
      INDETERMINATE: 0,
    });
  });

  it('counts a severity this build does not know with INDETERMINATE rather than dropping it', () => {
    expect(severityCounts([count('CATASTROPHIC', 2), count('INDETERMINATE', 1)]).INDETERMINATE).toBe(3);
  });

  it('ignores a zero or negative count', () => {
    expect(severityCounts([count('MAJOR', 0), count('MAJOR', -3)]).MAJOR).toBe(0);
  });
});

describe('the breakdown', () => {
  it('lists every severity, most severe first, with the count of each', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(MIXED)} />);
    expect(legend(container)).toEqual([
      ['CRITICAL', 3, false],
      ['MAJOR', 0, true],
      ['MINOR', 5, false],
      ['WARNING', 4, false],
      ['INDETERMINATE', 0, true],
    ]);
    expect(container.querySelector('[data-alarm-severity-total]')?.textContent).toBe('12 alarms');
  });

  it('dims a severity with none and keeps it listed, so the scale is stable', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state([count('MAJOR', 4)])} />);
    const items = [...container.querySelectorAll<HTMLElement>('li[data-severity]')];
    expect(items).toHaveLength(5);
    const item = (sev: string) => items.find((li) => li.dataset.severity === sev)!;
    expect(item('MAJOR').style.color).toContain('--foreground');
    expect(item('MAJOR').style.color).not.toContain('muted');
    for (const sev of ['CRITICAL', 'MINOR', 'WARNING', 'INDETERMINATE']) {
      // Dimmed by a colour that keeps its text contrast, never by fading the text.
      expect(item(sev).style.color, sev).toContain('--muted-foreground');
      expect(item(sev).style.opacity, sev).toBe('');
      expect(Number((item(sev).firstElementChild as HTMLElement).style.opacity), sev).toBeLessThan(0.6);
      expect(item(sev).textContent).toMatch(/0$/);
    }
    // No segment for a severity with none.
    expect([...container.querySelectorAll('[data-segment]')].map((e) => e.getAttribute('data-segment'))).toEqual(['MAJOR']);
  });

  it('sizes the segments by their counts', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(MIXED)} />);
    const grow = (sev: string) => Number((container.querySelector(`[data-segment="${sev}"]`) as HTMLElement).style.flexGrow);
    expect(grow('CRITICAL')).toBe(3);
    expect(grow('MINOR')).toBe(5);
    expect(grow('WARNING')).toBe(4);
  });

  it('is calm when nothing is active: five dimmed rows, an empty track, "0 alarms"', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state([])} />);
    expect(legend(container).every(([, n, dim]) => n === 0 && dim === true)).toBe(true);
    expect(container.querySelectorAll('[data-segment]')).toHaveLength(0);
    expect(container.querySelector('[data-alarm-severity-total]')?.textContent).toBe('0 alarms');
  });

  it('colours a severity with the severity token and INDETERMINATE with the neutral one', () => {
    const { container } = render(
      <AlarmSeverity widget={widget()} data={state([count('CRITICAL', 1), count('INDETERMINATE', 1)])} />,
    );
    const fill = (sev: string) => (container.querySelector(`[data-segment="${sev}"]`) as HTMLElement).style.background;
    expect(fill('CRITICAL')).not.toBe('');
    expect(fill('CRITICAL')).not.toBe(fill('INDETERMINATE'));
    expect(fill('INDETERMINATE')).toContain('--muted-foreground');
  });

  it('says a breakdown set to one device type counts only the alarms on its devices', () => {
    render(<AlarmSeverity widget={widget({ deviceType: 'sensor' })} data={state([count('WARNING', 4), count('CRITICAL', 1)])} />);
    expect(screen.getByText('5 alarms on sensor devices')).toBeTruthy();
    expect(screen.getByRole('list', { name: 'Alarms by severity, 5 alarms on sensor devices' })).toBeTruthy();
  });

  it('gives assistive tech a text list of the counts, and hides the bar', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(MIXED)} />);
    const list = screen.getByRole('list', { name: 'Alarms by severity, 12 alarms' });
    expect(within(list).getAllByRole('listitem').map((li) => li.getAttribute('aria-label'))).toEqual([
      'Critical: 3',
      'Major: 0',
      'Minor: 5',
      'Warning: 4',
      'Indeterminate: 0',
    ]);
    expect(container.querySelector('[data-alarm-severity-bar]')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('speaks Spanish when the page is Spanish', () => {
    document.documentElement.lang = 'es';
    render(<AlarmSeverity widget={widget()} data={state(MIXED)} />);
    expect(screen.getByRole('list', { name: 'Alarmas por severidad, 12 alarmas' })).toBeTruthy();
    expect(screen.getByText('Crítica')).toBeTruthy();
  });

  it('says so, rather than drawing zeros, when the source cannot count by severity', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(undefined)} />);
    expect(container.querySelector('[data-alarm-severity="message"]')?.textContent).toBe(
      'This data source cannot count alarms by severity.',
    );
    expect(container.querySelector('li')).toBeNull();
  });

  it('says the read failed, rather than that the source cannot count, when the read fails', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(undefined, { error: new Error('boom') })} />);
    expect(container.querySelector('[data-alarm-severity="message"]')?.textContent).toBe('The alarm counts could not be read.');
  });

  it('says it is loading until the first answer', () => {
    const { container } = render(<AlarmSeverity widget={widget()} data={state(undefined, { loading: true })} />);
    expect(container.querySelector('[data-alarm-severity="message"]')?.textContent).toBe('Loading alarms…');
  });
});

describe('at a phone width', () => {
  it('lays the legend out as a wrapping grid that never exceeds its column, with no fixed pixel width', () => {
    const { container } = render(
      <div style={{ width: 390 }}>
        <AlarmSeverity widget={widget()} data={state(MIXED)} />
      </div>,
    );
    const list = container.querySelector('ul') as HTMLElement;
    // Auto-fit columns of at least 7.5rem (120 px): three fit 390 px, two fit a 3-cell widget, and
    // `min(100%, ...)` keeps one column from overflowing a narrower box. jsdom has no layout, so the
    // contract is the declared one.
    expect(list.style.gridTemplateColumns).toBe('repeat(auto-fit, minmax(min(100%, 7.5rem), 1fr))');
    expect(list.style.overflow).toBe('hidden');
    for (const li of container.querySelectorAll<HTMLElement>('li')) {
      expect(li.style.minWidth).toBe('0px');
      expect(li.style.width).toBe('');
      // The label truncates instead of pushing the count out of the row.
      expect((li.children[1] as HTMLElement).style.textOverflow).toBe('ellipsis');
    }
  });

  it('declares a phone-sized minimum span in the descriptor', () => {
    const d = builtinWidgetCatalog.get('alarm-severity')!;
    expect((d.layout?.minSpan?.cols ?? 99)).toBeLessThanOrEqual(3);
  });
});

// ---- Connected ---------------------------------------------------------------

function fakeHub() {
  const subs: Array<{ sub: AlarmSubscription; sink: AlarmStreamSink; disposed: boolean }> = [];
  const hub = {
    subscribeAlarms: (sub: AlarmSubscription, sink: AlarmStreamSink) => {
      const entry = { sub, sink, disposed: false };
      subs.push(entry);
      return () => {
        entry.disposed = true;
      };
    },
    isDatasourceAvailable: async () => true,
    isDatasourceSupported: async () => true,
  } as unknown as DashboardHub;
  const live = () => subs.filter((s) => !s.disposed);
  const emit = (snapshot: AlarmSnapshot) => act(() => live()[0].sink.next(snapshot));
  return { hub, subs, live, emit };
}

async function mount(options: Record<string, unknown> = {}) {
  const fake = fakeHub();
  const { container } = render(<ConnectedWidget widget={widget(options)} hub={fake.hub} />);
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
  return { ...fake, container };
}

const snapshot = (severityCounts: AlarmSeverityCount[]): AlarmSnapshot => ({
  alarms: [],
  total: severityCounts.reduce((n, c) => n + c.count, 0),
  severityCounts,
});

describe('connected', () => {
  it('reads ONE grouped count of the widget’s alarms, never the rows and never a read per severity', async () => {
    const f = await mount({ acknowledged: 'false' });
    expect(f.subs).toHaveLength(1);
    expect(f.live()[0].sub).toMatchObject({ state: 'ACTIVE', acknowledged: false, countBySeverity: true });
    // Unbound and untyped: every alarm the viewer can see, whatever its originator.
    expect(f.live()[0].sub.deviceType).toBeUndefined();
    expect(f.live()[0].sub.datasource).toBeUndefined();
    // A severity filter would make the breakdown a single bar, so the widget declares none.
    expect(f.live()[0].sub.severity).toBeUndefined();
    await f.emit(snapshot(MIXED));
    expect(legend(f.container).map(([, n]) => n)).toEqual([3, 0, 5, 4, 0]);
  });

  it('shows exactly what the server counted for a customer-scoped viewer, and widens nothing', async () => {
    // The viewer's own token reaches device-management, which counts only the alarms in the
    // customer's scope; the widget shows what comes back and adds no read of its own.
    const f = await mount();
    expect(f.subs).toHaveLength(1);
    await f.emit(snapshot([count('WARNING', 2)]));
    expect(legend(f.container).map(([, n]) => n)).toEqual([0, 0, 0, 2, 0]);
    expect(f.container.querySelector('[data-alarm-severity-total]')?.textContent).toBe('2 alarms');
  });

  it('asks the server for one device type, in the same single read', async () => {
    const f = await mount({ deviceType: 'sensor' });
    expect(f.subs).toHaveLength(1);
    expect(f.live()[0].sub).toMatchObject({ countBySeverity: true, deviceType: 'sensor' });
    await f.emit(snapshot([count('CRITICAL', 1), count('WARNING', 4)]));
    expect(legend(f.container).map(([, n]) => n)).toEqual([1, 0, 0, 4, 0]);
    expect(f.container.querySelector('[data-alarm-severity-total]')?.textContent).toBe('5 alarms on sensor devices');
  });

  it('treats a blank device type as no type', async () => {
    const f = await mount({ deviceType: '' });
    expect(f.live()[0].sub.deviceType).toBeUndefined();
  });

  it('draws the unsupported message when the source answers with rows and no counts', async () => {
    const f = await mount();
    await f.emit({ alarms: [], total: 0 });
    expect(f.container.querySelector('[data-alarm-severity="message"]')).not.toBeNull();
  });
});

describe('the descriptor', () => {
  it('derives a count subscription from its policy, with no row page to read', () => {
    const sub = alarmSubscription(builtinWidgetCatalog.get('alarm-severity')!, widget({ acknowledged: 'true' }));
    expect(sub).toMatchObject({ state: 'ACTIVE', acknowledged: true, pageSize: 1, countBySeverity: true });
    expect(sub.severity).toBeUndefined();
  });

  it('leaves every other alarm widget reading rows', () => {
    for (const id of ['alarm-table', 'alarm-count'] as const) {
      const sub = alarmSubscription(builtinWidgetCatalog.get(id)!, widget({ deviceType: 'sensor' }));
      expect(sub.countBySeverity, id).toBeUndefined();
      expect(sub.deviceType, id).toBeUndefined();
    }
  });
});
