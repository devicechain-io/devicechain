// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The facet-value import wizard end to end with the transport faked: paste, mapped columns,
// dry run, import, retry, summary. The form, the parser and the chunk runner run for real.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock, downloads } = vi.hoisted(() => ({
  gqlMock: vi.fn(),
  downloads: [] as string[],
}));

vi.mock('@/lib/csv', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/csv')>();
  return { ...actual, downloadCsv: (_name: string, csv: string) => downloads.push(csv) };
});

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, gql: (...args: unknown[]) => gqlMock(...args) };
});

import { FacetImportForm } from './FacetImportForm';

type Row = { memberType: string; token: string; key: string; value: string };
type Vars = { rows: Row[]; dryRun: boolean };

const calls = (): Vars[] =>
  gqlMock.mock.calls.filter((c) => (c[2] as Vars | undefined)?.rows).map((c) => c[2] as Vars);

let refusedOnce: Set<string>;

afterEach(cleanup);
beforeEach(() => {
  downloads.length = 0;
  refusedOnce = new Set();
  gqlMock.mockReset();
  gqlMock.mockImplementation((_service: string, _doc: unknown, vars?: Vars) => {
    if (!vars?.rows) return Promise.resolve({});
    return Promise.resolve({
      importFacetValues: vars.rows.map((r, i) => {
        const base = { row: i + 1, memberType: r.memberType, token: r.token, key: r.key };
        if (r.token === 'ghost') {
          return { ...base, outcome: 'INVALID', code: 'NOT_FOUND', message: 'entity not found' };
        }
        if (!vars.dryRun && refusedOnce.has(r.token)) {
          refusedOnce.delete(r.token);
          return { ...base, outcome: 'REFUSED', code: 'IMPORT_FAILED', message: 'facet value was not written; re-run this row' };
        }
        return { ...base, outcome: 'SET', code: null, message: null };
      }),
    });
  });
});

async function pasteAndCheck(text: string, onDone = vi.fn()) {
  render(<FacetImportForm onDone={onDone} />);
  fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: text } });
  fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Check the file' }));
  await screen.findByText('What an import would do');
  return onDone;
}

