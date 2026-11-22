// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The integration form on its own (the list page's tests drive create and the reveal-once token
// through the page): its starting state, the generic-source fields and the problems it names,
// the exact create request, the partial update an edit sends, and the read-only view. Only the
// transport is faked; the form, the pickers and the request builders run for real.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => h.gql(...args) };
});

import { IntegrationForm } from './IntegrationForm';
import type { Integration } from '@/lib/api/integrations';

const DEVICE_TYPES = [
  { id: 'dt-1', token: 'meter', name: 'Meter', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null },
  { id: 'dt-2', token: 'pump', name: 'Pump', icon: null, backgroundColor: null, foregroundColor: null, borderColor: null },
];

const STORED = {
  id: 'i-1',
  token: 'eu868-gw',
  name: 'EU868 gateway fleet',
  description: null,
  type: 'CHIRPSTACK_V4',
  enabled: true,
  identity: 'DEV_EUI',
  generic: null,
  linkMetrics: false,
  tokenIssuedAt: '2026-10-01T00:00:00Z',
  undecodedCount: 0,
  lastUndecodedAt: null,
  lastUndecodedSample: null,
  lastAuthFailureAt: null,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  deviceTypes: [{ id: 'dt-1', token: 'meter', name: 'Meter' }],
} as unknown as Integration;

let failWith: Error | null = null;
let writes: { op: string; variables: Record<string, any> }[] = [];

beforeEach(() => {
  failWith = null;
  writes = [];
  h.gql.mockReset();
  h.gql.mockImplementation((service: string, doc: unknown, variables: Record<string, any>) => {
    if (service === 'user-management/settings') return Promise.resolve({ tokenMasks: '{}' });
    const op = /(?:query|mutation)\s+(\w+)/.exec(String(doc))?.[1] ?? '';
    switch (op) {
      case 'DeviceTypes':
        return Promise.resolve({
          deviceTypes: { results: DEVICE_TYPES, pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 } },
        });
      case 'IntegrationByToken':
        return Promise.resolve({ integration: null });
      case 'CreateIntegration':
      case 'UpdateIntegration':
        writes.push({ op, variables });
        if (failWith) return Promise.reject(failWith);
        return Promise.resolve({
          createIntegration: { issuedToken: 'issued-once', integration: STORED },
          updateIntegration: STORED,
        });
      default:
        return Promise.resolve({});
    }
  });
});
afterEach(cleanup);

const submitButton = (name: string) => screen.getByRole('button', { name }) as HTMLButtonElement;

async function chooseType(name: string) {
  fireEvent.click(document.getElementById('ig-type') as HTMLButtonElement);
  fireEvent.click(await screen.findByText(name));
}

async function chooseScope(...names: string[]) {
  fireEvent.click(document.getElementById('ig-scope') as HTMLButtonElement);
  for (const n of names) fireEvent.click(await screen.findByText(n));
  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
}

