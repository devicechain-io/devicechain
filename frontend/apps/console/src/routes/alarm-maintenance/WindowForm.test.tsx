// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn() }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: ['*'] } }) }));
import { WindowForm } from './WindowForm';
import { editable, isBroad, partialUpdate, HOUR } from './rules';
import type { MaintenanceWindow } from '@/lib/api/alarm-maintenance';
export const stored: MaintenanceWindow = {
  token: 'pump-service', name: 'Pump service', description: null, reason: 'Seal replacement',
  targets: [{ type: 'DEVICE', token: 'pump' }], alarmKeys: [], bypassSeverity: 'CRITICAL', kind: 'ONE_SHOT',
  startTime: '2099-01-01T00:00:00Z', endTime: '2099-01-01T01:00:00Z', timezone: null, slots: null,
  validFrom: null, validUntil: null, endedAt: null, status: 'ACTIVE', updatedAt: null, openings: [],
};
afterEach(cleanup);
beforeEach(() => { h.gql.mockReset().mockResolvedValue({ createAlarmSuppressionWindow: { token: 'new' }, updateAlarmSuppressionWindow: { token: stored.token } }); });
describe('alarm window editor', () => {
  it('creates ONE_SHOT with a typed target and bounded interval', async () => {
    render(<WindowForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'service' } });
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Replace seal' } });
    fireEvent.change(screen.getByLabelText('Target token'), { target: { value: 'pump' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create alarm maintenance window' }));
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(1));
    expect(h.gql.mock.calls[0][2].request).toMatchObject({ token: 'service', kind: 'ONE_SHOT', reason: 'Replace seal', targets: [{ type: 'DEVICE', token: 'pump' }], bypassSeverity: 'CRITICAL' });
  });
  it('creates RECURRING with SlotEditor canonical JSON and previews after save', async () => {
    render(<WindowForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'weekly' } });
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Weekly service' } });
    fireEvent.change(screen.getByLabelText('Target token'), { target: { value: 'pump' } });
    fireEvent.click(screen.getByRole('button', { name: /Schedule One shot/ }));
    fireEvent.click(await screen.findByRole('button', { name: 'Recurring' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add slot' }));
    expect(screen.getAllByTestId('slot-row')).toHaveLength(2);
    fireEvent.click(screen.getByRole('button', { name: 'Create alarm maintenance window' }));
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(1));
    const request = h.gql.mock.calls[0][2].request;
    expect(request).toMatchObject({ kind: 'RECURRING', startTime: null, endTime: null });
    expect(JSON.parse(request.slots)).toEqual(Array(2).fill({ day: 'SATURDAY', start: '02:00', durationMinutes: 120 }));
    expect(request.timezone).toBeTruthy();
    expect(screen.getByText(/Save to preview the next five/)).toBeTruthy();
  });
  it('sends only a changed field and keeps the form mounted on save', async () => {
    render(<WindowForm entity={stored} onDone={vi.fn()} />);
    expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Updated name' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(1));
    expect(h.gql.mock.calls[0][2]).toEqual({ token: stored.token, request: { name: 'Updated name' } });
  });
  it('preserves comma keys on a name-only save and appends a new key verbatim', async () => {
    render(<WindowForm entity={{ ...stored, alarmKeys: ['pressure,high'] }} onDone={vi.fn()} />);
    expect((screen.getByLabelText('Alarm key 1') as HTMLInputElement).value).toBe('pressure,high');
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'New name' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(1));
    expect(h.gql.mock.calls[0][2].request).toEqual({ name: 'New name' });
    fireEvent.click(screen.getByRole('button', { name: 'Add alarm key' }));
    fireEvent.change(screen.getByLabelText('Alarm key 2'), { target: { value: ' temperature,high ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(h.gql).toHaveBeenCalledTimes(2));
    expect(h.gql.mock.calls[1][2].request).toEqual({ alarmKeys: ['pressure,high', ' temperature,high '] });
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove alarm key' })[1]);
    expect(screen.queryByLabelText('Alarm key 2')).toBeNull();
  });
});
describe('reach and partial updates', () => {
  it('treats recurring, populations and >24h intervals as broad', () => {
    expect(isBroad(stored)).toBe(false);
    expect(isBroad({ ...stored, kind: 'RECURRING' })).toBe(true);
    expect(isBroad({ ...stored, targets: [{ type: 'TENANT' }] })).toBe(true);
    expect(isBroad({ ...stored, endTime: new Date(Date.parse(stored.startTime!) + 24 * HOUR).toISOString() })).toBe(false);
    expect(isBroad({ ...stored, endTime: new Date(Date.parse(stored.startTime!) + 24 * HOUR + 1).toISOString() })).toBe(true);
  });
  it('omits untouched fields and explicitly clears changed nullable fields', () => {
    const baseline = editable(stored);
    expect(partialUpdate(baseline, baseline)).toEqual({});
    expect(partialUpdate(baseline, { ...baseline, name: null, bypassSeverity: null })).toEqual({ name: null, bypassSeverity: null });
  });
});
