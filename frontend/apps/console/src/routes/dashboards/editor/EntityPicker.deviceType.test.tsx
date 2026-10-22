// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device-type kind of EntityPicker: search by name or token, names shown, the TOKEN stored,
// a stored token the catalogue lacks named as unknown (never dropped), and no accusation of
// "unknown" from a read that proves nothing (still loading, failed, or only the first page).
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { listDeviceTypes, getDeviceType } = vi.hoisted(() => ({ listDeviceTypes: vi.fn(), getDeviceType: vi.fn() }));
vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api/device-management')>()),
  listDeviceTypes,
  getDeviceType,
  listDevices: vi.fn(),
}));

import { EntityPicker } from './EntityPicker';

afterEach(cleanup);
beforeEach(() => {
  listDeviceTypes.mockReset();
  getDeviceType.mockReset();
  getDeviceType.mockRejectedValue(new Error('getDeviceType not stubbed for this test'));
  listDeviceTypes.mockResolvedValue({
    results: [
      { token: 'thermostat', name: 'Thermostat' },
      { token: 'pump-3', name: 'Booster pump' },
      { token: 'bare', name: null },
    ],
    pagination: { totalRecords: 3 },
  });
});

// The clear affordance is itself role=button once a value is chosen, so name the trigger by its marker.
const trigger = () => document.querySelector<HTMLElement>('[data-shot="combobox"]')!;
const open = () => fireEvent.click(trigger());
const optionTokens = () =>
  [...document.querySelectorAll('[data-shot="combobox-option"]')].map((o) => o.getAttribute('data-shot-value'));

