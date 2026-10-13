// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ExportCsvButton } from './ExportCsvButton';

type Row = { name: string };
const columns = [{ header: 'Name', value: (r: Row) => r.name }];

let blobs: Blob[] = [];
beforeEach(async () => {
  blobs = [];
  URL.createObjectURL = vi.fn((b: Blob) => {
    blobs.push(b);
    return 'blob:x';
  });
  URL.revokeObjectURL = vi.fn();
  await i18n.changeLanguage('en');
});
afterEach(() => cleanup());

const page = (rows: Row[], total: number) => ({ results: rows, pagination: { totalRecords: total } });

describe('ExportCsvButton', () => {
  it('downloads the whole filtered list as BOM-prefixed, formula-guarded CSV', async () => {
    const fetchPage = vi.fn(async (p: number, size: number) => {
      expect(size).toBeGreaterThan(20);
      return p === 1 ? page([{ name: 'ä,1' }, { name: '=evil()' }], 2) : page([], 2);
    });
    render(<ExportCsvButton fileStem="things" columns={columns} fetchPage={fetchPage} />);
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }));
    await waitFor(() => expect(blobs).toHaveLength(1));
    const bytes = new Uint8Array(await blobs[0].arrayBuffer());
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    expect(new TextDecoder().decode(bytes.slice(3))).toBe('Name\r\n"ä,1"\r\n\'=evil()\r\n');
    expect(fetchPage).toHaveBeenCalledTimes(1);
  });

  it('says so when the row cap cut the export short', async () => {
    const big = Array.from({ length: 500 }, (_, i) => ({ name: `n${i}` }));
    const fetchPage = vi.fn(async () => page(big, 50_000));
    render(<ExportCsvButton fileStem="things" columns={columns} fetchPage={fetchPage} />);
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }));
    await waitFor(() => expect(blobs).toHaveLength(1));
    expect((await screen.findAllByText(/10000-row limit/)).length).toBeGreaterThan(0);
    expect(fetchPage).toHaveBeenCalledTimes(20);
  });

  it('can be cancelled mid-export and downloads nothing', async () => {
    let release: (v: ReturnType<typeof page>) => void = () => {};
    const fetchPage = vi.fn(
      () => new Promise<ReturnType<typeof page>>((resolve) => (release = resolve)),
    );
    render(<ExportCsvButton fileStem="things" columns={columns} fetchPage={fetchPage} />);
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }));
    fireEvent.click(await screen.findByRole('button', { name: /cancel export/i }));
    release(page([{ name: 'a' }], 9999));
    await screen.findByText(/export cancelled/i);
    expect(blobs).toHaveLength(0);
    expect(screen.getByRole('button', { name: /export csv/i })).toBeTruthy();
  });

  it('reports a failed page instead of downloading a partial file', async () => {
    const fetchPage = vi.fn(async () => {
      throw new Error('boom');
    });
    render(<ExportCsvButton fileStem="things" columns={columns} fetchPage={fetchPage} />);
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }));
    await screen.findByText(/export failed: boom/i);
    expect(blobs).toHaveLength(0);
  });
});
