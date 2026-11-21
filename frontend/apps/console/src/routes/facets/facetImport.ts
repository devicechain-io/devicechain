// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the bulk facet-value import: mapping a parsed file's columns onto
// (entity type, token, facet key, value) rows, finding duplicates across the WHOLE file,
// sending the rows in sequential chunks and folding the answers back onto the file's own row
// numbers. No React, no network of its own (the sender is passed in), so every rule here is
// unit-tested.
//
// 🔴 THE SERVER DECIDES WHAT IS VALID. A row is sent as the file wrote it (the value is not
// trimmed or typed here), so the dry run and the import agree by construction. The one thing
// the browser decides is the duplicate: the server only sees one call at a time, and a file is
// several calls, so a repeated (entity type, token, key) is caught here across the whole file
// and never sent. The first occurrence wins, which is also what a single call does.

import { csvCell, toCsv, unguardCell, type CsvColumn } from '@/lib/csv';
import { chunk, IMPORT_CHUNK_SIZE } from '@/routes/devices/deviceImport';
import type {
  FacetImportOutcome,
  FacetValueImportRow,
  FacetValueImportRowResult,
} from '@/lib/api/facet-import';

export { IMPORT_CHUNK_SIZE };

export const FACET_IMPORT_FIELDS = ['memberType', 'token', 'key', 'value'] as const;
export type FacetImportField = (typeof FACET_IMPORT_FIELDS)[number];

/** field -> index of the file column that feeds it. */
export type ColumnMapping = Partial<Record<FacetImportField, number>>;

/** Whole-file values used when a row's own cell is empty or the column is not mapped. */
export interface FileDefaults {
  memberType?: string;
  key?: string;
}

const normalizeHeader = (header: string): string =>
  header
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]/g, '');

// The field's own name (what the mutation and the docs use) plus the plain words a
// spreadsheet column would carry, in English and Spanish.
const ALIASES: Record<FacetImportField, string[]> = {
  memberType: ['membertype', 'entitytype', 'type', 'family', 'memberfamily', 'tipodeentidad', 'tipo'],
  token: ['token', 'entitytoken', 'devicetoken', 'assettoken', 'areatoken', 'customertoken'],
  key: ['key', 'facetkey', 'facet', 'attributekey', 'attribute', 'clave', 'clavedefaceta'],
  value: ['value', 'facetvalue', 'valor'],
};

/** Maps each field to the first file column whose header names it. Unmatched fields are left out. */
export function autoMap(header: string[]): ColumnMapping {
  const normalized = header.map(normalizeHeader);
  const mapping: ColumnMapping = {};
  const taken = new Set<number>();
  for (const field of FACET_IMPORT_FIELDS) {
    const idx = normalized.findIndex((h, i) => !taken.has(i) && ALIASES[field].includes(h));
    if (idx >= 0) {
      mapping[field] = idx;
      taken.add(idx);
    }
  }
  return mapping;
}

// A header that names a credential. Such a column is never written to the failed-rows file,
// and if it was mapped as the value its cells are blanked there too.
const SECRET_HEADER = /secret|passw(or)?d|passphrase|credential|apikey|privatekey|bearer|accesskey|authtoken/;

/** True when a column header looks like it holds a credential. */
export const isSecretHeader = (header: string): boolean => SECRET_HEADER.test(normalizeHeader(header));

/** Blanks every secret-looking column of a table (header kept), so none is held after a run. */
export function stripSecretColumns(table: string[][]): string[][] {
  if (table.length === 0) return table;
  const secret = new Set(table[0].flatMap((h, i) => (isSecretHeader(h) ? [i] : [])));
  if (secret.size === 0) return table;
  return table.map((r, ri) => (ri === 0 ? r : r.map((c, i) => (secret.has(i) ? '' : c))));
}

// A cell with the export's formula guard undone (these are facet values, not credentials).
const cell = (row: string[], idx: number | undefined): string | undefined => {
  const v = idx === undefined ? undefined : row[idx];
  return v === undefined ? undefined : unguardCell(v);
};

const trimmed = (v: string | undefined): string => v?.trim() ?? '';

