// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { toCsv } from '@/lib/csv';
import {
  CsvImportError,
  IMPORT_MAX_BYTES,
  IMPORT_MAX_ROWS,
  decodeUtf8,
  parseCsv,
  parsePastedCsv,
  readCsvFile,
} from '@/lib/csvImport';

const bytes = (s: string) => new TextEncoder().encode(s);
const asBuffer = (u: Uint8Array) => u.buffer.slice(u.byteOffset, u.byteOffset + u.byteLength) as ArrayBuffer;

describe('parseCsv', () => {
  it('reads plain rows', () => {
    expect(parseCsv('a,b\n1,2\n')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ]);
  });

  it('accepts CRLF, LF and CR line ends and a missing final newline', () => {
    expect(parseCsv('a,b\r\n1,2\r\n3,4')).toEqual([
      ['a', 'b'],
      ['1', '2'],
      ['3', '4'],
    ]);
    expect(parseCsv('a,b\r1,2')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ]);
  });

  it('handles quoted fields with delimiters, doubled quotes and newlines', () => {
    expect(parseCsv('t,n\nd1,"Pump, ""A"""\nd2,"line1\r\nline2"\n')).toEqual([
      ['t', 'n'],
      ['d1', 'Pump, "A"'],
      ['d2', 'line1\r\nline2'],
    ]);
  });

  it('keeps empty cells and a trailing delimiter', () => {
    expect(parseCsv('a,b,c\n1,,\n,,3,\n')).toEqual([
      ['a', 'b', 'c'],
      ['1', '', ''],
      ['', '', '3', ''],
    ]);
  });

  it('skips blank lines', () => {
    expect(parseCsv('a\n\n\nb\n')).toEqual([['a'], ['b']]);
  });

  it('strips a byte-order mark', () => {
    expect(parseCsv('﻿token,name\nd1,x')[0]).toEqual(['token', 'name']);
  });

  it('detects a semicolon or tab delimiter from the first line', () => {
    expect(parseCsv('token;name\nd1;Pump, one\n')).toEqual([
      ['token', 'name'],
      ['d1', 'Pump, one'],
    ]);
    expect(parseCsv('token\tname\nd1\tx')).toEqual([
      ['token', 'name'],
      ['d1', 'x'],
    ]);
  });

  it('refuses an unterminated quote', () => {
    expect(() => parseCsv('a,b\n1,"oops\n2,3')).toThrowError(CsvImportError);
    expect(() => parseCsv('"x')).toThrowError(expect.objectContaining({ code: 'unterminated_quote' }) as Error);
  });

  it('never evaluates or alters a formula-looking cell', () => {
    const [, row] = parseCsv('name\n"=HYPERLINK(""http://x"")"\n');
    expect(row).toEqual(['=HYPERLINK("http://x")']);
    expect(parseCsv('name\n-5\n@sum\n+1')).toEqual([['name'], ['-5'], ['@sum'], ['+1']]);
  });

  it('returns cells exactly as written, guard apostrophes included', () => {
    const csv = toCsv([{ header: 'name', value: (v: string) => v }], ['=cmd|calc', "'tis"]);
    expect(parseCsv(csv).slice(1).map((r) => r[0])).toEqual(["'=cmd|calc", "'tis"]);
  });

  it('caps the data rows while parsing', () => {
    const body = Array.from({ length: IMPORT_MAX_ROWS + 1 }, (_, i) => `d${i}`).join('\n');
    expect(() => parseCsv(`token\n${body}`)).toThrowError(CsvImportError);
    const ok = Array.from({ length: IMPORT_MAX_ROWS }, (_, i) => `d${i}`).join('\n');
    expect(parseCsv(`token\n${ok}`)).toHaveLength(IMPORT_MAX_ROWS + 1);
  });
});

describe('decodeUtf8', () => {
  it('decodes valid UTF-8 and drops a BOM', () => {
    expect(decodeUtf8(bytes('﻿café,日本'))).toBe('café,日本');
  });

  it('refuses bytes that are not UTF-8 instead of substituting characters', () => {
    // 0xE9 alone is Latin-1 "é", an invalid UTF-8 sequence.
    expect(() => decodeUtf8(new Uint8Array([0x63, 0x61, 0x66, 0xe9]))).toThrowError(
      expect.objectContaining({ code: 'invalid_utf8' }) as Error,
    );
    expect(() => decodeUtf8(new Uint8Array([0xff, 0xfe, 0x00]))).toThrowError(CsvImportError);
  });
});

describe('readCsvFile', () => {
  it('refuses an over-size file WITHOUT reading it', async () => {
    let read = false;
    const file = {
      size: IMPORT_MAX_BYTES + 1,
      arrayBuffer: () => {
        read = true;
        return Promise.resolve(new ArrayBuffer(0));
      },
    };
    await expect(readCsvFile(file)).rejects.toMatchObject({ code: 'too_large' });
    expect(read).toBe(false);
  });

  it('accepts a file of exactly the cap and parses it', async () => {
    const buf = asBuffer(bytes('token\nd1\n'));
    const file = { size: IMPORT_MAX_BYTES, arrayBuffer: () => Promise.resolve(buf) };
    await expect(readCsvFile(file)).resolves.toEqual([['token'], ['d1']]);
  });

  it('rejects an empty file and a non-UTF-8 file', async () => {
    const empty = { size: 0, arrayBuffer: () => Promise.resolve(new ArrayBuffer(0)) };
    await expect(readCsvFile(empty)).rejects.toMatchObject({ code: 'empty' });
    const latin1 = {
      size: 4,
      arrayBuffer: () => Promise.resolve(asBuffer(new Uint8Array([0x63, 0xe9, 0x2c, 0x61]))),
    };
    await expect(readCsvFile(latin1)).rejects.toMatchObject({ code: 'invalid_utf8' });
  });
});

describe('parsePastedCsv', () => {
  it('parses pasted text and refuses oversize or empty input', () => {
    expect(parsePastedCsv('a\tb\n1\t2')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ]);
    expect(() => parsePastedCsv('')).toThrowError(CsvImportError);
    expect(() => parsePastedCsv('x'.repeat(IMPORT_MAX_BYTES + 1))).toThrowError(CsvImportError);
  });
});
