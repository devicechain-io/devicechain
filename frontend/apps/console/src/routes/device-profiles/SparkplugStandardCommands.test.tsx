// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Runs under jsdom with the real i18n catalogs; only the GraphQL transport is faked.
//
// What this file pins: the shortcut declares exactly the adapter's three command keys, only the
// ones the profile does not already declare, with the profile-scoped tokens and the right
// impacts (a reboot is DISRUPTIVE, because the adapter refuses one that is not); the stored text
// is the canonical English whatever the console's language; and a server refusal is shown.
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

import { SparkplugStandardCommands } from './SparkplugStandardCommands';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
  authorities.current = ['device:read', 'device:write'];
});

const BUTTON = /Add Sparkplug standard commands/;

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
          id: String(i),
          token: `t${i}`,
          name: null,
          description: null,
          commandKey: k,
          parameterSchema: null,
          metadata: null,
        })),
        pagination: { pageStart: 1, pageEnd: existingKeys.length, totalRecords: existingKeys.length },
      },
    });
  });
  return created;
}

describe('SparkplugStandardCommands', () => {
  it('declares all three commands on a profile that has none', async () => {
    const created = install([]);
    const onAdded = vi.fn();
    render(<SparkplugStandardCommands profileToken="plant" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: BUTTON }));
    await waitFor(() => expect(onAdded).toHaveBeenCalled());

    expect(created.map((c) => c.request.commandKey)).toEqual([
      'sparkplug.write',
      'sparkplug.rebirth',
      'sparkplug.reboot',
    ]);
    expect(created.map((c) => c.request.token)).toEqual([
      'plant-sparkplug-write',
      'plant-sparkplug-rebirth',
      'plant-sparkplug-reboot',
    ]);
    expect(created.every((c) => c.request.deviceProfileToken === 'plant')).toBe(true);
    // No schema: the write form sends the metric and value as typed, and the node's birth
    // decides the datatype.
    expect(created.every((c) => c.request.parameterSchema === null)).toBe(true);
    // The adapter refuses a reboot whose command is not DISRUPTIVE or DESTRUCTIVE.
    expect(created.map((c) => c.request.impact)).toEqual(['NORMAL', 'NORMAL', 'DISRUPTIVE']);
  });

  it('stores the canonical names whatever language the console is in', async () => {
    await i18n.changeLanguage('es');
    try {
      const created = install([]);
      const onAdded = vi.fn();
      render(<SparkplugStandardCommands profileToken="plant" onAdded={onAdded} />);
      fireEvent.click(screen.getByRole('button', { name: /Añadir comandos estándar de Sparkplug/ }));
      await waitFor(() => expect(onAdded).toHaveBeenCalled());
      expect(created.map((c) => c.request.name)).toEqual(['Write metric', 'Rebirth', 'Reboot']);
    } finally {
      await i18n.changeLanguage('en');
    }
  });

  it('adds only what is missing and leaves a hand-declared command alone', async () => {
    const created = install(['sparkplug.reboot', 'sparkplug.write']);
    const onAdded = vi.fn();
    render(<SparkplugStandardCommands profileToken="plant" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: BUTTON }));
    await waitFor(() => expect(onAdded).toHaveBeenCalled());
    expect(created.map((c) => c.request.commandKey)).toEqual(['sparkplug.rebirth']);
    expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('Added 1 Sparkplug command.'));
  });

  it('says so, and refreshes nothing, when all three are already declared', async () => {
    const created = install(['sparkplug.write', 'sparkplug.rebirth', 'sparkplug.reboot']);
    const onAdded = vi.fn();
    render(<SparkplugStandardCommands profileToken="plant" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: BUTTON }));
    await waitFor(() =>
      expect(toastMock).toHaveBeenCalledWith('All three Sparkplug commands are already declared.'),
    );
    expect(created).toHaveLength(0);
    expect(onAdded).not.toHaveBeenCalled();
  });

  it('shows a server refusal instead of swallowing it', async () => {
    install([], 'token "plant-sparkplug-write" is already in use');
    const onAdded = vi.fn();
    render(<SparkplugStandardCommands profileToken="plant" onAdded={onAdded} />);
    fireEvent.click(screen.getByRole('button', { name: BUTTON }));
    await waitFor(() => expect(toastMock).toHaveBeenCalledWith(expect.stringContaining('already in use'), 'error'));
    expect(onAdded).not.toHaveBeenCalled();
  });

  it('offers nothing to a user who cannot write devices', () => {
    authorities.current = ['device:read'];
    install([]);
    render(<SparkplugStandardCommands profileToken="plant" onAdded={vi.fn()} />);
    expect(screen.queryByRole('button', { name: BUTTON })).toBeNull();
  });
});
