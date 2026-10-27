// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Sparkplug parts of the device command panel, under jsdom with the real catalogs: the
// write form (free-text metric and value, sent as the payload the adapter expects) and the
// outcome help under a sparkplug.* command's status (what a confirmed, unchanged, unconfirmed
// or in-flight command actually means, and the secondary-host warning). Only the GraphQL
// transport and the command picker's popover are faked.
import i18n from '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, toastMock } = vi.hoisted(() => ({ gqlMock: vi.fn(), toastMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: toastMock }) }));
vi.mock('./HeldWhileDisabledNotice', () => ({ HeldWhileDisabledNotice: () => null }));
// The real Combobox hides its options behind a popover; this stand-in lists them as buttons.
vi.mock('@/components/ui/combobox', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/ui/combobox')>()),
  Combobox: ({ options, onChange }: { options: { value: string; label?: string }[]; onChange: (v: string) => void }) => (
    <div>
      {options.map((o) => (
        <button key={o.value} type="button" onClick={() => onChange(o.value)}>
          {o.label ?? o.value}
        </button>
      ))}
    </div>
  ),
}));

import { DeviceCommandsPanel } from './DeviceCommandsPanel';

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  toastMock.mockReset();
});

const vocabulary = {
  constrained: true,
  commands: [
    { commandKey: 'sparkplug.write', name: 'Write metric', description: null, parameterSchema: null, impact: 'NORMAL' },
    { commandKey: 'sparkplug.rebirth', name: 'Rebirth', description: null, parameterSchema: null, impact: 'NORMAL' },
    { commandKey: 'reboot', name: 'Reboot', description: null, parameterSchema: null, impact: 'NORMAL' },
  ],
};

function row(name: string, status: string, responsePayload: string | null = null, error: string | null = null) {
  return {
    id: `id-${name}-${status}`,
    token: `tok-${name}-${status}`,
    deviceToken: 'valve-1',
    batchToken: null,
    name,
    payload: null,
    status,
    queuedTime: '2026-10-27T12:00:00Z',
    sentTime: null,
    respondedTime: null,
    expiresAt: null,
    responsePayload,
    error,
  };
}

// Answers the history, the vocabulary and the enqueue; returns the createCommand requests seen.
function install(history: ReturnType<typeof row>[] = []) {
  const created: { request: { name: string; payload?: string } }[] = [];
  gqlMock.mockImplementation((service: string, document: unknown, vars: { request: { name: string; payload?: string } }) => {
    if (String(document).includes('mutation CreateCommand')) {
      created.push(vars);
      return Promise.resolve({ createCommand: { command: { id: 'n', token: 'n', status: 'QUEUED' }, rejection: null } });
    }
    if (service === 'command-delivery') {
      return Promise.resolve({
        commands: { results: history, pagination: { pageStart: 0, pageEnd: history.length, totalRecords: history.length } },
      });
    }
    return Promise.resolve({ deviceCommandVocabulary: vocabulary });
  });
  return created;
}

async function pick(label: string) {
  fireEvent.click(await screen.findByRole('button', { name: label }));
}

describe('the Sparkplug write form', () => {
  it('replaces the generated form for sparkplug.write and sends the typed metric and value', async () => {
    const created = install();
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    await pick('Write metric (sparkplug.write)');

    fireEvent.change(await screen.findByLabelText('Metric'), { target: { value: 'Outputs/Valve1' } });
    fireEvent.change(screen.getByLabelText('Value'), { target: { value: 'true' } });
    fireEvent.click(screen.getByRole('button', { name: 'Issue command' }));

    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].request.name).toBe('sparkplug.write');
    // A boolean, not the string "true": the adapter refuses the wrong JSON class for the
    // datatype the node declared.
    expect(created[0].request.payload).toBe('{"metric":"Outputs/Valve1","value":true}');
  });

  it('sends the value as text when asked to, so a String metric can hold "true"', async () => {
    const created = install();
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    await pick('Write metric (sparkplug.write)');

    fireEvent.change(await screen.findByLabelText('Metric'), { target: { value: 'Mode' } });
    fireEvent.change(screen.getByLabelText('Value'), { target: { value: 'true' } });
    fireEvent.click(screen.getByRole('radio', { name: 'Text' }));
    fireEvent.click(screen.getByRole('button', { name: 'Issue command' }));

    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].request.payload).toBe('{"metric":"Mode","value":"true"}');
  });

  it('names a missing metric and sends nothing', async () => {
    const created = install();
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    await pick('Write metric (sparkplug.write)');

    fireEvent.change(await screen.findByLabelText('Value'), { target: { value: '5' } });
    fireEvent.click(screen.getByRole('button', { name: 'Issue command' }));

    expect(await screen.findByText('Enter the metric to write.')).toBeTruthy();
    expect(created).toHaveLength(0);
    expect(toastMock).toHaveBeenCalledWith(expect.any(String), 'error');
  });

  it('is not shown for any other command', async () => {
    install();
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    await pick('Rebirth (sparkplug.rebirth)');
    expect(screen.queryByTestId('sparkplug-write-form')).toBeNull();
    await pick('Write metric (sparkplug.write)');
    expect(await screen.findByTestId('sparkplug-write-form')).toBeTruthy();
    // Choosing another command drops what was half-typed instead of carrying it over.
    fireEvent.change(screen.getByLabelText('Metric'), { target: { value: 'Outputs/V' } });
    await pick('Rebirth (sparkplug.rebirth)');
    await pick('Write metric (sparkplug.write)');
    expect((await screen.findByLabelText('Metric') as HTMLInputElement).value).toBe('');
  });
});

