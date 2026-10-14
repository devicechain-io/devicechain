// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The import wizard end to end with the transport faked: paste → mapped columns → dry run →
// import → summary. The form, the parser and the chunk runner all run for real.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { DeviceImportForm } from './DeviceImportForm';

type ImportVars = { rows: Array<Record<string, string>>; mode: string; dryRun: boolean };

const SECRET = 'factory-secret-0123456789';

function imports(): ImportVars[] {
  return gqlMock.mock.calls
    .filter((c) => c[0] === 'device-management' && (c[2] as ImportVars | undefined)?.rows)
    .map((c) => c[2] as ImportVars);
}

afterEach(cleanup);
beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockImplementation((service: string, _doc: unknown, vars?: ImportVars) => {
    if (vars?.rows) {
      return Promise.resolve({
        importDevices: {
          rows: vars.rows.map((r, i) => ({
            row: i + 1,
            token: r.token,
            outcome: r.token === 'bad token' ? 'INVALID' : 'CREATED',
            code: r.token === 'bad token' ? 'INVALID_VALUE' : null,
            message: r.token === 'bad token' ? 'token must be letters and digits' : null,
          })),
        },
      });
    }
    if (service === 'device-management') {
      return Promise.resolve({ deviceTypes: { results: [], pagination: { totalRecords: 0 } } });
    }
    return Promise.resolve({});
  });
});

async function pasteAndCheck(text: string, onDone = vi.fn()) {
  render(<DeviceImportForm onDone={onDone} />);
  fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: text } });
  fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Check the file' }));
  await screen.findByText('What an import would do');
  return onDone;
}

describe('the device import wizard', () => {
  it('maps the columns by header, previews with a dry run, then imports', async () => {
    const onDone = await pasteAndCheck('token,deviceTypeToken,name\r\nd1,gateway,One\r\nbad token,gateway,Two\r\n');

    // The dry run went first, and wrote nothing by asking for nothing to be written.
    expect(imports()).toHaveLength(1);
    expect(imports()[0]).toMatchObject({ mode: 'NONE', dryRun: true });
    expect(imports()[0].rows).toEqual([
      { token: 'd1', deviceTypeToken: 'gateway', name: 'One' },
      { token: 'bad token', deviceTypeToken: 'gateway', name: 'Two' },
    ]);
    expect(screen.getByText('token must be letters and digits')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Import 1 device' }));
    await screen.findByText('Import finished');
    expect(imports()).toHaveLength(2);
    expect(imports()[1].dryRun).toBe(false);
    expect(screen.getByText('1 created, 0 already existed, 1 need attention.')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onDone).toHaveBeenCalledWith('Imported 1 device');
  });

  it('filters the outcome table', async () => {
    await pasteAndCheck('token,deviceTypeToken\nd1,gateway\nbad token,gateway\n');
    // Before filtering, a "Created" badge is shown. The row badges are the only elements reading exactly "Created" / "Invalid".
    expect(screen.getByText('Created')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: /^Invalid · 1$/ }));
    await waitFor(() => expect(screen.queryByText('Created')).toBeNull());
    expect(screen.getByText('Invalid')).toBeTruthy();
  });

  it('refuses to check a file with no token column', async () => {
    render(<DeviceImportForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: 'serial,deviceTypeToken\nd1,gateway' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    await screen.findByText('Map the token column.');
    expect((screen.getByRole('button', { name: 'Check the file' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('shows a message for a file that is not valid text', async () => {
    render(<DeviceImportForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: 'a,b\n1,"never closed' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    await screen.findByText(/stray quotation mark/);
  });

  it('sends a factory secret verbatim, and never shows it', async () => {
    render(<DeviceImportForm onDone={vi.fn()} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Factory credentials' }));
    fireEvent.change(screen.getByLabelText('Or paste the rows'), {
      target: {
        value: `token,deviceTypeToken,credentialType,credentialId,credentialValue\nd1,gateway,MQTT_BASIC,u1,${SECRET}\n`,
      },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Check the file' }));
    await screen.findByText('What an import would do');
    fireEvent.click(screen.getByRole('button', { name: 'Import 1 device' }));
    await screen.findByText('Import finished');

    expect(imports().every((i) => i.mode === 'FACTORY_CREDENTIALS')).toBe(true);
    expect(imports()[1].rows[0].credentialValue).toBe(SECRET);
    expect(document.body.textContent).not.toContain(SECRET);
    expect(document.body.innerHTML).not.toContain(SECRET);
  });
});