describe('EntityPicker kind="deviceType"', () => {
  it('lists names, searches by name and by token, and stores the token', async () => {
    const onChange = vi.fn();
    render(<EntityPicker kind="deviceType" value="" onChange={onChange} />);
    await waitFor(() => expect(trigger().textContent).toContain('Select device type'));
    open();
    await waitFor(() => expect(optionTokens()).toEqual(['thermostat', 'pump-3', 'bare']));
    expect(document.body.textContent).toContain('Booster pump');

    const search = screen.getByPlaceholderText('Search device types…');
    fireEvent.change(search, { target: { value: 'booster' } });
    expect(optionTokens()).toEqual(['pump-3']);
    fireEvent.change(search, { target: { value: 'thermo' } });
    expect(optionTokens()).toEqual(['thermostat']);

    fireEvent.click(document.querySelector('[data-shot-value="thermostat"]')!);
    expect(onChange).toHaveBeenCalledWith('thermostat');
  });

  it('is operable from the keyboard: arrows move, Enter chooses', async () => {
    const onChange = vi.fn();
    render(<EntityPicker kind="deviceType" value="" onChange={onChange} ariaLabel="Device type 1" />);
    await waitFor(() => expect(listDeviceTypes).toHaveBeenCalled());
    open();
    await waitFor(() => expect(optionTokens().length).toBe(3));
    const search = screen.getByPlaceholderText('Search device types…');
    fireEvent.keyDown(search, { key: 'ArrowDown' });
    fireEvent.keyDown(search, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith('pump-3');
  });

  it('names its control: the trigger carries the label and the current choice', async () => {
    render(<EntityPicker kind="deviceType" value="thermostat" onChange={vi.fn()} ariaLabel="Device type 1" />);
    await waitFor(() => expect(trigger().textContent).toContain('Thermostat'));
    const t = trigger();
    const names = t
      .getAttribute('aria-labelledby')!
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent);
    expect(names.join(' ')).toContain('Device type 1');
    expect(names.join(' ')).toContain('Thermostat');
  });

  it('shows a stored token the catalogue lacks as "unknown type <token>" and still offers it', async () => {
    render(<EntityPicker kind="deviceType" value="retired" onChange={vi.fn()} />);
    // Until the read answers, the bare token: nothing is known yet.
    expect(trigger().textContent).toContain('retired');
    expect(trigger().textContent).not.toContain('unknown');
    await waitFor(() => expect(trigger().textContent).toContain('unknown type retired'));
    open();
    expect(optionTokens()).toContain('retired');
  });

  it('shows the whole "unknown type <token>" text: it wraps rather than truncating, and carries a title', async () => {
    const token = 'a-long-retired-device-type-token-that-would-not-fit-in-a-narrow-row';
    render(<EntityPicker kind="deviceType" value={token} onChange={vi.fn()} />);
    await waitFor(() => expect(trigger().textContent).toContain(`unknown type ${token}`));
    const label = trigger().querySelector('span')!;
    expect(label.className).not.toContain('truncate');
    expect(label.className).toContain('break-all');
    expect(label.getAttribute('title')).toBe(`unknown type ${token}`);
    // A name that resolves keeps the single-line form, with the full name on hover.
    cleanup();
    render(<EntityPicker kind="deviceType" value="thermostat" onChange={vi.fn()} />);
    await waitFor(() => expect(trigger().textContent).toContain('Thermostat'));
    expect(trigger().querySelector('span')!.className).toContain('truncate');
    expect(trigger().querySelector('span')!.getAttribute('title')).toBe('Thermostat');
  });

  it('resolves a stored token beyond the first page by token, and names it', async () => {
    listDeviceTypes.mockResolvedValue({ results: [{ token: 'a', name: 'A' }], pagination: { totalRecords: 500 } });
    getDeviceType.mockResolvedValue({ token: 'on-page-two', name: 'Chiller' });
    render(<EntityPicker kind="deviceType" value="on-page-two" onChange={vi.fn()} />);
    await waitFor(() => expect(trigger().textContent).toContain('Chiller'));
    expect(getDeviceType).toHaveBeenCalledWith('on-page-two');
    open();
    expect(optionTokens()).toContain('on-page-two');
  });

  it('calls an off-page token unknown once the by-token lookup finds nothing', async () => {
    listDeviceTypes.mockResolvedValue({ results: [{ token: 'a', name: 'A' }], pagination: { totalRecords: 500 } });
    getDeviceType.mockResolvedValue(null);
    render(<EntityPicker kind="deviceType" value="gone" onChange={vi.fn()} />);
    await waitFor(() => expect(trigger().textContent).toContain('unknown type gone'));
  });

  it('does not call an off-page token unknown while its lookup is in flight or failed', async () => {
    listDeviceTypes.mockResolvedValue({ results: [{ token: 'a', name: 'A' }], pagination: { totalRecords: 500 } });
    let settle!: (v: unknown) => void;
    getDeviceType.mockReturnValue(new Promise((r) => (settle = r)));
    render(<EntityPicker kind="deviceType" value="on-page-two" onChange={vi.fn()} />);
    await waitFor(() => expect(getDeviceType).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect(trigger().textContent).toContain('on-page-two');
    expect(trigger().textContent).not.toContain('unknown');
    cleanup();

    getDeviceType.mockRejectedValue(new Error('boom'));
    render(<EntityPicker kind="deviceType" value="on-page-two" onChange={vi.fn()} />);
    await waitFor(() => expect(getDeviceType).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(trigger().textContent).toContain('on-page-two');
    expect(trigger().textContent).not.toContain('unknown');
    settle(null);
  });

  it('does not look up a token the page already holds, nor any token when the page was whole', async () => {
    render(<EntityPicker kind="deviceType" value="retired" onChange={vi.fn()} />);
    await waitFor(() => expect(trigger().textContent).toContain('unknown type retired'));
    expect(getDeviceType).not.toHaveBeenCalled();
  });

  it('does not call a token unknown when the read failed', async () => {
    listDeviceTypes.mockRejectedValue(new Error('boom'));
    render(<EntityPicker kind="deviceType" value="thermostat" onChange={vi.fn()} />);
    await waitFor(() => expect(listDeviceTypes).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect(trigger().textContent).toContain('thermostat');
    expect(trigger().textContent).not.toContain('unknown');
  });

  it('says the load failed, rather than that there are no device types', async () => {
    listDeviceTypes.mockRejectedValue(new Error('boom'));
    render(<EntityPicker kind="deviceType" value="" onChange={vi.fn()} />);
    await waitFor(() => expect(listDeviceTypes).toHaveBeenCalled());
    open();
    await screen.findByText('Failed to load device types.');
  });

  it('fits a 390 px column: the trigger fills it and truncates rather than widening it', async () => {
    window.innerWidth = 390;
    render(
      <div style={{ width: 358 }} className="min-w-0">
        <EntityPicker kind="deviceType" value="thermostat" onChange={vi.fn()} />
      </div>,
    );
    await waitFor(() => expect(trigger().textContent).toContain('Thermostat'));
    const t = trigger();
    // jsdom does no layout, so assert the classes that make the layout hold at that width.
    expect(t.className).toContain('w-full');
    expect(t.querySelector('span')!.className).toContain('truncate');
  });
});
