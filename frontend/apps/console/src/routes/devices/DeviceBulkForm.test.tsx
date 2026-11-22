// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bulk-create form: a device type, a count and three templates expand into a fleet in one
// createDevices call. The form, the type picker and the preview run for real; only the transport
// is faked, so the variables asserted on are exactly what would go over the wire.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceBulkForm } from './DeviceBulkForm';

const DEVICE_TYPE = {
  id: 'dt-1',
  token: 'meter',
  name: 'Meter',
  icon: null,
  backgroundColor: null,
  foregroundColor: null,
  borderColor: null,
};

let createError: Error | null = null;

function bulkRequests(): Record<string, unknown>[] {
  return gqlMock.mock.calls
    .map((c) => (c[2] as { request?: Record<string, unknown> } | undefined)?.request)
    .filter((r): r is Record<string, unknown> => !!r && 'count' in r);
}

afterEach(cleanup);
beforeEach(() => {
  createError = null;
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, _doc: unknown, vars?: { request?: { count?: number } }) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    if (vars?.request?.count !== undefined) {
      if (createError) return Promise.reject(createError);
      return Promise.resolve({
        createDevices: Array.from({ length: vars.request.count }, (_, i) => ({ id: `d${i}`, token: `t${i}`, name: 'x' })),
      });
    }
    return Promise.resolve({
      deviceTypes: { results: [DEVICE_TYPE], pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 } },
      deviceTypesByToken: [DEVICE_TYPE],
    });
  });
});

const button = () => screen.getByRole('button', { name: /^Create \d+ devices?$/ }) as HTMLButtonElement;
const field = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
const type = (label: string, value: string) => fireEvent.change(field(label), { target: { value } });

async function chooseType() {
  fireEvent.click(screen.getByText('Select a device type…'));
  fireEvent.click(await screen.findByText('Meter'));
}

describe('DeviceBulkForm', () => {
  it('starts on the defaults with nothing to submit until a device type is chosen', () => {
    render(<DeviceBulkForm onDone={vi.fn()} />);
    expect(field('Count').value).toBe('10');
    expect(field('Start index').value).toBe('1');
    expect(field('Token template').value).toBe('device-{n:04d}');
    expect(field('Name template').value).toBe('Device {n}');
    expect(field('External ID template').value).toBe('');
    expect(button().textContent).toBe('Create 10 devices');
    expect(button().disabled).toBe(true);
    // The reason is stated beside the button, not left to be guessed.
    expect(screen.getByText('Choose a device type.')).toBeTruthy();
  });

  it('previews the first three, an ellipsis and the last device the templates will produce', () => {
    render(<DeviceBulkForm onDone={vi.fn()} />);
    expect(screen.getByText('device-0001')).toBeTruthy();
    expect(screen.getByText('device-0003')).toBeTruthy();
    expect(screen.getByText('device-0010')).toBeTruthy();
    expect(screen.getByText('…')).toBeTruthy();
    expect(screen.getByText('Device 10')).toBeTruthy();
    // A small batch shows every row and no ellipsis.
    type('Count', '3');
    expect(screen.queryByText('…')).toBeNull();
    expect(screen.getByText('device-0003')).toBeTruthy();
    // {random} is a marker in the preview: the server fills it in.
    type('External ID template', 'ext-{random}');
    expect(screen.getAllByText('ext-‹random›').length).toBeGreaterThan(0);
  });

  it('refuses bad input with the reason and keeps the button off', async () => {
    render(<DeviceBulkForm onDone={vi.fn()} />);
    await chooseType();
    await waitFor(() => expect(button().disabled).toBe(false));

    type('Count', '0');
    expect(screen.getByText('Count must be at least 1.')).toBeTruthy();
    expect(button().disabled).toBe(true);

    type('Count', '1001');
    expect(screen.getByText('Count cannot exceed 1000 per bulk create.')).toBeTruthy();
    type('Count', '5');

    type('Start index', '0');
    expect(screen.getByText('Start index must be a whole number ≥ 1.')).toBeTruthy();
    type('Start index', '1');

    type('Token template', 'device');
    expect(screen.getByText(/must contain an \{n\} placeholder/)).toBeTruthy();
    type('Token template', 'dev-{random}');
    expect(screen.getByText(/must not use \{random\}/)).toBeTruthy();
    type('Token template', '-{n}');
    expect(screen.getByText('Rendered token “-1” is not valid: use letters, digits, hyphens and underscores.')).toBeTruthy();
    type('Token template', 'device-{n}');

    type('External ID template', 'fixed');
    expect(screen.getByText(/External ID template must include/)).toBeTruthy();
    expect(button().disabled).toBe(true);
    // A fixed external id is fine for a batch of one.
    type('Count', '1');
    expect(button().disabled).toBe(false);
    expect(bulkRequests()).toHaveLength(0);
  });

  it('sends exactly the chosen type, count, start index and trimmed templates, then reports the count', async () => {
    const onDone = vi.fn();
    render(<DeviceBulkForm onDone={onDone} />);
    await chooseType();
    type('Count', '4');
    type('Start index', '7');
    type('Token template', ' meter-{n:03d} ');
    type('External ID template', 'ERP-{random}');
    await waitFor(() => expect(button().disabled).toBe(false));
    fireEvent.click(button());

    await waitFor(() => expect(onDone).toHaveBeenCalledWith('Created 4 devices'));
    expect(bulkRequests()).toEqual([
      {
        deviceTypeToken: 'meter',
        count: 4,
        startIndex: 7,
        tokenTemplate: 'meter-{n:03d}',
        nameTemplate: 'Device {n}',
        externalIdTemplate: 'ERP-{random}',
      },
    ]);
  });

  it('omits a blank name or external id template rather than sending an empty string', async () => {
    render(<DeviceBulkForm onDone={vi.fn()} />);
    await chooseType();
    type('Name template', '   ');
    await waitFor(() => expect(button().disabled).toBe(false));
    fireEvent.click(button());
    await waitFor(() => expect(bulkRequests()).toHaveLength(1));
    const req = bulkRequests()[0];
    expect(req.nameTemplate).toBeUndefined();
    expect(req.externalIdTemplate).toBeUndefined();
  });

  it('shows the server refusal as it said it and keeps the input', async () => {
    createError = new GraphQLRequestError('token "meter-0003" already exists', 200, [
      { message: 'token "meter-0003" already exists', extensions: { code: 'CONFLICT' } },
    ]);
    const onDone = vi.fn();
    render(<DeviceBulkForm onDone={onDone} />);
    await chooseType();
    type('Count', '3');
    await waitFor(() => expect(button().disabled).toBe(false));
    fireEvent.click(button());
    expect(await screen.findByText(/already exists/)).toBeTruthy();
    expect(onDone).not.toHaveBeenCalled();
    expect(field('Count').value).toBe('3');
    expect(button().disabled).toBe(false);
  });
});
