// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm table's device-type tabs, authored in the settings panel: the Group by choice and the
// optional pinned list of device types. Drives a panel that KEEPS ITS STATE and asserts what reached
// the stored options (what the renderer reads), then feeds those options back to prove the round trip.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { validateOptions, type KnownWidgetInstance, type ResolvedWidgetInstance } from '@devicechain/dashboards';
import { WIDGET_OPTIONS } from '@devicechain/widgets';

import { WidgetConfigPanel } from './WidgetConfigPanel';

vi.mock('./EntityPicker', () => ({ EntityPicker: () => null }));

afterEach(cleanup);

const store: { options: Record<string, unknown> | undefined } = { options: undefined };
const stored = () => store.options;

function Harness({ initial }: { initial?: Record<string, unknown> }) {
  const [widget, setWidget] = useState<KnownWidgetInstance>({
    id: 'a1',
    type: 'alarm-table',
    layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 6, rows: 4 } },
    options: initial ?? {},
  });
  return (
    <WidgetConfigPanel
      widget={widget}
      datasource={undefined}
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

const field = (root: HTMLElement, key: string) => root.querySelector<HTMLElement>(`[data-testid="widget-option-field-${key}"]`)!;
const cell = (root: HTMLElement, row: number) =>
  root.querySelector<HTMLInputElement>(`[data-testid="list-cell-deviceTypes-${row}-token"] input`)!;

async function pickGroupBy(root: HTMLElement, label: string) {
  fireEvent.click(field(root, 'groupBy').querySelector('button')!);
  const choice = await waitFor(() => {
    const el = [...document.querySelectorAll<HTMLElement>('[role="dialog"] button, [role="option"]')].find(
      (b) => (b.textContent ?? '').trim() === label,
    );
    expect(el, `no "${label}" choice`).toBeTruthy();
    return el!;
  });
  fireEvent.click(choice);
}

describe('the alarm table’s device-type tabs in the settings panel', () => {
  it('offers Group by and the pinned type list, worded in English', () => {
    const { container } = render(<Harness />);
    expect(field(container, 'groupBy').textContent).toContain('Group by');
    expect(field(container, 'groupBy').textContent).toContain('Show a tab per device type');
    expect(field(container, 'deviceTypes').textContent).toContain('Device type tabs');
  });

  it('writes the choice and the pinned types, and reads them back', async () => {
    store.options = undefined;
    const first = render(<Harness />);

    await pickGroupBy(first.container, 'Device type');
    expect(stored()?.groupBy).toBe('deviceType');

    fireEvent.click(first.container.querySelector('[data-testid="list-add-deviceTypes"]')!);
    fireEvent.change(cell(first.container, 0), { target: { value: 'thermostat' } });
    fireEvent.click(first.container.querySelector('[data-testid="list-add-deviceTypes"]')!);
    fireEvent.change(cell(first.container, 1), { target: { value: 'sensor' } });
    expect(stored()?.deviceTypes).toEqual([{ token: 'thermostat' }, { token: 'sensor' }]);

    // What the editor wrote is a valid options bag for the widget...
    const written = stored()!;
    expect(validateOptions(WIDGET_OPTIONS['alarm-table'], written, { label: 'alarm-table' })).toEqual([]);

    // ...and a fresh panel opened on it shows exactly it.
    first.unmount();
    const second = render(<Harness initial={written} />);
    expect(field(second.container, 'groupBy').textContent).toContain('Device type');
    expect(cell(second.container, 0).value).toBe('thermostat');
    expect(cell(second.container, 1).value).toBe('sensor');
  });

  it('switches back to one table by choosing One table, keeping the pinned list for later', async () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ groupBy: 'deviceType', deviceTypes: [{ token: 'pump' }] }} />);
    await pickGroupBy(container, 'One table');
    expect(stored()?.groupBy).toBe('none');
    expect(stored()?.deviceTypes).toEqual([{ token: 'pump' }]);
  });

  it('is worded in Spanish too', async () => {
    await i18n.changeLanguage('es');
    try {
      const { container } = render(<Harness />);
      expect(field(container, 'groupBy').textContent).toContain('Agrupar por');
      expect(field(container, 'deviceTypes').textContent).toContain('Pestañas de tipos de dispositivo');
    } finally {
      await i18n.changeLanguage('en');
    }
  });
});
