// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';
import { collectPages, csvCell, exportFilename, isoTime, toCsv } from './csv';

describe('csvCell', () => {
  it('leaves plain text bare and empties null/undefined', () => {
    expect(csvCell('abc')).toBe('abc');
    expect(csvCell(null)).toBe('');
    expect(csvCell(undefined)).toBe('');
    expect(csvCell(0)).toBe('0');
    expect(csvCell(false)).toBe('false');
  });

  it('quotes commas, quotes and line breaks, doubling embedded quotes (RFC 4180)', () => {
    expect(csvCell('a,b')).toBe('"a,b"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell('l1\nl2')).toBe('"l1\nl2"');
    expect(csvCell('l1\r\nl2')).toBe('"l1\r\nl2"');
  });

  it.each(['=1+1', '+SUM(A1)', '-2+3', '@cmd', '\tx', '\rx'])(
    'defuses a formula-leading cell %j with a single quote',
    (v) => {
      const out = csvCell(v);
      expect(out.replace(/^"/, '').startsWith("'")).toBe(true);
    },
  );

  it('guards a formula that also needs quoting', () => {
    expect(csvCell('=HYPERLINK("http://x","y")')).toBe(`"'=HYPERLINK(""http://x"",""y"")"`);
  });

  it('keeps plain numbers numeric, including negatives', () => {
    expect(csvCell(-12.5)).toBe('-12.5');
    expect(csvCell('-12.5')).toBe('-12.5');
    expect(csvCell('+3')).toBe('+3');
    expect(csvCell('1e-3')).toBe('1e-3');
    expect(csvCell('-1+2')).toBe("'-1+2");
  });
});

describe('toCsv', () => {
  it('writes the header then rows in column order with CRLF endings', () => {
    const csv = toCsv(
      [
        { header: 'Name', value: (r: { n: string; v: number }) => r.n },
        { header: 'Value', value: (r) => r.v },
      ],
      [
        { n: 'a', v: 1 },
        { n: 'b,c', v: 2 },
      ],
    );
    expect(csv).toBe('Name,Value\r\na,1\r\n"b,c",2\r\n');
  });

  it('guards headers too', () => {
    expect(toCsv([{ header: '=x', value: () => '' }], [])).toBe("'=x\r\n");
  });
});

describe('isoTime', () => {
  it('normalises to ISO-8601 UTC and blanks the unparseable', () => {
    expect(isoTime('2026-10-01T12:00:00+02:00')).toBe('2026-10-01T10:00:00.000Z');
    expect(isoTime(null)).toBe('');
    expect(isoTime('not a date')).toBe('');
  });
});

describe('exportFilename', () => {
  it('appends the UTC date and sanitises', () => {
    expect(exportFilename('Audit log!', new Date('2026-10-13T23:59:00Z'))).toBe('Audit-log--2026-10-13.csv');
  });
});

const page = (from: number, n: number, total: number | null) => ({
  results: Array.from({ length: n }, (_, i) => from + i),
  pagination: { totalRecords: total },
});

describe('collectPages', () => {
  it('collects every page of the current filter until exhausted', async () => {
    const fetchPage = vi.fn(async (p: number, size: number) => page((p - 1) * size, p === 3 ? 5 : size, 25));
    const progress = vi.fn();
    const r = await collectPages(fetchPage, { pageSize: 10, onProgress: progress });
    expect(r.rows).toHaveLength(25);
    expect(r.truncated).toBe(false);
    expect(fetchPage.mock.calls.map((c) => c[0])).toEqual([1, 2, 3]);
    expect(progress.mock.calls.map((c) => c[0])).toEqual([10, 20, 25]);
  });

  it('stops exactly at the cap and reports truncation', async () => {
    const fetchPage = vi.fn(async (p: number, size: number) => page((p - 1) * size, size, 1000));
    const r = await collectPages(fetchPage, { pageSize: 10, cap: 25 });
    expect(r.rows).toHaveLength(25);
    expect(r.truncated).toBe(true);
    expect(fetchPage).toHaveBeenCalledTimes(3);
  });

  it('is not truncated when the result is exactly the cap', async () => {
    const fetchPage = async (p: number, size: number) => page((p - 1) * size, size, 20);
    const r = await collectPages(fetchPage, { pageSize: 10, cap: 20 });
    expect(r.rows).toHaveLength(20);
    expect(r.truncated).toBe(false);
  });

  it('handles an empty result', async () => {
    const r = await collectPages(async () => page(0, 0, 0), { pageSize: 10 });
    expect(r).toEqual({ rows: [], truncated: false });
  });

  it('rejects with AbortError and fetches no further page once cancelled', async () => {
    const ctl = new AbortController();
    const fetchPage = vi.fn(async (p: number, size: number) => {
      if (p === 2) ctl.abort();
      return page((p - 1) * size, size, 1000);
    });
    await expect(collectPages(fetchPage, { pageSize: 10, signal: ctl.signal })).rejects.toMatchObject({
      name: 'AbortError',
    });
    expect(fetchPage).toHaveBeenCalledTimes(2);
  });
});
