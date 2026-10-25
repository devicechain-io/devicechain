// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the device import: mapping a parsed file's columns onto the fields of an
// import row, building the rows, sending them in chunks and folding the outcomes back onto
// the file's own row numbers. No React, no network of its own (the sender is passed in), so
// every rule here is unit-tested.
//
// 🔴 THE SERVER DECIDES WHAT IS VALID. Nothing here pre-validates a token or a type: a row is
// sent as the file wrote it, so the preview (a dry run) and the import agree by construction.
// The only thing the browser does with a value is trim the cells whose meaning is not
// whitespace-sensitive. A credential secret is the exception on purpose: it is sent byte for
// byte, because the device will present exactly those bytes.

import { csvCell, toCsv, unguardCell, type CsvColumn } from '@/lib/csv';
import type {
  DeviceImportMode,
  DeviceImportOutcome,
  DeviceImportRow,
  DeviceImportRowResult,
} from '@/lib/api/device-management';

/** Rows per call; mirrors the server's per-call bound. */
export const IMPORT_CHUNK_SIZE = 1000;

export const IMPORT_FIELDS = [
  'token',
  'deviceTypeToken',
  'externalId',
  'name',
  'description',
  'metadata',
  'provisioningProfileToken',
  'credentialType',
  'credentialId',
  'credentialValue',
] as const;
export type ImportField = (typeof IMPORT_FIELDS)[number];

const CREDENTIAL_FIELDS: ImportField[] = ['credentialType', 'credentialId', 'credentialValue'];

/** The fields a mode accepts: the credential columns exist only in factory-credentials mode. */
export function fieldsForMode(mode: DeviceImportMode): ImportField[] {
  return mode === 'FACTORY_CREDENTIALS'
    ? [...IMPORT_FIELDS]
    : IMPORT_FIELDS.filter((f) => !CREDENTIAL_FIELDS.includes(f));
}

/** field -> index of the file column that feeds it. */
export type ColumnMapping = Partial<Record<ImportField, number>>;

/** Lowercased, with everything but letters and digits removed (accents folded). */
export function normalizeHeader(header: string): string {
  return header
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]/g, '');
}

// Header spellings that name each field: the field's own name (what dcctl and the docs use),
// and the headers this console's own export writes, in English and Spanish, so an exported
// list is importable as it is. The export's "Type" column holds the type NAME, not its
// token, and is deliberately not an alias of deviceTypeToken.
const ALIASES: Record<ImportField, string[]> = {
  token: ['token', 'devicetoken'],
  deviceTypeToken: ['devicetypetoken', 'typetoken', 'tokendeltipo'],
  externalId: ['externalid', 'externaldeviceid'],
  name: ['name', 'devicename', 'nombre'],
  description: ['description', 'descripcion'],
  metadata: ['metadata', 'metadatos'],
  // The profile the device is earmarked for: the one profile that may later adopt it when it
  // registers itself. Both spellings of the concept ("expected" is the console's word for it).
  provisioningProfileToken: [
    'provisioningprofiletoken',
    'provisioningprofile',
    'expectedprovisioningprofile',
    'expectedprofile',
  ],
  credentialType: ['credentialtype'],
  credentialId: ['credentialid'],
  credentialValue: ['credentialvalue', 'credentialsecret'],
};

/** Maps each field to the first file column whose header names it. Unmatched fields are left out. */
export function autoMap(header: string[], mode: DeviceImportMode): ColumnMapping {
  const normalized = header.map(normalizeHeader);
  const mapping: ColumnMapping = {};
  const taken = new Set<number>();
  for (const field of fieldsForMode(mode)) {
    const idx = normalized.findIndex((h, i) => !taken.has(i) && ALIASES[field].includes(h));
    if (idx >= 0) {
      mapping[field] = idx;
      taken.add(idx);
    }
  }
  return mapping;
}

// A cell with the export's formula guard undone, so a file this console exported reads back
// to its original values. NOT for the credential secret: see rawCell.
const cell = (row: string[], idx: number | undefined): string | undefined => {
  const v = idx === undefined ? undefined : row[idx];
  return v === undefined ? undefined : unguardCell(v);
};

// A cell exactly as the file holds it. The credential secret is read this way: a password
// that begins `'=` is a password, not a guarded formula, and the device presents it as is.
const rawCell = (row: string[], idx: number | undefined): string | undefined =>
  idx === undefined ? undefined : row[idx];