/**
 * Builds one import row per data row, in file order. An entity type or key cell wins over the
 * whole-file default; the family is lowercased because the vocabulary is lowercase. The value
 * is sent as written: the server trims it and refuses a blank one for that row.
 */
export function buildRows(table: string[][], mapping: ColumnMapping, defaults: FileDefaults = {}): FacetValueImportRow[] {
  return table.map((r) => ({
    memberType: (trimmed(cell(r, mapping.memberType)) || trimmed(defaults.memberType)).toLowerCase(),
    token: trimmed(cell(r, mapping.token)),
    key: trimmed(cell(r, mapping.key)) || trimmed(defaults.key),
    value: cell(r, mapping.value) ?? '',
  }));
}

const naturalKey = (r: FacetValueImportRow): string => JSON.stringify([r.memberType, r.token, r.key]);

/**
 * 1-based file rows that repeat an earlier row's (entity type, token, key). The first
 * occurrence is not reported; only the later ones are. A row with no token or key is not
 * compared (the server refuses it by itself).
 */
export function findDuplicates(rows: FacetValueImportRow[]): Set<number> {
  const seen = new Set<string>();
  const dup = new Set<number>();
  rows.forEach((r, i) => {
    if (!r.token || !r.key) return;
    const k = naturalKey(r);
    if (seen.has(k)) dup.add(i + 1);
    else seen.add(k);
  });
  return dup;
}

/** A row's outcome keyed to its position in the FILE (1-based data row, header excluded). */
export interface FileRowOutcome {
  /** 1-based data row in the file. */
  row: number;
  memberType: string;
  token: string;
  key: string;
  outcome: FacetImportOutcome;
  code?: string | null;
  message?: string | null;
}

export type OutcomeCounts = Record<FacetImportOutcome, number>;

export const emptyCounts = (): OutcomeCounts => ({ SET: 0, EXISTS: 0, INVALID: 0, REFUSED: 0 });

export function countOutcomes(outcomes: FileRowOutcome[]): OutcomeCounts {
  const counts = emptyCounts();
  for (const o of outcomes) counts[o.outcome]++;
  return counts;
}

/** Outcomes that mean the row still needs the operator. EXISTS is a success. */
export const FAILED_OUTCOMES: FacetImportOutcome[] = ['INVALID', 'REFUSED'];

export const DUPLICATE_CODE = 'DUPLICATE_IN_FILE';
export const NO_RESULT_CODE = 'NO_RESULT';

/** The sender: one call of at most IMPORT_CHUNK_SIZE rows. */
export type ChunkSender = (rows: FacetValueImportRow[], dryRun: boolean) => Promise<FacetValueImportRowResult[]>;

