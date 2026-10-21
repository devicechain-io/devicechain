// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The option forms of the stat card's trend and the fleet count, end to end against a panel that
// KEEPS ITS STATE: what the author sets reaches the stored options, and those options survive the
// definition's own parse and serialize unchanged (the round trip the renderer later reads).

import '@/i18n/config';
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  CURRENT_SCHEMA_VERSION,
  parseDashboardDefinition,
  serializeDefinition,
  type KnownWidgetInstance,
  type ResolvedWidgetInstance,
  type WidgetType,
} from '@devicechain/dashboards';

import { WidgetConfigPanel } from './WidgetConfigPanel';

vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

afterEach(cleanup);

const store: { options: Record<string, unknown> | undefined } = { options: undefined };
const stored = () => store.options;

function Harness({ type, initial }: { type: WidgetType; initial?: Record<string, unknown> }) {
  const [widget, setWidget] = useState<KnownWidgetInstance>({
    id: 'w1',
    type,
    layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 3, rows: 3 } },
    options: initial ?? {},
  });
  return (
    <WidgetConfigPanel
      widget={widget}
      datasource={{ kind: 'device', deviceToken: 'dev-1', measurements: ['temp'] }}
      slots={{}}
      onChange={(next: ResolvedWidgetInstance) => {
        setWidget(next as KnownWidgetInstance);
        store.options = next.options as Record<string, unknown> | undefined;
      }}
      onDatasource={vi.fn()}
      onScope={vi.fn()}
      onClose={vi.fn()}
    />
  );
}

const field = (key: string) => screen.getByTestId(`widget-option-field-${key}`);
const tr = (key: string) => i18n.t(key, { ns: 'dashboards' });

// What the renderer reads after a save and a reload: the options pass through the definition
// parser and its serializer untouched.
function roundTrip(type: string, options: Record<string, unknown>): Record<string, unknown> | undefined {
  const def = parseDashboardDefinition({
    schemaVersion: CURRENT_SCHEMA_VERSION,
    widgets: [
      {
        id: 'w1',
        type,
        layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
        datasource:
          type === 'fleet-count'
            ? { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements: ['fuel'] }
            : { kind: 'device', deviceToken: 'dev-1', measurements: ['temp'] },
        options,
      },
    ],
  });
  const again = parseDashboardDefinition(JSON.parse(serializeDefinition(def)));
  return again.widgets[0].options as Record<string, unknown> | undefined;
}

describe('the stat card trend option', () => {
  it('is a labelled checkbox that is off until the author turns it on, and writes true then drops the key', () => {
    store.options = undefined;
    render(<Harness type="stat-card" />);
    expect(field('sparkline').textContent).toContain(tr('widgetLabelSparkline'));
    expect(field('sparkline').textContent).toContain(tr('widgetHintSparkline'));
    const box = field('sparkline').querySelector<HTMLElement>('[role="checkbox"]')!;
    expect(box.getAttribute('aria-checked')).toBe('false');

    fireEvent.click(box);
    expect(stored()?.sparkline).toBe(true);
    expect(field('sparkline').querySelector('[role="checkbox"]')!.getAttribute('aria-checked')).toBe('true');

    fireEvent.click(field('sparkline').querySelector('[role="checkbox"]')!);
    expect(stored()?.sparkline).toBeUndefined();
  });

  it('survives the definition parse and serialize with its neighbours', () => {
    const options = { title: 'Peak', measurement: 'temp', stat: 'max', sparkline: true, windowSeconds: 3600 };
    expect(roundTrip('stat-card', options)).toEqual(options);
  });
});

describe('the fleet count options', () => {
  it('offers its fields in the order the author reads them', () => {
    render(<Harness type="fleet-count" />);
    const ids = screen
      .queryAllByTestId(/^widget-option-field-/)
      .map((el) => el.getAttribute('data-testid')!.replace('widget-option-field-', ''));
    expect(ids).toEqual(['title', 'show', 'measurement', 'thresholds', 'staleAfterSeconds', 'windowSeconds']);
  });

  it('words the count choice, and writes the measurement the author types', () => {
    store.options = undefined;
    render(<Harness type="fleet-count" />);
    expect(field('show').textContent).toContain(tr('widgetLabelFleetShow'));
    expect(field('show').textContent).toContain(tr('widgetHintFleetShow'));
    fireEvent.change(field('measurement').querySelector('input')!, { target: { value: 'fuel' } });
    expect(stored()?.measurement).toBe('fuel');
  });

  it('adds a threshold band row the card counts against', () => {
    store.options = undefined;
    const { container } = render(<Harness type="fleet-count" initial={{ measurement: 'fuel' }} />);
    fireEvent.click(container.querySelector('[data-testid="list-add-thresholds"]')!);
    expect(stored()?.thresholds).toEqual([{ value: 0, level: 'normal' }]);
  });

  it('survives the definition parse and serialize, attention and devices alike', () => {
    const attention = {
      title: 'Low fuel',
      show: 'attention',
      measurement: 'fuel',
      thresholds: [
        { value: 0, level: 'critical', label: 'Low' },
        { value: 15, level: 'normal' },
      ],
      staleAfterSeconds: 300,
      windowSeconds: 3600,
    };
    expect(roundTrip('fleet-count', attention)).toEqual(attention);
    expect(roundTrip('fleet-count', { title: 'Machines', show: 'devices' })).toEqual({ title: 'Machines', show: 'devices' });
  });
});
