// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: the shortcut declares exactly the adapter's two named keys, only the ones the
// profile does not already declare, with the profile-scoped tokens; and a server refusal is shown
// rather than swallowed.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock, authorities } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  toastMock: vi.fn(),
  authorities: { current: ['device:read', 'device:write'] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: authorities.current } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { Lwm2mStandardCommands } from './Lwm2mStandardCommands';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authorities.current = ['device:read', 'device:write'];
});

function install(existingKeys: string[], createError?: string) {
  const created: { request: Record<string, unknown> }[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: { request: Record<string, unknown> }) => {
    if (String(doc).includes('mutation CreateCommandDefinition')) {
      if (createError) return Promise.reject(new Error(createError));
      created.push(vars);
      return Promise.resolve({ createCommandDefinition: { id: '1', token: String(vars.request.token) } });
    }
    return Promise.resolve({
      commandDefinitions: {
        results: existingKeys.map((k, i) => ({
          id: String(i), token: `t${i}`, name: null, description: null, commandKey: k, parameterSchema: null, metadata: null,
        })),
        pagination: { pageStart: 1, pageEnd: existingKeys.length, totalRecords: existingKeys.length },
      },
    });
  });
  return created;
}

describe('Lwm2mStandardCommands', () => {
  it('declares both named commands on a profile that has neither', async () => {
    const created = install([]);
    const onAdded = vi.fn();
    render(<Lwm2mStandardCommands profileToken="gateway" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: /Add LwM2M standard commands/ }));
    await waitFor(() => expect(onAdded).toHaveBeenCalled());

    expect(created.map((c) => c.request.commandKey)).toEqual(['lwm2m.reboot', 'lwm2m.readDeviceInfo']);
    expect(created.map((c) => c.request.token)).toEqual(['gateway-lwm2m-reboot', 'gateway-lwm2m-read-device-info']);
    expect(created.every((c) => c.request.deviceProfileToken === 'gateway')).toBe(true);
    expect(created.every((c) => c.request.parameterSchema === null)).toBe(true);
    // A reboot interrupts the device, so it waits for the maintenance window; reading device
    // information never does.
    expect(created.map((c) => c.request.impact)).toEqual(['DISRUPTIVE', 'NORMAL']);
  });

  it('stores the canonical names whatever language the console is in', async () => {
    await i18n.changeLanguage('es');
    try {
      const created = install([]);
      const onAdded = vi.fn();
      render(<Lwm2mStandardCommands profileToken="gateway" onAdded={onAdded} />);
      fireEvent.click(screen.getByRole('button', { name: /Añadir comandos estándar de LwM2M/ }));
      await waitFor(() => expect(onAdded).toHaveBeenCalled());
      expect(created.map((c) => c.request.name)).toEqual(['Reboot', 'Read device information']);
      expect(String(created[1].request.description)).toMatch(/^Reads manufacturer/);
    } finally {
      await i18n.changeLanguage('en');
    }
  });

  it('adds only what is missing and leaves a hand-declared command alone', async () => {
    const created = install(['lwm2m.reboot']);
    const onAdded = vi.fn();
    render(<Lwm2mStandardCommands profileToken="gateway" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: /Add LwM2M standard commands/ }));
    await waitFor(() => expect(onAdded).toHaveBeenCalled());
    expect(created.map((c) => c.request.commandKey)).toEqual(['lwm2m.readDeviceInfo']);
  });

  it('says so, and refreshes nothing, when both are already declared', async () => {
    const created = install(['lwm2m.reboot', 'lwm2m.readDeviceInfo']);
    const onAdded = vi.fn();
    render(<Lwm2mStandardCommands profileToken="gateway" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: /Add LwM2M standard commands/ }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith('Both LwM2M commands are already declared.'));
    expect(created).toHaveLength(0);
    expect(onAdded).not.toHaveBeenCalled();
  });

  it('shows a server refusal instead of swallowing it', async () => {
    install([], 'token "gateway-lwm2m-reboot" is already in use');
    const onAdded = vi.fn();
    render(<Lwm2mStandardCommands profileToken="gateway" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: /Add LwM2M standard commands/ }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('already in use'), 'error'));
    expect(onAdded).not.toHaveBeenCalled();
  });

  it('offers nothing to a user who cannot write devices', () => {
    authorities.current = ['device:read'];
    install([]);
    render(<Lwm2mStandardCommands profileToken="gateway" onAdded={vi.fn()} />);
    expect(screen.queryByRole('button', { name: /Add LwM2M standard commands/ })).toBeNull();
  });
});