const trimmed = (v: string | undefined): string | undefined => {
  const t = v?.trim();
  return t ? t : undefined;
};

/**
 * Builds the import rows from the file's data rows. `fileType` is the one device type for the
 * whole file; a row's own type cell wins over it when both exist. Empty cells are omitted, so
 * the server reads them as absent.
 */
export function buildRows(
  table: string[][],
  mapping: ColumnMapping,
  mode: DeviceImportMode,
  fileType?: string,
): DeviceImportRow[] {
  const credentials = mode === 'FACTORY_CREDENTIALS';
  return table.map((r) => {
    const out: DeviceImportRow = {
      token: trimmed(cell(r, mapping.token)) ?? '',
      deviceTypeToken: trimmed(cell(r, mapping.deviceTypeToken)) ?? trimmed(fileType) ?? '',
    };
    const externalId = trimmed(cell(r, mapping.externalId));
    if (externalId) out.externalId = externalId;
    const name = trimmed(cell(r, mapping.name));
    if (name) out.name = name;
    const description = trimmed(cell(r, mapping.description));
    if (description) out.description = description;
    const metadata = trimmed(cell(r, mapping.metadata));
    if (metadata) out.metadata = metadata;
    // An empty cell earmarks nothing: absent, never an empty token the server would refuse.
    const earmark = trimmed(cell(r, mapping.provisioningProfileToken));
    if (earmark) out.provisioningProfileToken = earmark;
    if (credentials) {
      const credentialType = trimmed(cell(r, mapping.credentialType));
      if (credentialType) out.credentialType = credentialType.toUpperCase();
      const credentialId = cell(r, mapping.credentialId);
      if (credentialId !== undefined && credentialId.trim() !== '') out.credentialId = credentialId.trim();
      const secret = rawCell(r, mapping.credentialValue);
      // Verbatim: the device presents exactly these bytes.
      if (secret !== undefined && secret !== '') out.credentialValue = secret;
    }
    return out;
  });
}

/** Splits rows into calls of at most `size`. */
/** Largest serialized request a call may carry; the server refuses a body over 4 MiB. */
export const IMPORT_CHUNK_MAX_BYTES = 3 * 1024 * 1024;

const encoder = new TextEncoder();

/**
 * Splits rows into calls of at most `size` rows AND at most `maxBytes` of serialized JSON, so
 * a chunk of rows with large metadata or secrets stays under the request body cap. A single
 * row larger than the cap goes alone (the server then refuses it, by field, as it would any
 * oversize value).
 */
export function chunk<T>(
  items: T[],
  size: number = IMPORT_CHUNK_SIZE,
  maxBytes: number = IMPORT_CHUNK_MAX_BYTES,
): T[][] {
  const out: T[][] = [];
  let current: T[] = [];
  let bytes = 0;
  for (const item of items) {
    const n = encoder.encode(JSON.stringify(item)).length + 1;
    if (current.length > 0 && (current.length >= size || bytes + n > maxBytes)) {
      out.push(current);
      current = [];
      bytes = 0;
    }
    current.push(item);
    bytes += n;
  }
  if (current.length > 0) out.push(current);
  return out;
}

/** A row's outcome keyed to its position in the FILE (1-based data row, header excluded). */
export interface FileRowOutcome {
  /** 1-based data row in the file. */
  row: number;
  token: string;
  outcome: DeviceImportOutcome;
  code?: string | null;
  message?: string | null;
}

export type OutcomeCounts = Record<DeviceImportOutcome, number>;

export const emptyCounts = (): OutcomeCounts => ({
  CREATED: 0,
  EXISTS: 0,
  CONFLICT: 0,
  INVALID: 0,
  REFUSED: 0,
});

export function countOutcomes(outcomes: FileRowOutcome[]): OutcomeCounts {
  const counts = emptyCounts();
  for (const o of outcomes) counts[o.outcome]++;
  return counts;
}

/** The sender: one call of at most IMPORT_CHUNK_SIZE rows. */
export type ChunkSender = (rows: DeviceImportRow[], dryRun: boolean) => Promise<DeviceImportRowResult[]>;

