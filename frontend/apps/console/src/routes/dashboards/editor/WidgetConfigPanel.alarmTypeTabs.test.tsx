// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm table's device-type tabs, authored in the settings panel: the Group by choice and the
// optional pinned list of device types. Drives a panel that KEEPS ITS STATE and asserts what reached
// the stored options (what the renderer reads), then feeds those options back to prove the round trip.

import '@/i18n/config';
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { validateOptions, type KnownWidgetInstance, type ResolvedWidgetInstance } from '@devicechain/dashboards';
import { WIDGET_OPTIONS } from '@devicechain/widgets';

import { WidgetConfigPanel } from './WidgetConfigPanel';


// The device-type catalogue the pickers read. The real EntityPicker and Combobox run on top of it.
const { listDeviceTypes } = vi.hoisted(() => ({ listDeviceTypes: vi.fn() }));
vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api/device-management')>()),
  listDeviceTypes,
  listDevices: vi.fn().mockResolvedValue({ results: [] }),
}));
const CATALOGUE = [
  { token: 'thermostat', name: 'Thermostat' },
  { token: 'sensor', name: 'Sensor' },
  { token: 'pump', name: 'Pump' },
];
beforeEach(() => {
  listDeviceTypes.mockReset();
  listDeviceTypes.mockResolvedValue({ results: CATALOGUE, pagination: { totalRecords: CATALOGUE.length } });
});

// Open the picker inside `scope` and choose the option for `token`.
// Opened only once the picker's catalogue has loaded, and the option is found and clicked in ONE
// step: opened mid-load, the list re-rendered under the test between finding the option and
// clicking it, and the click landed on nothing (intermittently).
async function pickType(scope: HTMLElement, token: string, label: string) {
  await waitFor(() => expect(scope.textContent).not.toContain('Loading'));
  fireEvent.click(scope.querySelector('button')!);
  await waitFor(() => {
    const option = document.querySelector<HTMLElement>(`[data-shot-value="${token}"]`);
    expect(option).toBeTruthy();
    fireEvent.click(option!);
    expect(scope.textContent).toContain(label);
  });
}

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
  root.querySelector<HTMLElement>(`[data-testid="list-cell-deviceTypes-${row}-token"]`)!;
const addRow = (root: HTMLElement) => fireEvent.click(root.querySelector('[data-testid="list-add-deviceTypes"]')!);
const rowButton = (root: HTMLElement, name: string) =>
  root.querySelector<HTMLButtonElement>(`[data-testid="widget-option-field-deviceTypes"] button[aria-label="${name}"]`)!;

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

    addRow(first.container);
    await pickType(cell(first.container, 0), 'thermostat', 'Thermostat');
    addRow(first.container);
    await pickType(cell(first.container, 1), 'sensor', 'Sensor');
    expect(stored()?.deviceTypes).toEqual([{ token: 'thermostat' }, { token: 'sensor' }]);

    // What the editor wrote is a valid options bag for the widget...
    const written = stored()!;
    expect(validateOptions(WIDGET_OPTIONS['alarm-table'], written, { label: 'alarm-table' })).toEqual([]);

    // ...and a fresh panel opened on it shows exactly it.
    first.unmount();
    const second = render(<Harness initial={written} />);
    expect(field(second.container, 'groupBy').textContent).toContain('Device type');
    await waitFor(() => expect(cell(second.container, 0).textContent).toContain('Thermostat'));
    expect(cell(second.container, 1).textContent).toContain('Sensor');
  });

  it('reorders with move up / move down, never past either end', () => {
    store.options = undefined;
    const { container } = render(
      <Harness initial={{ groupBy: 'deviceType', deviceTypes: [{ token: 'a' }, { token: 'b' }, { token: 'c' }] }} />,
    );
    expect(rowButton(container, 'Move row 1 up').disabled).toBe(true);
    expect(rowButton(container, 'Move row 3 down').disabled).toBe(true);
    fireEvent.click(rowButton(container, 'Move row 3 up'));
    expect(stored()?.deviceTypes).toEqual([{ token: 'a' }, { token: 'c' }, { token: 'b' }]);
    fireEvent.click(rowButton(container, 'Move row 1 down'));
    expect(stored()?.deviceTypes).toEqual([{ token: 'c' }, { token: 'a' }, { token: 'b' }]);
  });

  it('keeps keyboard focus on the moved row, switching arrows when it reaches an end', async () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ deviceTypes: [{ token: 'a' }, { token: 'b' }, { token: 'c' }] }} />);
    const down1 = rowButton(container, 'Move row 1 down');
    down1.focus();
    fireEvent.click(down1);
    expect(stored()?.deviceTypes).toEqual([{ token: 'b' }, { token: 'a' }, { token: 'c' }]);
    // The moved entry is now row 2, and focus went with it: pressing again moves the SAME entry.
    await waitFor(() => expect(document.activeElement).toBe(rowButton(container, 'Move row 2 down')));
    fireEvent.click(document.activeElement as HTMLElement);
    expect(stored()?.deviceTypes).toEqual([{ token: 'b' }, { token: 'c' }, { token: 'a' }]);
    // At the bottom its down arrow is disabled, so focus lands on its up arrow, not on the page.
    await waitFor(() => expect(document.activeElement).toBe(rowButton(container, 'Move row 3 up')));
  });

  it('removes a row, and drops the option when the last one goes', () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ deviceTypes: [{ token: 'a' }, { token: 'b' }] }} />);
    fireEvent.click(rowButton(container, 'Remove row 1'));
    expect(stored()?.deviceTypes).toEqual([{ token: 'b' }]);
    fireEvent.click(rowButton(container, 'Remove row 1'));
    expect(stored()?.deviceTypes).toBeUndefined();
  });

  it('shows a stored token the catalogue lacks as unknown, with a remove action, never dropping it', async () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ deviceTypes: [{ token: 'thermostat' }, { token: 'gone' }] }} />);
    await waitFor(() => expect(cell(container, 1).textContent).toContain('unknown type gone'));
    expect(cell(container, 0).textContent).toContain('Thermostat');
    expect(stored()).toBeUndefined();
    fireEvent.click(rowButton(container, 'Remove row 2'));
    expect(stored()?.deviceTypes).toEqual([{ token: 'thermostat' }]);
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
