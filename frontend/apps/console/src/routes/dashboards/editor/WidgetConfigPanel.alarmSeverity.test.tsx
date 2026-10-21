// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm severity breakdown, authored in the settings panel: its device type and its state and
// acknowledged filters. Drives a panel that KEEPS ITS STATE and asserts what reached
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
async function pickType(scope: HTMLElement, token: string, label: string) {
  fireEvent.click(scope.querySelector('button')!);
  await waitFor(() => expect(document.querySelector(`[data-shot-value="${token}"]`)).toBeTruthy());
  fireEvent.click(document.querySelector(`[data-shot-value="${token}"]`)!);
  await waitFor(() => expect(scope.textContent).toContain(label));
}

afterEach(cleanup);

const store: { options: Record<string, unknown> | undefined } = { options: undefined };
const stored = () => store.options;

function Harness({ initial }: { initial?: Record<string, unknown> }) {
  const [widget, setWidget] = useState<KnownWidgetInstance>({
    id: 'a1',
    type: 'alarm-severity',
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

const field = (root: HTMLElement, key: string) => root.querySelector<HTMLElement>(`[data-testid="widget-option-field-${key}"]`);

describe('the alarm severity breakdown in the settings panel', () => {
  it('offers the type, state and acknowledged filters, worded in English, and no severity filter', () => {
    const { container } = render(<Harness />);
    expect(field(container, 'deviceType')?.textContent).toContain('Device type');
    expect(field(container, 'deviceType')?.textContent).toContain('Count only the alarms on devices of this device type');
    expect(field(container, 'state')).not.toBeNull();
    expect(field(container, 'acknowledged')).not.toBeNull();
    expect(field(container, 'severity')).toBeNull();
  });

  it('writes the device type TOKEN from a picker that shows names, and reads it back', async () => {
    store.options = undefined;
    const first = render(<Harness />);
    expect(field(first.container, 'deviceType')!.querySelector('input')).toBeNull();
    await pickType(field(first.container, 'deviceType')!, 'thermostat', 'Thermostat');
    expect(stored()?.deviceType).toBe('thermostat');

    // What the editor wrote is a valid options bag for the widget...
    const written = { ...stored()!, state: 'ACTIVE', acknowledged: 'false' };
    expect(validateOptions(WIDGET_OPTIONS['alarm-severity'], written, { label: 'alarm-severity' })).toEqual([]);

    // ...and a fresh panel opened on it shows exactly it.
    first.unmount();
    const second = render(<Harness initial={written} />);
    await waitFor(() => expect(field(second.container, 'deviceType')!.textContent).toContain('Thermostat'));
  });

  it('shows a stored token the catalogue no longer holds as unknown, and keeps it until removed', async () => {
    store.options = undefined;
    const { container } = render(<Harness initial={{ deviceType: 'retired-kind', state: 'ACTIVE' }} />);
    await waitFor(() => expect(field(container, 'deviceType')!.textContent).toContain('unknown type retired-kind'));
    expect(stored()).toBeUndefined(); // viewing changed nothing
    fireEvent.click(field(container, 'deviceType')!.querySelector('[aria-label="Clear"]')!);
    expect(stored()?.deviceType).toBeUndefined();
  });

  it('refuses a severity filter, which it does not read', () => {
    const issues = validateOptions(WIDGET_OPTIONS['alarm-severity'], { severity: 'MAJOR' }, { label: 'alarm-severity' });
    expect(issues.map((i) => i.code)).toEqual(['unknown']);
  });

  it('is worded in Spanish too', async () => {
    await i18n.changeLanguage('es');
    try {
      const { container } = render(<Harness />);
      expect(field(container, 'deviceType')?.textContent).toContain('Tipo de dispositivo');
      await waitFor(() => expect(field(container, 'deviceType')?.textContent).toContain('Seleccionar tipo de dispositivo'));
      expect(field(container, 'deviceType')?.textContent).toContain('Cuenta solo las alarmas');
    } finally {
      await i18n.changeLanguage('en');
    }
  });
});
