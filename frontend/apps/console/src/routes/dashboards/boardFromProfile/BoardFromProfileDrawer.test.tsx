// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The flow end to end around the real generator, parser and registry: read the profile, preview,
// create a DRAFT under the typed token, land in the editor. Faked: the two APIs it talks to (the
// dashboard create is the only write) and the token field, which polls the network.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/lib/api/dashboards', () => ({
  createDashboard: vi.fn(),
  getDashboard: vi.fn(),
  updateDashboard: vi.fn(),
  publishDashboard: vi.fn(),
}));
vi.mock('@/lib/api/device-management', () => ({
  listDevices: vi.fn(),
  listCommandDefinitions: vi.fn(),
  listMetricDefinitions: vi.fn(),
  getDeviceMetricVocabulary: vi.fn(),
  getDeviceCommandVocabulary: vi.fn(),
}));
vi.mock('@/components/ui/token-field', () => ({
  TokenField: (p: { id?: string; value: string; onChange: (v: string) => void }) => (
    <input aria-label="token" id={p.id} value={p.value} onChange={(e) => p.onChange(e.target.value)} />
  ),
}));

import { createDashboard, publishDashboard, updateDashboard } from '@/lib/api/dashboards';
import {
  getDeviceCommandVocabulary,
  getDeviceMetricVocabulary,
  listCommandDefinitions,
  listDevices,
  listMetricDefinitions,
} from '@/lib/api/device-management';
import { BoardFromProfileDrawer } from './BoardFromProfileDrawer';

const asReturn = <T,>(v: unknown) => v as T;

const draftMetric = (metricKey: string, extra: Record<string, unknown> = {}) => ({
  metricKey,
  dataType: 'DOUBLE',
  unit: null,
  calculated: false,
  ...extra,
});
const draftCommand = (commandKey: string, impact: string) => ({
  commandKey,
  name: commandKey,
  description: null,
  parameterSchema: null,
  impact,
});

function Landing() {
  return <p>landed {useLocation().pathname}</p>;
}