export interface RunResult {
  /** An outcome for every row answered, in file order. */
  outcomes: FileRowOutcome[];
  /** True when the run stopped before every chunk was sent (aborted). */
  stopped: boolean;
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

const byRow = (a: FileRowOutcome, b: FileRowOutcome) => a.row - b.row;

/**
 * Sends the rows chunk by chunk, strictly in sequence, and returns every outcome keyed to its
 * file row. `only` limits the run to those file rows (a retry); duplicates are always judged
 * against the whole file so a retry never sends a row the first run held back. When the signal
 * fires the run stops BEFORE the next chunk (a call in flight completes: it commits per row on
 * the server and its answer is kept). A thrown chunk error propagates with the outcomes
 * gathered so far attached; re-running is safe because an unchanged value reports EXISTS.
 */
export async function runFacetImport(
  rows: FacetValueImportRow[],
  dryRun: boolean,
  send: ChunkSender,
  opts: { only?: Set<number>; signal?: AbortSignal; onProgress?: (answered: number, outcomes: FileRowOutcome[]) => void } = {},
): Promise<RunResult> {
  const duplicates = findDuplicates(rows);
  const outcomes: FileRowOutcome[] = [];
  const toSend: { file: number; row: FacetValueImportRow }[] = [];
  rows.forEach((row, i) => {
    const file = i + 1;
    if (opts.only && !opts.only.has(file)) return;
    if (duplicates.has(file)) {
      outcomes.push({
        row: file,
        memberType: row.memberType,
        token: row.token,
        key: row.key,
        outcome: 'INVALID',
        code: DUPLICATE_CODE,
        message: null,
      });
    } else {
      toSend.push({ file, row });
    }
  });
  opts.onProgress?.(outcomes.length, [...outcomes].sort(byRow));

  for (const part of chunk(toSend)) {
    if (opts.signal?.aborted) return { outcomes: outcomes.sort(byRow), stopped: true };
    let results: FacetValueImportRowResult[];
    try {
      results = await send(
        part.map((p) => p.row),
        dryRun,
      );
    } catch (err) {
      throw new ImportRunError(err, outcomes.sort(byRow));
    }
    const answered = new Map(results.map((r) => [r.row, r]));
    part.forEach((p, i) => {
      const r = answered.get(i + 1);
      // A row the server did not answer is a fault, never a silent success.
      outcomes.push(
        r
          ? { row: p.file, memberType: r.memberType, token: r.token, key: r.key, outcome: r.outcome, code: r.code, message: r.message }
          : {
              row: p.file,
              memberType: p.row.memberType,
              token: p.row.token,
              key: p.row.key,
              outcome: 'REFUSED',
              code: NO_RESULT_CODE,
              message: null,
            },
      );
    });
    opts.onProgress?.(outcomes.length, [...outcomes].sort(byRow));
  }
  return { outcomes: outcomes.sort(byRow), stopped: false };
}

/** Folds a retry's outcomes over the earlier ones (the retry's answer replaces a row's). */
export function mergeOutcomes(previous: FileRowOutcome[], fresh: FileRowOutcome[]): FileRowOutcome[] {
  const merged = new Map(previous.map((o) => [o.row, o]));
  for (const o of fresh) merged.set(o.row, o);
  return [...merged.values()].sort(byRow);
}

const RESULT_HEADERS = ['row', 'memberType', 'token', 'key', 'outcome', 'code', 'message'] as const;

/** The per-row results as CSV. Carries no value: outcomes never hold one. */
export function resultsCsv(outcomes: FileRowOutcome[]): string {
  const columns: CsvColumn<FileRowOutcome>[] = [
    { header: RESULT_HEADERS[0], value: (o) => o.row },
    { header: RESULT_HEADERS[1], value: (o) => o.memberType },
    { header: RESULT_HEADERS[2], value: (o) => o.token },
    { header: RESULT_HEADERS[3], value: (o) => o.key },
    { header: RESULT_HEADERS[4], value: (o) => o.outcome },
    { header: RESULT_HEADERS[5], value: (o) => o.code },
    { header: RESULT_HEADERS[6], value: (o) => o.message },
  ];
  return toCsv(columns, outcomes);
}

/**
 * The rows that did not land, as a file that can be fixed and imported again: the canonical
 * four columns (so it maps without help) plus the outcome columns (ignored on re-import).
 * Nothing else from the source file is written, so a secret-looking column never appears; and
 * if the value column itself is secret-looking its cells are blanked.
 */
export function failedRowsCsv(
  table: string[][],
  header: string[],
  mapping: ColumnMapping,
  defaults: FileDefaults,
  outcomes: FileRowOutcome[],
): string {
  const rows = buildRows(table, mapping, defaults);
  const valueIsSecret = mapping.value !== undefined && isSecretHeader(header[mapping.value] ?? '');
  const head = [...FACET_IMPORT_FIELDS, 'outcome', 'code', 'message'].map((h) => csvCell(h)).join(',');
  const lines = outcomes
    .filter((o) => FAILED_OUTCOMES.includes(o.outcome))
    .map((o) => {
      const r = rows[o.row - 1];
      return [
        csvCell(r?.memberType ?? o.memberType),
        csvCell(r?.token ?? o.token),
        csvCell(r?.key ?? o.key),
        csvCell(valueIsSecret ? '' : (r?.value ?? '')),
        csvCell(o.outcome),
        csvCell(o.code),
        csvCell(o.message),
      ].join(',');
    });
  return [head, ...lines].join('\r\n') + '\r\n';
}
