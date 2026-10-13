// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The one CSV export path for console lists: RFC 4180 encoding, a spreadsheet-formula
// guard, and a bounded, cancellable page-through of an existing list query.
//
// 🔴 A list export is a spreadsheet opened by a person, and several fields in it are
// attacker-influenced (a device name, an alarm key, an audit actor). A cell that begins
// with `=`, `+`, `-`, `@`, tab or carriage return is read by Excel/Sheets as a FORMULA,
// so every such text cell is written with a leading single quote. That is the standard
// CSV-injection defence and it is applied here, once, to every cell — a caller cannot
// forget it per column.

/** Hard ceiling on exported rows; a larger result is cut here and reported as truncated. */
export const EXPORT_ROW_CAP = 10_000;

/** Rows requested per page while exporting (the services clamp a page at 1000). */
export const EXPORT_PAGE_SIZE = 500;

export type CsvValue = string | number | boolean | null | undefined;

export interface CsvColumn<T> {
  /** Already-localized header text. */
  header: string;
  value: (row: T) => CsvValue;
}

// A string that is entirely a plain number cannot start a formula, and quoting it would turn
// a measurement like -12.5 into text a spreadsheet will not sum. Anything with another
// character in it (`-1+cmd|...`) fails the match and is guarded.
const PLAIN_NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

// The formula triggers, plus their full-width forms (an IME produces those, and some
// spreadsheet builds normalise them on entry). Matched after any leading whitespace too:
// an importer that trims cells (LibreOffice's "Trim spaces", a pasted range) would
// otherwise hand ` =cmd` to the formula parser with its guard trimmed away.
const FORMULA_LEAD = /^[=+\-@＝＋－＠]/;

/** True when a spreadsheet could read the text as a formula. */
function formulaLike(text: string): boolean {
  if (/^[\t\r]/.test(text)) return true;
  const lead = text.replace(/^\s+/, '');
  return FORMULA_LEAD.test(lead) && !PLAIN_NUMBER.test(text);
}

/** Encodes one cell: formula-guarded, then quoted when RFC 4180 requires it. */
export function csvCell(value: CsvValue): string {
  if (value === null || value === undefined) return '';
  let text = typeof value === 'string' ? value : String(value);
  if (typeof value === 'string' && formulaLike(text)) {
    text = `'${text}`;
  }
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** Renders rows as CSV text: header row first, CRLF line endings, columns in the given order. */
export function toCsv<T>(columns: CsvColumn<T>[], rows: T[]): string {
  const lines = [columns.map((c) => csvCell(c.header)).join(',')];
  for (const row of rows) lines.push(columns.map((c) => csvCell(c.value(row))).join(','));
  return lines.join('\r\n') + '\r\n';
}

/** ISO-8601 (UTC) for a timestamp, or empty when absent or unparseable. */
export function isoTime(value?: string | null): string {
  if (!value) return '';
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString();
}

export interface CollectResult<T> {
  rows: T[];
  /** True when more rows existed than were collected (the cap was hit). */
  truncated: boolean;
}

export interface CollectOptions {
  pageSize?: number;
  cap?: number;
  signal?: AbortSignal;
  /** Called after each page with the number of rows collected so far. */
  onProgress?: (collected: number) => void;
}

export interface PageResult<T> {
  results: T[];
  pagination: { totalRecords?: number | null };
}

const cancelled = () => new DOMException('Export cancelled', 'AbortError');

// Settles with the page, or rejects the moment the signal fires — so a cancel takes effect
// at once rather than after a slow page returns. The list API takes no signal, so the
// request itself still completes in the background; its result is discarded.
function abortable<R>(work: Promise<R>, signal?: AbortSignal): Promise<R> {
  if (!signal) return work;
  if (signal.aborted) return Promise.reject(cancelled());
  return new Promise<R>((resolve, reject) => {
    const onAbort = () => reject(cancelled());
    signal.addEventListener('abort', onAbort, { once: true });
    work.then(
      (v) => {
        signal.removeEventListener('abort', onAbort);
        resolve(v);
      },
      (e: unknown) => {
        signal.removeEventListener('abort', onAbort);
        reject(e);
      },
    );
  });
}

/**
 * Pages through a list query until it is exhausted or the cap is reached. Rejects with an
 * AbortError when the signal fires, and never returns a partial result in that case.
 */
export async function collectPages<T>(
  fetchPage: (pageNumber: number, pageSize: number) => Promise<PageResult<T>>,
  opts: CollectOptions = {},
): Promise<CollectResult<T>> {
  const pageSize = opts.pageSize ?? EXPORT_PAGE_SIZE;
  const cap = opts.cap ?? EXPORT_ROW_CAP;
  const rows: T[] = [];
  for (let pageNumber = 1; ; pageNumber++) {
    const page = await abortable(fetchPage(pageNumber, pageSize), opts.signal);
    // A page that resolved in the same tick as the abort still counts as cancelled.
    if (opts.signal?.aborted) throw cancelled();
    const total = page.pagination.totalRecords;
    const room = cap - rows.length;
    if (page.results.length > room) {
      rows.push(...page.results.slice(0, room));
      opts.onProgress?.(rows.length);
      return { rows, truncated: true };
    }
    rows.push(...page.results);
    opts.onProgress?.(rows.length);
    const exhausted =
      page.results.length < pageSize || (total != null && rows.length >= total);
    if (exhausted) return { rows, truncated: false };
    if (rows.length >= cap) return { rows, truncated: true };
  }
}

/** Saves CSV text as a file. UTF-8 with a byte-order mark so Excel reads non-ASCII correctly. */
export function downloadCsv(filename: string, csv: string): void {
  const blob = new Blob(['﻿', csv], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** `<base>-<UTC date>.csv`, with anything unsafe in a filename replaced. */
export function exportFilename(base: string, now: Date = new Date()): string {
  const safe = base.replace(/[^A-Za-z0-9._-]+/g, '-');
  return `${safe}-${now.toISOString().slice(0, 10)}.csv`;
}