describe('the Sparkplug outcome help', () => {
  const confirmed = JSON.stringify({
    kind: 'write', metric: 'Outputs/Valve1', value: true, reportedAt: '2026-10-27T12:00:05Z', via: 'DDATA', hostRole: 'primary',
  });

  it('explains a confirmed write as a report, not as proof the command acted', async () => {
    install([row('sparkplug.write', 'SUCCESSFUL', confirmed)]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    const note = await screen.findByTestId('sparkplug-outcome');
    expect(note.textContent).toContain('Confirmed by the node');
    expect(note.textContent).toContain('Outputs/Valve1 = true');
    expect(note.textContent).toContain('via DDATA');
    expect(note.textContent).toContain('not that this command caused it');
    expect(screen.queryByTestId('sparkplug-secondary-host')).toBeNull();
  });

  it('says an unchanged value is not an observed actuation', async () => {
    const unchanged = JSON.stringify({
      kind: 'write', metric: 'Outputs/Valve1', value: true, via: 'UNCHANGED', since: '2026-10-27T11:00:00Z', hostRole: 'primary',
    });
    install([row('sparkplug.write', 'SUCCESSFUL', unchanged)]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    const note = await screen.findByTestId('sparkplug-outcome');
    expect(note.textContent).toContain('Already at the value');
    expect(note.textContent).toContain('did not see the node act on this command');
    expect(note.textContent).not.toContain('Confirmed by');
  });

  it('says a timeout is not a failure and that Sparkplug has no acknowledgement', async () => {
    install([row('sparkplug.write', 'TIMEOUT', null, 'no report of Outputs/Valve1 at the requested value within 120s')]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    const note = await screen.findByTestId('sparkplug-outcome');
    expect(note.textContent).toContain('Not confirmed');
    expect(note.textContent).toContain('no acknowledgement');
  });

  it('says a SENT command is still waiting', async () => {
    install([row('sparkplug.write', 'SENT')]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    expect((await screen.findByTestId('sparkplug-outcome')).textContent).toContain('Waiting for the node');
  });

  it('warns when the confirming host was a secondary host', async () => {
    const secondary = JSON.stringify({ kind: 'write', metric: 'm', value: 1, via: 'DDATA', hostRole: 'secondary' });
    install([row('sparkplug.write', 'SUCCESSFUL', secondary)]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    const warning = await screen.findByTestId('sparkplug-secondary-host');
    expect(warning.textContent).toContain('Sent by a secondary host');
    expect(warning.textContent).toContain("only the nodes' primary host");
  });

  it('adds nothing under a command that is not a Sparkplug command', async () => {
    install([row('reboot', 'TIMEOUT'), row('lwm2m.reboot', 'SUCCESSFUL', confirmed)]);
    render(<DeviceCommandsPanel deviceToken="valve-1" />);
    await screen.findByText('lwm2m.reboot');
    expect(screen.queryByTestId('sparkplug-outcome')).toBeNull();
    expect(screen.queryByTestId('sparkplug-secondary-host')).toBeNull();
  });

  it('is translated', async () => {
    await i18n.changeLanguage('es');
    try {
      install([row('sparkplug.write', 'TIMEOUT')]);
      render(<DeviceCommandsPanel deviceToken="valve-1" />);
      expect((await screen.findByTestId('sparkplug-outcome')).textContent).toContain('Sin confirmar');
    } finally {
      await i18n.changeLanguage('en');
    }
  });
});
