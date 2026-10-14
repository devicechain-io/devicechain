// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reading a CSV file an operator picked, safely. The counterpart of csv.ts (the export).
//
// 🔴 A picked file is untrusted input, and the rules here are about what happens BEFORE any
// of it reaches a screen or a request:
//
//   - the size is checked from `file.size` and the file is not read at all when it is over the
//     cap, so a 2 GB file never becomes a string;
//   - the bytes are decoded as UTF-8 in FATAL mode: a file in another encoding (Windows-1252
//     is what a spreadsheet saves by default in some locales) is refused with a message, never
//     decoded to replacement characters that would be stored as device names;
//   - nothing is evaluated. A cell that starts with `=`, `+`, `-` or `@` is just text here (a
//     formula only matters when a SPREADSHEET opens a file, which is what the export's guard
//     is for). It is kept verbatim apart from undoing that same guard, so a file this console
//     exported reads back to the values it was written from;
//   - the row count is capped while parsing, so a file of ten million empty lines stops at the
//     cap instead of building ten million arrays.

import { unguardCell } from '@/lib/csv';

/** Largest file the import will read. */
export const IMPORT_MAX_BYTES = 5 * 1024 * 1024;

/** Most DATA rows (the header excluded) one import will take. */
export const IMPORT_MAX_ROWS = 10_000;

export type CsvImportErrorCode =
  | 'too_large'
  | 'invalid_utf8'
  | 'too_many_rows'
  | 'unterminated_quote'
  | 'empty';

/** A refusal with a stable code the UI maps to a localized sentence. */
export class CsvImportError extends Error {
  readonly code: CsvImportErrorCode;
  constructor(code: CsvImportErrorCode) {
    super(code);
    this.name = 'CsvImportError';
    this.code = code;
  }
}

/**
 * Decodes bytes as UTF-8, refusing anything that is not valid UTF-8. A leading byte-order mark
 * (what Excel writes) is dropped.
 */
export function decodeUtf8(bytes: ArrayBuffer | Uint8Array): string {
  let text: string;
  try {
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: false }).decode(bytes);
  } catch {
    throw new CsvImportError('invalid_utf8');
  }
  return text.charCodeAt(0) === 0xfeff ? text.slice(1) : text;
}

const DELIMITERS = [',', ';', '\t'] as const;

// A spreadsheet in a locale that writes a decimal comma saves `;`-separated files, and a
// paste from a sheet is tab-separated. The delimiter is the candidate that splits the first
// record into the most fields, counted outside quotes; a tie goes to the comma.
function detectDelimiter(text: string): string {
  const counts: Record<string, number> = { ',': 0, ';': 0, '\t': 0 };
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') quoted = !quoted;
    else if (!quoted && (c === '\n' || c === '\r')) break;
    else if (!quoted && c in counts) counts[c]++;
  }
  let best: string = ',';
  for (const d of DELIMITERS) if (counts[d] > counts[best]) best = d;
  return best;
}

/**
 * Parses RFC 4180 CSV text: quoted fields with doubled quotes, embedded delimiters and
 * newlines, LF / CRLF / CR line ends, and a `;` or tab delimiter when the first line uses one.
 * Blank lines are skipped. Rows may be ragged. Throws on an unterminated quote and when the
 * table holds more than `maxRows` data rows (plus the header).
 */
export function parseCsv(input: string, maxRows: number = IMPORT_MAX_ROWS): string[][] {
  const text = input.charCodeAt(0) === 0xfeff ? input.slice(1) : input;
  const delimiter = detectDelimiter(text);
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let quoted = false;
  let fieldStarted = false;

  const endField = () => {
    row.push(field);
    field = '';
    fieldStarted = false;
  };
  const endRow = () => {
    endField();
    // A line holding nothing at all is blank, not a row of one empty cell.
    if (!(row.length === 1 && row[0] === '')) {
      rows.push(row);
      if (rows.length > maxRows + 1) throw new CsvImportError('too_many_rows');
    }
    row = [];
  };

  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          quoted = false;
        }
      } else {
        field += c;
      }
      continue;
    }
    if (c === '"' && !fieldStarted) {
      quoted = true;
      fieldStarted = true;
    } else if (c === delimiter) {
      endField();
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      endRow();
    } else {
      field += c;
      fieldStarted = true;
    }
  }
  if (quoted) throw new CsvImportError('unterminated_quote');
  if (fieldStarted || field !== '' || row.length > 0) endRow();
  return rows.map((r) => r.map(unguardCell));
}

/** Parses pasted text (already a string, so only the row cap and structure apply). */
export function parsePastedCsv(text: string): string[][] {
  if (new Blob([text]).size > IMPORT_MAX_BYTES) throw new CsvImportError('too_large');
  const rows = parseCsv(text);
  if (rows.length === 0) throw new CsvImportError('empty');
  return rows;
}

/** Reads and parses a picked file. A file over IMPORT_MAX_BYTES is refused before it is read. */
export async function readCsvFile(file: Pick<File, 'size' | 'arrayBuffer'>): Promise<string[][]> {
  if (file.size > IMPORT_MAX_BYTES) throw new CsvImportError('too_large');
  const text = decodeUtf8(await file.arrayBuffer());
  const rows = parseCsv(text);
  if (rows.length === 0) throw new CsvImportError('empty');
  return rows;
}
