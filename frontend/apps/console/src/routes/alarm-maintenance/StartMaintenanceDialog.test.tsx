// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const h = vi.hoisted(() => ({ gql: vi.fn(), authorities: ['alarm:write'] }));
vi.mock('@devicechain/client', async original => ({ ...await original<typeof import('@devicechain/client')>(), gql: h.gql }));
vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: { authorities: h.authorities } }) }));
import StartMaintenanceDialog from './StartMaintenanceDialog';
afterEach(cleanup);
beforeEach(() => { h.authorities = ['alarm:write']; h.gql.mockReset().mockResolvedValue({ createAlarmSuppressionWindow: { token: 'new' } }); });
const submit = () => screen.getByRole('button', { name: 'Start maintenance' }) as HTMLButtonElement;
describe('quick maintenance declaration', () => {
  it('defaults to a narrow one-hour ONE_SHOT with critical bypass and sends the page target', async () => {
    const done = vi.fn();
    render(<StartMaintenanceDialog target={{ type: 'DEVICE', token: 'pump-1' }} onClose={vi.fn()} onDone={done} />);
    expect(screen.getByRole('radio', { name: '1 h' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByText('Critical')).toBeTruthy();
    expect(screen.queryByText(/AND notification:write/)).toBeNull();
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Replace pump seal' } });
    fireEvent.click(submit());
    await waitFor(() => expect(done).toHaveBeenCalled());
    const request = h.gql.mock.calls[0][2].request;
    expect(request).toMatchObject({ kind: 'ONE_SHOT', targets: [{ type: 'DEVICE', token: 'pump-1' }], reason: 'Replace pump seal', bypassSeverity: 'CRITICAL', alarmKeys: [] });
    expect(Date.parse(request.endTime) - Date.parse(request.startTime)).toBe(3600000);
    expect(Math.abs(Date.parse(request.startTime) - Date.now())).toBeLessThan(5000);
  });
  it.each(['AREA', 'ENTITY_GROUP', 'ASSET'])('explains broad permissions for %s and refuses submission', type => {
    render(<StartMaintenanceDialog target={{ type, token: 'population' }} onClose={vi.fn()} onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Service' } });
    expect(screen.getByText(/AND notification:write/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
    expect(screen.getByRole('status').textContent).toMatch(/permissions/);
  });
  it('reclassifies a custom device window longer than 24 hours', () => {
    render(<StartMaintenanceDialog target={{ type: 'DEVICE', token: 'pump' }} onClose={vi.fn()} onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Service' } });
    expect(submit().disabled).toBe(false);
    fireEvent.click(screen.getByRole('radio', { name: 'Custom end' }));
    const date = new Date(Date.now() + 26 * 3600000);
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    fireEvent.change(screen.getByLabelText('End time'), { target: { value: local } });
    expect(screen.getByText(/AND notification:write/)).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });
  it('shows a server refusal in the open dialog', async () => {
    h.gql.mockRejectedValue(new Error('Outside your device scope'));
    render(<StartMaintenanceDialog target={{ type: 'DEVICE', token: 'pump' }} onClose={vi.fn()} onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Reason (required)'), { target: { value: 'Service' } });
    fireEvent.click(submit());
    expect(await screen.findByText('Outside your device scope')).toBeTruthy();
  });
});