describe('the facet value import wizard', () => {
  it('maps columns by header, dry-runs first, then imports and reports', async () => {
    const onDone = await pasteAndCheck('memberType,token,key,value\ndevice,d1,site,north\ndevice,ghost,site,x\n');

    expect(calls()).toHaveLength(1);
    expect(calls()[0].dryRun).toBe(true);
    expect(calls()[0].rows).toEqual([
      { memberType: 'device', token: 'd1', key: 'site', value: 'north' },
      { memberType: 'device', token: 'ghost', key: 'site', value: 'x' },
    ]);
    expect(screen.getByText('entity not found')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Set 1 value' }));
    await screen.findByText('Import finished');
    expect(calls()).toHaveLength(2);
    expect(calls()[1].dryRun).toBe(false);
    expect(screen.getByText('1 set, 0 unchanged, 1 need attention.')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onDone).toHaveBeenCalledWith('Set 1 facet value');
  });

  it('holds back a repeated entity and key across the whole file and says so', async () => {
    await pasteAndCheck('memberType,token,key,value\ndevice,d1,site,a\ndevice,d1,site,b\n');
    expect(screen.getByText(/1 row repeats an earlier row/)).toBeTruthy();
    expect(calls()[0].rows).toHaveLength(1);
    expect(screen.getByText('This entity and facet key already appear earlier in the file.')).toBeTruthy();
  });

  it('uses the whole-file entity type and key when the columns are absent', async () => {
    render(<FacetImportForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: 'token,value\nd1,north\n' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    await screen.findByText('Map an entity type column or choose an entity type for the whole file.');
    fireEvent.change(screen.getByLabelText('Facet key for the whole file'), { target: { value: 'site' } });
    fireEvent.click(screen.getByLabelText('Entity type for the whole file'));
    fireEvent.click(await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-shot-value="device"]');
      expect(el).toBeTruthy();
      return el!;
    }));
    fireEvent.click(screen.getByRole('button', { name: 'Check the file' }));
    await screen.findByText('What an import would do');
    expect(calls()[0].rows).toEqual([{ memberType: 'device', token: 'd1', key: 'site', value: 'north' }]);
  });

  it('refuses to check a file with no value column', async () => {
    render(<FacetImportForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: 'memberType,token,key\ndevice,d1,site' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    await screen.findByText('Map the value column.');
    expect((screen.getByRole('button', { name: 'Check the file' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('retries only the refused rows and merges the answer', async () => {
    refusedOnce.add('d2');
    await pasteAndCheck('memberType,token,key,value\ndevice,d1,site,a\ndevice,d2,site,b\n');
    fireEvent.click(screen.getByRole('button', { name: 'Set 2 values' }));
    await screen.findByText('Import finished');
    expect(screen.getByText('1 set, 0 unchanged, 1 need attention.')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Retry 1 row' }));
    await waitFor(() => expect(screen.getByText('2 set, 0 unchanged, 0 need attention.')).toBeTruthy());
    const last = calls()[calls().length - 1];
    expect(last.dryRun).toBe(false);
    expect(last.rows.map((r) => r.token)).toEqual(['d2']);
    expect(screen.queryByRole('button', { name: /^Retry/ })).toBeNull();
  });

  it('shows a lost answer as unknown, then the server answer on retry', async () => {
    let lose = true;
    const inner = gqlMock.getMockImplementation()!;
    gqlMock.mockImplementation((service: string, doc: unknown, vars?: Vars) => {
      if (vars?.rows && !vars.dryRun && lose) {
        lose = false;
        return Promise.reject(new Error('connection reset'));
      }
      return inner(service, doc, vars);
    });
    await pasteAndCheck('memberType,token,key,value\ndevice,d1,site,a\n');
    fireEvent.click(screen.getByRole('button', { name: 'Set 1 value' }));
    await screen.findByText('Import finished');
    expect(screen.getAllByText('Outcome unknown').length).toBeGreaterThan(0);
    expect(screen.queryByText(/need attention/)?.textContent).toContain('0 need attention');
    expect(screen.getByText(/answer for this row was lost/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Retry 1 row' }));
    await waitFor(() => expect(screen.getByText('1 set, 0 unchanged, 0 need attention.')).toBeTruthy());
    expect(screen.queryByText('Outcome unknown')).toBeNull();
  });

  it('exposes the selected outcome filter', async () => {
    await pasteAndCheck('memberType,token,key,value\ndevice,d1,site,a\ndevice,ghost,site,b\n');
    const all = screen.getByRole('button', { name: /^All · 2$/ });
    expect(all.getAttribute('aria-pressed')).toBe('true');
    const invalid = screen.getByRole('button', { name: /^Invalid · 1$/ });
    expect(invalid.getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(invalid);
    expect(invalid.getAttribute('aria-pressed')).toBe('true');
    expect(all.getAttribute('aria-pressed')).toBe('false');
  });

  it('downloads failed rows without any secret-looking column', async () => {
    await pasteAndCheck('memberType,token,key,value,api_key\ndevice,d1,site,a,AKIA-TOPSECRET\ndevice,ghost,site,b,AKIA-TOPSECRET-2\n');
    fireEvent.click(screen.getByRole('button', { name: 'Set 1 value' }));
    await screen.findByText('Import finished');
    fireEvent.click(screen.getByRole('button', { name: 'Download failed rows' }));
    expect(downloads).toHaveLength(1);
    expect(downloads[0]).toContain('ghost');
    expect(downloads[0]).not.toContain('AKIA');
    expect(document.body.innerHTML).not.toContain('AKIA');
  });

  it('shows a message for a file that is not valid text', async () => {
    render(<FacetImportForm onDone={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('Or paste the rows'), { target: { value: 'a,b\n1,"never closed' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use pasted rows' }));
    await screen.findByText(/stray quotation mark/);
  });
});