function renderDrawer(typeToken = 'boiler') {
  return render(
    <MemoryRouter initialEntries={['/device-types/' + typeToken]}>
      <Routes>
        <Route
          path="/device-types/:t"
          element={
            <BoardFromProfileDrawer
              open
              onOpenChange={() => {}}
              typeToken={typeToken}
              typeName="Boiler"
              profileToken="boiler-profile"
            />
          }
        />
        <Route path="/dashboards/:token" element={<Landing />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  for (const fn of [
    createDashboard,
    listDevices,
    listCommandDefinitions,
    listMetricDefinitions,
    getDeviceMetricVocabulary,
    getDeviceCommandVocabulary,
  ]) {
    vi.mocked(fn).mockReset();
  }
  vi.mocked(createDashboard).mockImplementation(async (o) => ({ token: o.token }));
  vi.mocked(listCommandDefinitions).mockResolvedValue(
    asReturn([draftCommand('flush', 'NORMAL'), draftCommand('reboot', 'DISRUPTIVE')]),
  );
});
afterEach(cleanup);

describe('BoardFromProfileDrawer', () => {
  it('previews the layout, creates a DRAFT under the typed token and opens the editor', async () => {
    vi.mocked(listDevices).mockResolvedValue(asReturn({ results: [{ token: 'boiler-1' }] }));
    vi.mocked(getDeviceMetricVocabulary).mockResolvedValue(
      asReturn([draftMetric('temperature', { unit: 'C' }), draftMetric('heatIndex', { calculated: true })]),
    );
    vi.mocked(getDeviceCommandVocabulary).mockResolvedValue(
      asReturn({ constrained: true, commands: [draftCommand('flush', 'x'), draftCommand('reboot', 'x')] }),
    );
    renderDrawer();

    // The preview names what will be built, calculated metrics marked and the unsafe command noted.
    expect(await screen.findByText('2 measurement cards')).toBeTruthy();
    expect(screen.getByText('heatIndex (Calculated)')).toBeTruthy();
    expect(screen.getByText('1 command button')).toBeTruthy();
    expect(screen.getAllByText(/1 command was left out/).length).toBeGreaterThan(0);

    // Creation waits for a token.
    const create = screen.getByRole('button', { name: 'Create draft and open editor' }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('token'), { target: { value: 'boiler-board' } });
    fireEvent.click(create);

    expect(await screen.findByText('landed /dashboards/boiler-board')).toBeTruthy();
    expect(createDashboard).toHaveBeenCalledTimes(1);
    const call = vi.mocked(createDashboard).mock.calls[0][0];
    expect(call).toMatchObject({ token: 'boiler-board', name: 'Boiler dashboard' });
    const stored = JSON.parse(call.definition ?? '') as {
      title: string;
      slots: Record<string, { defaultBinding?: unknown }>;
      widgets: Array<{ type: string }>;
    };
    expect(stored.title).toBe('Boiler dashboard');
    expect(stored.slots.device.defaultBinding).toEqual({ kind: 'device', deviceToken: 'boiler-1' });
    expect(stored.widgets.map((w) => w.type)).toContain('command-button');
    // A draft: created, never overwritten, never published.
    expect(updateDashboard).not.toHaveBeenCalled();
    expect(publishDashboard).not.toHaveBeenCalled();
  });

  it('joins the published command list with the draft for impact, so an unknown one gets no button', async () => {
    vi.mocked(listDevices).mockResolvedValue(asReturn({ results: [{ token: 'boiler-1' }] }));
    vi.mocked(getDeviceMetricVocabulary).mockResolvedValue(asReturn([draftMetric('temperature')]));
    // `ghost` is published but has no draft row: its impact is unknown.
    vi.mocked(getDeviceCommandVocabulary).mockResolvedValue(
      asReturn({ constrained: true, commands: [draftCommand('flush', 'x'), draftCommand('ghost', 'x')] }),
    );
    renderDrawer();
    expect(await screen.findByText('1 command button')).toBeTruthy();
    expect(screen.getAllByText(/1 command was left out/).length).toBeGreaterThan(0);
  });

  it('falls back to the profile draft for a type with no devices, and says so', async () => {
    vi.mocked(listDevices).mockResolvedValue(asReturn({ results: [] }));
    vi.mocked(listMetricDefinitions).mockResolvedValue(asReturn([draftMetric('temperature')]));
    renderDrawer();
    expect(await screen.findByText('1 measurement card')).toBeTruthy();
    expect(screen.getByText(/no devices yet/)).toBeTruthy();
    expect(screen.getByText(/Built from the profile's draft/)).toBeTruthy();
    expect(getDeviceMetricVocabulary).not.toHaveBeenCalled();
  });

  it('refuses a profile with no measurements, with no way to create', async () => {
    vi.mocked(listDevices).mockResolvedValue(asReturn({ results: [] }));
    vi.mocked(listMetricDefinitions).mockResolvedValue(asReturn([]));
    renderDrawer();
    expect(await screen.findByText(/declares no measurements/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create draft and open editor' })).toBeNull();
    expect(createDashboard).not.toHaveBeenCalled();
  });

  it('shows a failed create and stays on the form', async () => {
    vi.mocked(listDevices).mockResolvedValue(asReturn({ results: [] }));
    vi.mocked(listMetricDefinitions).mockResolvedValue(asReturn([draftMetric('temperature')]));
    vi.mocked(createDashboard).mockRejectedValue(new Error('token taken'));
    renderDrawer();
    await screen.findByText('1 measurement card');
    fireEvent.change(screen.getByLabelText('token'), { target: { value: 'x' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create draft and open editor' }));
    await waitFor(() => expect(screen.getByText(/token taken/)).toBeTruthy());
    expect(screen.queryByText(/landed/)).toBeNull();
  });
});
