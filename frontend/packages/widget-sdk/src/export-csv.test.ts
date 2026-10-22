// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { csvCell, downloadCsv, formulaLike, safeFileStem, toCsv } from './export-csv';

describe('formula injection', () => {
  it.each(['=1+1', '+SUM(A1)', '-2+3', '@SUM(A1)', '\tcmd', '\rcmd', '＝1+1', ' =1+1', '  @x'])('guards %j', (text) => {
    expect(formulaLike(text)).toBe(true);
    expect(csvCell(text)).toBe(text.includes('\r') ? `"'${text}"` : `'${text}`);
  });

  it('writes the apostrophe in front of the value, then quotes when RFC 4180 needs it', () => {
    expect(csvCell('=HYPERLINK("http://x","y")')).toBe(`"'=HYPERLINK(""http://x"",""y"")"`);
    expect(csvCell('-1,5')).toBe(`"'-1,5"`);
  });

  it('leaves a number a number, and a string that is only a number alone', () => {
    expect(csvCell(-12.5)).toBe('-12.5');
    expect(csvCell('-12.5')).toBe('-12.5');
    expect(csvCell('+3')).toBe('+3');
    expect(csvCell('1e-3')).toBe('1e-3');
    // ...but a number with anything after it is not a number.
    expect(csvCell('-1+cmd|x')).toBe(`'-1+cmd|x`);
  });

  it('does not touch an ordinary cell, including an ISO instant', () => {
    expect(csvCell('2026-10-22T14:05:00.000Z')).toBe('2026-10-22T14:05:00.000Z');
    expect(csvCell('temperature (°C)')).toBe('temperature (°C)');
  });

  it('guards a heading as it guards a cell', () => {
    expect(toCsv({ columns: ['=evil', 'ok'], rows: [] })).toBe(`'=evil,ok\r\n`);
  });
});

describe('RFC 4180', () => {
  it('quotes a comma, a quote and a line break, and doubles the quote', () => {
    expect(csvCell('a,b')).toBe('"a,b"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell('two\nlines')).toBe('"two\nlines"');
  });

  it('writes empty for a missing value and for a number that is not finite', () => {
    expect(csvCell(null)).toBe('');
    expect(csvCell(undefined)).toBe('');
    expect(csvCell(Number.NaN)).toBe('');
    expect(csvCell(Number.POSITIVE_INFINITY)).toBe('');
    expect(csvCell(0)).toBe('0');
    expect(csvCell(false)).toBe('false');
  });

  it('ends every record, the last included, with CRLF, and pads a short row', () => {
    expect(toCsv({ columns: ['a', 'b', 'c'], rows: [[1, 'x'], [2, 'y', 'z']] })).toBe('a,b,c\r\n1,x,\r\n2,y,z\r\n');
  });
});

describe('file names', () => {
  it('keeps letters, digits, dot, dash and underscore and nothing else', () => {
    expect(safeFileStem('Pump 3 / history: avg per 5m')).toBe('Pump-3-history-avg-per-5m');
    expect(safeFileStem('../../etc/passwd')).toBe('etc-passwd');
    expect(safeFileStem('a\\b:c*d?e"f<g>h|i')).toBe('a-b-c-d-e-f-g-h-i');
  });

  it('falls back when nothing is left, and bounds the length', () => {
    expect(safeFileStem('日本語')).toBe('widget');
    expect(safeFileStem('???', 'chart')).toBe('chart');
    expect(safeFileStem('x'.repeat(500)).length).toBeLessThanOrEqual(160);
  });

  it('turns an accented letter into its base letter rather than dropping it', () => {
    expect(safeFileStem('Température')).toBe('Temperature');
  });
});

describe('the download', () => {
  it('is UTF-8 with a byte-order mark, so Excel reads the degree sign', async () => {
    let blob: Blob | undefined;
    const create = URL.createObjectURL;
    const revoke = URL.revokeObjectURL;
    URL.createObjectURL = (b: Blob) => {
      blob = b;
      return 'blob:test';
    };
    URL.revokeObjectURL = () => undefined;
    try {
      downloadCsv({ name: 'Température 24h', columns: ['t (°C)'], rows: [[21.5]] });
    } finally {
      URL.createObjectURL = create;
      URL.revokeObjectURL = revoke;
    }
    const bytes = new Uint8Array(await blob!.arrayBuffer());
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    expect(new TextDecoder('utf-8', { ignoreBOM: true }).decode(bytes).slice(1)).toBe('t (°C)\r\n21.5\r\n');
    expect(blob!.type).toBe('text/csv;charset=utf-8');
  });
});