describe('creating an integration', () => {
  it('starts as an enabled ChirpStack integration with nothing to submit', () => {
    render(<IntegrationForm onCreated={vi.fn()} />);
    expect(document.getElementById('ig-type')?.textContent).toContain('ChirpStack v4');
    expect((screen.getByLabelText('Accepting data') as HTMLElement).getAttribute('aria-checked')).toBe('true');
    expect((screen.getByLabelText('Store link metrics') as HTMLElement).getAttribute('aria-checked')).toBe('false');
    expect(submitButton('Create integration').disabled).toBe(true);
    // The scope is required and the form says so before anything is sent.
    expect(screen.getByText('Choose at least one device type.')).toBeTruthy();
    // A network server's identity is fixed, a generic source's settings are not shown.
    expect(screen.queryByText('Where the data is')).toBeNull();
  });

  it('needs a token and a device type, and sends exactly the request the draft describes', async () => {
    const onCreated = vi.fn();
    render(<IntegrationForm onCreated={onCreated} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: ' eu868-gw ' } });
    expect(submitButton('Create integration').disabled).toBe(true);
    await chooseScope('Meter', 'Pump');
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: ' EU868 gateway fleet ' } });
    fireEvent.click(screen.getByLabelText('Store link metrics'));
    await waitFor(() => expect(submitButton('Create integration').disabled).toBe(false));
    fireEvent.click(submitButton('Create integration'));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(writes).toEqual([
      {
        op: 'CreateIntegration',
        variables: {
          request: {
            token: 'eu868-gw',
            name: 'EU868 gateway fleet',
            description: null,
            type: 'CHIRPSTACK_V4',
            enabled: true,
            deviceTypeTokens: ['meter', 'pump'],
            identity: 'DEV_EUI',
            linkMetrics: true,
          },
        },
      },
    ]);
    // The one-time token is handed straight to the caller, not kept by the form.
    expect(onCreated.mock.calls[0][0].issuedToken).toBe('issued-once');
    expect(document.body.textContent).not.toContain('issued-once');
  });

  it('offers a choice of identity for The Things Stack and sends the chosen one', async () => {
    render(<IntegrationForm onCreated={vi.fn()} />);
    await chooseType('The Things Stack v3');
    fireEvent.click(await screen.findByRole('radio', { name: 'Device id' }));
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'tts' } });
    await chooseScope('Meter');
    await waitFor(() => expect(submitButton('Create integration').disabled).toBe(false));
    fireEvent.click(submitButton('Create integration'));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0].variables.request).toMatchObject({ type: 'TTS_V3', identity: 'DEVICE_ID' });
  });

  it('shows the generic-source fields, names what is wrong with each, and sends the settings document', async () => {
    render(<IntegrationForm onCreated={vi.fn()} />);
    await chooseType('Generic JSON');
    expect(await screen.findByText('Where the data is')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'webhook' } });
    await chooseScope('Meter');
    // Nothing typed yet: the scheme name and the identifier location are required.
    expect(submitButton('Create integration').disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('Identifier scheme name'), { target: { value: 'Bad Name' } });
    expect(screen.getByText(/Use lower-case letters, digits, hyphen or underscore/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Identifier scheme name'), { target: { value: 'serial' } });

    fireEvent.change(screen.getByLabelText('Identifier location'), { target: { value: 'device/id' } });
    expect(screen.getByText(/not a valid location for that kind/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Identifier location'), { target: { value: '/device/id' } });

    fireEvent.change(screen.getByLabelText('Items array (optional)'), { target: { value: 'uplinks' } });
    expect(screen.getAllByText(/not a JSON pointer/).length).toBeGreaterThan(0);
    expect(submitButton('Create integration').disabled).toBe(true);
    fireEvent.change(screen.getByLabelText('Items array (optional)'), { target: { value: '/uplinks' } });

    // The frame encoding appears once a frame pointer is typed, and must be chosen.
    fireEvent.change(screen.getByLabelText('Raw frame (optional)'), { target: { value: '/frame' } });
    expect(await screen.findByText('Choose HEX or BASE64.')).toBeTruthy();
    expect(submitButton('Create integration').disabled).toBe(true);
    fireEvent.click(screen.getByRole('radio', { name: 'HEX' }));

    await waitFor(() => expect(submitButton('Create integration').disabled).toBe(false));
    fireEvent.click(submitButton('Create integration'));
    await waitFor(() => expect(writes).toHaveLength(1));
    const request = writes[0].variables.request;
    expect(request).toMatchObject({ type: 'GENERIC_JSON', identity: 'ALIAS:custom.serial' });
    expect(JSON.parse(request.generic)).toEqual({
      idFrom: 'pointer:/device/id',
      itemsPointer: '/uplinks',
      framePointer: '/frame',
      frameEncoding: 'HEX',
    });
  });

  it('a generic binary source identifies the sender by header or query, never by a body field', async () => {
    render(<IntegrationForm onCreated={vi.fn()} />);
    await chooseType('Generic binary');
    await screen.findByText('Where the data is');
    expect(screen.queryByRole('radio', { name: 'Body field' })).toBeNull();
    expect(screen.getByRole('radio', { name: 'Header' })).toBeTruthy();
    expect(screen.queryByLabelText('Items array (optional)')).toBeNull();
  });

  it('shows the server refusal and keeps the form filled in', async () => {
    failWith = new GraphQLRequestError('integrations limit reached', 200, [
      { message: 'integrations limit reached', extensions: { code: 'LIMIT_EXCEEDED' } },
    ]);
    const onCreated = vi.fn();
    render(<IntegrationForm onCreated={onCreated} />);
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'eu868-gw' } });
    await chooseScope('Meter');
    await waitFor(() => expect(submitButton('Create integration').disabled).toBe(false));
    fireEvent.click(submitButton('Create integration'));
    expect(await screen.findByText(/integrations limit reached/)).toBeTruthy();
    expect(onCreated).not.toHaveBeenCalled();
    expect((screen.getByLabelText('Token') as HTMLInputElement).value).toBe('eu868-gw');
    expect(submitButton('Create integration').disabled).toBe(false);
  });
});

describe('editing an integration', () => {
  it('is prefilled, fixes the type and token, and offers no save until something changes', async () => {
    render(<IntegrationForm entity={STORED} onSaved={vi.fn()} />);
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('EU868 gateway fleet');
    const token = screen.getByLabelText('Token') as HTMLInputElement;
    expect(token.value).toBe('eu868-gw');
    expect(token.disabled).toBe(true);
    expect(document.getElementById('ig-type')?.textContent).toContain('ChirpStack v4');
    expect((document.getElementById('ig-type') as HTMLButtonElement).disabled).toBe(true);
    expect(submitButton('Save changes').disabled).toBe(true);
  });

  it('sends a partial update that names only what changed', async () => {
    const onSaved = vi.fn();
    render(<IntegrationForm entity={STORED} onSaved={onSaved} />);
    fireEvent.change(screen.getByLabelText('Description'), { target: { value: ' Gateways in the EU868 plan ' } });
    fireEvent.click(screen.getByLabelText('Accepting data'));
    await waitFor(() => expect(submitButton('Save changes').disabled).toBe(false));
    fireEvent.click(submitButton('Save changes'));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith('Integration “eu868-gw” updated'));
    expect(writes).toEqual([
      {
        op: 'UpdateIntegration',
        variables: { token: 'eu868-gw', request: { description: 'Gateways in the EU868 plan', enabled: false } },
      },
    ]);
  });

  it('is shown read-only without a save button for a member who cannot write', () => {
    render(<IntegrationForm entity={STORED} readOnly />);
    expect(screen.queryByRole('button', { name: 'Save changes' })).toBeNull();
    expect((screen.getByLabelText('Name') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText('Description') as HTMLTextAreaElement).disabled).toBe(true);
    expect((document.getElementById('ig-scope') as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText('Accepting data') as HTMLButtonElement).disabled).toBe(true);
  });
});