export interface RunResult {
  outcomes: FileRowOutcome[];
  /** True when the run stopped before every chunk was sent (aborted). */
  stopped: boolean;
}

/**
 * Sends the rows chunk by chunk, strictly in sequence, and returns every outcome keyed to its
 * file row. `onProgress` is told how many rows have been answered. When the signal fires the
 * run stops BEFORE the next chunk (a call in flight completes: it is committed per row on the
 * server and its answer is kept). A thrown error from a chunk propagates with the outcomes
 * gathered so far attached, so the operator can see how far it got; re-running the same file
 * is safe because created rows then report EXISTS.
 */
export async function runImport(
  rows: DeviceImportRow[],
  dryRun: boolean,
  send: ChunkSender,
  opts: { signal?: AbortSignal; onProgress?: (answered: number, outcomes: FileRowOutcome[]) => void } = {},
): Promise<RunResult> {
  const outcomes: FileRowOutcome[] = [];
  let offset = 0;
  for (const part of chunk(rows)) {
    if (opts.signal?.aborted) return { outcomes, stopped: true };
    let results: DeviceImportRowResult[];
    try {
      results = await send(part, dryRun);
    } catch (err) {
      throw new ImportRunError(err, outcomes);
    }
    for (const r of results) {
      outcomes.push({
        row: offset + r.row,
        token: r.token,
        outcome: r.outcome,
        code: r.code,
        message: r.message,
      });
    }
    offset += part.length;
    opts.onProgress?.(offset, outcomes);
  }
  return { outcomes, stopped: false };
}

/** A chunk failed outright (network, authorization, a server fault); earlier chunks stand. */
export class ImportRunError extends Error {
  readonly cause: unknown;
  readonly outcomes: FileRowOutcome[];
  constructor(cause: unknown, outcomes: FileRowOutcome[]) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = 'ImportRunError';
    this.cause = cause;
    this.outcomes = outcomes;
  }
}

/** Blanks the secret column of a table in place of keeping it after an import. */
export function stripSecrets(table: string[][], mapping: ColumnMapping): string[][] {
  const idx = mapping.credentialValue;
  if (idx === undefined) return table;
  return table.map((r) => r.map((c, i) => (i === idx ? '' : c)));
}

const RESULT_HEADERS = ['row', 'token', 'outcome', 'code', 'message'] as const;

/** The per-row results as CSV. Carries no secret: outcomes never hold one. */
export function resultsCsv(outcomes: FileRowOutcome[]): string {
  const columns: CsvColumn<FileRowOutcome>[] = [
    { header: RESULT_HEADERS[0], value: (o) => o.row },
    { header: RESULT_HEADERS[1], value: (o) => o.token },
    { header: RESULT_HEADERS[2], value: (o) => o.outcome },
    { header: RESULT_HEADERS[3], value: (o) => o.code },
    { header: RESULT_HEADERS[4], value: (o) => o.message },
  ];
  return toCsv(columns, outcomes);
}

/** Outcomes that mean the row still needs the operator. EXISTS is a success. */
export const FAILED_OUTCOMES: DeviceImportOutcome[] = ['CONFLICT', 'INVALID', 'REFUSED'];

/**
 * The rows that did not land, as a file that can be fixed and imported again: the canonical
 * column names, so it maps without help, plus the outcome columns (ignored on re-import).
 * The secret column is never written, in any mode.
 */
export function failedRowsCsv(
  table: string[][],
  mapping: ColumnMapping,
  mode: DeviceImportMode,
  outcomes: FileRowOutcome[],
  fileType?: string,
): string {
  const fields = fieldsForMode(mode).filter((f) => f !== 'credentialValue');
  const failed = outcomes.filter((o) => FAILED_OUTCOMES.includes(o.outcome));
  const head = [...fields, 'outcome', 'code', 'message'].map((h) => csvCell(h)).join(',');
  const lines = failed.map((o) => {
    const src = table[o.row - 1] ?? [];
    const cells = fields.map((f) => {
      const own = mapping[f] === undefined ? '' : (src[mapping[f]!] ?? '');
      return csvCell(f === 'deviceTypeToken' && own.trim() === '' ? (fileType ?? '') : own);
    });
    return [...cells, csvCell(o.outcome), csvCell(o.code), csvCell(o.message)].join(',');
  });
  return [head, ...lines].join('\r\n') + '\r\n';
}
