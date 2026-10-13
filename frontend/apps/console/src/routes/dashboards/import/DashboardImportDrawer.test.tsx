// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The import drawer end to end, around the real parser, registry and pipeline: a binding the
// tenant does not have is put to the user before anything is saved, the new dashboard is
// created under the token the user typed with the repaired definition, bad input is refused
// without a write, and a clone arrives already reviewed.
//
// Faked: the dashboard API (the only write), the entity lookup, and two inputs that talk to
// the network (the token field and the entity picker).

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ have: new Set<string>() }));

vi.mock('@/lib/api/dashboards', () => ({
  createDashboard: vi.fn(),
  getDashboard: vi.fn(),
}));
vi.mock('./entityExists', () => ({
  entityExists: async (_kind: string, token: string) => h.have.has(token),
}));
vi.mock('@/components/ui/token-field', () => ({
  TokenField: (p: { id?: string; value: string; onChange: (v: string) => void }) => (
    <input aria-label="token" id={p.id} value={p.value} onChange={(e) => p.onChange(e.target.value)} />
  ),
}));
vi.mock('../editor/EntityPicker', () => ({
  EntityPicker: (p: { id?: string; kind: string; value: string; onChange: (v: string) => void }) => (
    <input aria-label={`pick-${p.kind}`} id={p.id} value={p.value} onChange={(e) => p.onChange(e.target.value)} />
  ),
}));

import { createDashboard } from '@/lib/api/dashboards';
import { DashboardImportDrawer } from './DashboardImportDrawer';
import { DASHBOARD_TEMPLATES } from './templates';

const box = { base: { col: 0, colSpan: 6, row: 0, rowSpan: 4, z: 0 } };
const exported = JSON.stringify({
  schemaVersion: 1,
  title: 'Plant',
  widgets: [
    {
      id: 'w1',
      type: 'latest-card',
      layout: box,
      options: { title: 'T', measurement: 'temperature' },
      datasource: { kind: 'slot', slot: 'press', measurements: ['temperature'] },
    },
  ],
  slots: { press: { type: 'device', label: 'Press', defaultBinding: { kind: 'device', deviceToken: 'press-7' } } },
});

function open(source: { text?: string; name?: string }, onCreated = vi.fn()) {
  render(<DashboardImportDrawer open onOpenChange={() => {}} title="Import" source={source} onCreated={onCreated} />);
  return onCreated;
}

beforeEach(() => {
  h.have = new Set();
  vi.mocked(createDashboard).mockReset();
  vi.mocked(createDashboard).mockImplementation(async (o) => ({ token: o.token }));
});
afterEach(cleanup);

describe('DashboardImportDrawer', () => {
  it('prompts for a binding this tenant does not have, and creates a draft with the replacement', async () => {
    h.have.add('press-9');
    const onCreated = open({ text: exported });
    // The foreign device is named, with a picker for a replacement.
    await screen.findByText(/“press-7” does not exist in this tenant/);
    fireEvent.change(screen.getByLabelText('pick-device'), { target: { value: 'press-9' } });
    fireEvent.change(screen.getByLabelText('token'), { target: { value: 'plant-copy' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create draft' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledWith('plant-copy'));
    expect(createDashboard).toHaveBeenCalledTimes(1);
    const call = vi.mocked(createDashboard).mock.calls[0][0];
    expect(call.token).toBe('plant-copy');
    const stored = JSON.parse(call.definition ?? '') as { slots: Record<string, { defaultBinding?: unknown }> };
    expect(stored.slots.press.defaultBinding).toEqual({ kind: 'device', deviceToken: 'press-9' });
  });

  it('imports the slot unbound when no replacement is picked', async () => {
    open({ text: exported });
    await screen.findByText(/does not exist in this tenant/);
    fireEvent.change(screen.getByLabelText('token'), { target: { value: 'plant-copy' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create draft' }));
    await waitFor(() => expect(createDashboard).toHaveBeenCalled());
    const stored = JSON.parse(vi.mocked(createDashboard).mock.calls[0][0].definition ?? '') as {
      slots: Record<string, { defaultBinding?: unknown; label?: string }>;
    };
    expect(stored.slots.press.defaultBinding).toBeUndefined();
    expect(stored.slots.press.label).toBe('Press');
  });

  it('asks nothing when every binding exists', async () => {
    h.have.add('press-7');
    open({ text: exported });
    await screen.findByRole('button', { name: 'Create draft' });
    expect(screen.queryByText(/Bindings to review/)).toBeNull();
  });

  it('will not create without a token', async () => {
    open({ text: exported });
    const create = await screen.findByRole('button', { name: 'Create draft' });
    expect((create as HTMLButtonElement).disabled).toBe(true);
    expect(createDashboard).not.toHaveBeenCalled();
  });

  it('refuses pasted text that is not a dashboard, without a write', async () => {
    open({});
    fireEvent.change(screen.getByLabelText('Dashboard JSON'), { target: { value: '{"title":"x"}' } });
    fireEvent.click(screen.getByRole('button', { name: 'Review' }));
    await screen.findByText(/not a dashboard definition this console can read/);
    expect(createDashboard).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Create draft' })).toBeNull();
  });

  it('refuses an oversized paste before parsing it', () => {
    open({});
    const big = JSON.stringify({ widgets: [], pad: 'x'.repeat((1 << 20) + 10) });
    fireEvent.change(screen.getByLabelText('Dashboard JSON'), { target: { value: big } });
    expect((screen.getByRole('button', { name: 'Review' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/larger than the 1 MiB limit/)).toBeTruthy();
  });

  it('lists a widget this console cannot resolve, and keeps it in the draft', async () => {
    const withUnknown = JSON.stringify({
      ...JSON.parse(exported),
      widgets: [{ id: 'x1', type: 'vendor.ring', layout: box, options: { a: 1 } }],
    });
    h.have.add('press-7');
    open({ text: withUnknown });
    await screen.findByText(/cannot resolve/);
    fireEvent.change(screen.getByLabelText('token'), { target: { value: 'ring-copy' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create draft' }));
    await waitFor(() => expect(createDashboard).toHaveBeenCalled());
    const stored = JSON.parse(vi.mocked(createDashboard).mock.calls[0][0].definition ?? '') as {
      widgets: Array<{ type: string }>;
    };
    expect(stored.widgets[0].type).toBe('vendor.ring');
  });

  it('a template asks for a device to bind, and offers to leave it unbound', async () => {
    const detail = DASHBOARD_TEMPLATES.find((t) => t.id === 'device-detail')!;
    open({ text: detail.json, name: 'Device detail' });
    await screen.findByText('Bindings to review');
    expect(screen.getByText('Not bound yet.')).toBeTruthy();
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Device detail');
  });
});
