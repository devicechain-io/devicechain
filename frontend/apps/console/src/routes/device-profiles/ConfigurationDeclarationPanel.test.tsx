// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: the editor sends the WHOLE new list (the server replaces the
// declaration as a unit), a client-side grammar/duplicate mistake never reaches the
// server, and a refusal from the server is shown to the user with the drawer left open
// instead of being swallowed.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['device:read', 'device:write'] } }),
}));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));

import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ConfigurationDeclarationPanel } from './ConfigurationDeclarationPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

const MODE = { key: 'mode', valueType: 'STRING', description: 'Run mode' };

// Routes the read to `stored` and records every mutation's variables.
function install(
  initial: unknown[],
  setImpl?: (vars: { keys: unknown[] }, stored: unknown[]) => Promise<unknown>,
) {
  const stored = [...initial];
  const sets: { token: string; keys: unknown[] }[] = [];
  gqlMock.mockImplementation((_svc: string, doc: string, vars: { token: string; keys: unknown[] }) => {
    if (String(doc).includes('mutation SetDeviceProfileConfigurationDeclaration')) {
      sets.push(vars);
      return setImpl
        ? setImpl(vars, stored)
        : Promise.resolve({
            setDeviceProfileConfigurationDeclaration: { token: vars.token, configurationDeclaration: vars.keys },
          });
    }
    return Promise.resolve({
      deviceProfilesByToken: [{ token: 'thermostat', configurationDeclaration: [...stored] }],
    });
  });
  return sets;
}

// A mutation that stores what it was sent, so a re-read returns the saved list.
function stateful(vars: { keys: unknown[] }, stored: unknown[]) {
  stored.splice(0, stored.length, ...vars.keys);
  return Promise.resolve({
    setDeviceProfileConfigurationDeclaration: { token: 'thermostat', configurationDeclaration: vars.keys },
  });
}

function renderPanel() {
  return render(
    <ConfirmProvider>
      <ConfigurationDeclarationPanel profileToken="thermostat" />
    </ConfirmProvider>,
  );
}

describe('ConfigurationDeclarationPanel', () => {
  it('lists the declared keys and says nothing is declared when empty', async () => {
    install([MODE]);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());
    expect(screen.getByText('Run mode')).toBeTruthy();
    cleanup();

    install([]);
    renderPanel();
    await waitFor(() => expect(screen.getByText(/No configuration keys declared/)).toBeTruthy());
  });

  it('adds a key by sending the whole new list', async () => {
    const sets = install([MODE]);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: /Declare key/ }));
    fireEvent.change(await screen.findByLabelText('Attribute key'), { target: { value: 'sampleIntervalSeconds' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Declare key' }).slice(-1)[0]);

    await waitFor(() => expect(sets).toHaveLength(1));
    expect(sets[0]).toEqual({
      token: 'thermostat',
      keys: [
        { key: 'mode', valueType: 'STRING', description: 'Run mode' },
        { key: 'sampleIntervalSeconds', valueType: 'STRING', description: null },
      ],
    });
  });

  it('refuses a malformed or duplicate key on the client without calling the server', async () => {
    const sets = install([MODE]);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: /Declare key/ }));
    const input = await screen.findByLabelText('Attribute key');
    const submit = () => fireEvent.click(screen.getAllByRole('button', { name: 'Declare key' }).slice(-1)[0]);

    fireEvent.change(input, { target: { value: 'has space' } });
    submit();
    expect(await screen.findByText(/letters, digits, hyphens and underscores only/)).toBeTruthy();

    fireEvent.change(input, { target: { value: 'mode' } });
    submit();
    expect(await screen.findByText(/already declared/)).toBeTruthy();
    expect(sets).toHaveLength(0);
  });

  it('edits a key in place, keeping its position, and re-reads the stored list', async () => {
    const RATE = { key: 'rate', valueType: 'LONG', description: null };
    const sets = install([MODE, RATE], stateful);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getAllByRole('button', { name: /Edit/ })[0]);
    fireEvent.change(await screen.findByLabelText('Attribute key'), { target: { value: 'runMode' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(sets).toHaveLength(1));
    expect(sets[0].keys).toEqual([
      { key: 'runMode', valueType: 'STRING', description: 'Run mode' },
      RATE,
    ]);
    // The table shows what the server now stores, not the list it was loaded with.
    expect(await screen.findByText('runMode')).toBeTruthy();
    expect(screen.queryByText('mode')).toBeNull();
  });

  it('removes a key by sending the list without it', async () => {
    const RATE = { key: 'rate', valueType: 'LONG', description: null };
    const sets = install([MODE, RATE], stateful);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getAllByRole('button', { name: 'Remove key' })[0]);
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(sets).toHaveLength(1));
    expect(sets[0].keys).toEqual([RATE]);
    await waitFor(() => expect(screen.queryByText('mode')).toBeNull());
  });

  it('refuses a key the server grammar refuses: leading underscore or over 128 characters', async () => {
    const sets = install([MODE]);
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: /Declare key/ }));
    const input = await screen.findByLabelText('Attribute key');
    const submit = () => fireEvent.click(screen.getAllByRole('button', { name: 'Declare key' }).slice(-1)[0]);

    fireEvent.change(input, { target: { value: '_lead' } });
    submit();
    expect(await screen.findByText(/letters, digits, hyphens and underscores only/)).toBeTruthy();

    fireEvent.change(input, { target: { value: 'k'.repeat(129) } });
    submit();
    expect(await screen.findByText(/at most 128 characters/)).toBeTruthy();
    expect(sets).toHaveLength(0);
  });

  it('offers no write while the list is being re-read after a save', async () => {
    // The re-read after the save never answers, so the panel still holds the pre-save
    // list. A write built from it would send that list and undo the save.
    let reads = 0;
    const sets: { keys: unknown[] }[] = [];
    gqlMock.mockImplementation((_svc: string, doc: string, vars: { token: string; keys: unknown[] }) => {
      if (String(doc).includes('mutation SetDeviceProfileConfigurationDeclaration')) {
        sets.push(vars);
        return Promise.resolve({
          setDeviceProfileConfigurationDeclaration: { token: vars.token, configurationDeclaration: vars.keys },
        });
      }
      reads += 1;
      return reads === 1
        ? Promise.resolve({ deviceProfilesByToken: [{ token: 'thermostat', configurationDeclaration: [MODE] }] })
        : new Promise(() => {});
    });
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(sets).toHaveLength(1));
    await waitFor(() => expect(reads).toBe(2));

    expect((screen.getByRole('button', { name: /Declare key/ }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Remove key' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: /Edit/ }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('shows the server refusal and keeps the drawer open', async () => {
    const sets = install([MODE], () => Promise.reject(new Error('profile is locked by a publish')));
    renderPanel();
    await waitFor(() => expect(screen.getByText('mode')).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: /Declare key/ }));
    fireEvent.change(await screen.findByLabelText('Attribute key'), { target: { value: 'rate' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Declare key' }).slice(-1)[0]);

    expect(await screen.findByText('profile is locked by a publish')).toBeTruthy();
    expect(sets).toHaveLength(1);
    // The form is still there with what the user typed.
    expect((screen.getByLabelText('Attribute key') as HTMLInputElement).value).toBe('rate');
  });
});
