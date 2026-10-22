// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// CSV for the frame's export menu: RFC 4180 encoding and a spreadsheet-formula guard. Loaded with
// the menu, only when the viewer opens it.
//
// This is the console's list-export encoder (apps/console/src/lib/csv.ts) copied, not imported: a
// widget package cannot depend on an application. Keep the two in step.
//
// 🔴 An export is a spreadsheet opened by a person, and the cells are attacker-influenced (a device
// name, a measurement name, a title). A TEXT cell that begins with `=`, `+`, `-`, `@`, tab or carriage
// return is read by Excel/Sheets as a FORMULA, so it is written with a leading single quote. The guard
// is applied here, once, to every text cell and heading; a widget cannot forget it per column. A number
// is written as a number (never guarded: -12.5 must stay summable) and a string that is entirely a plain
// number is left alone for the same reason.
//
// 🔴 UTF-8 WITH A BYTE-ORDER MARK, on purpose. Units are not ASCII ("°C", "µS/cm") and neither are
// many device names, and Excel, the tool an auditor opens this in, decodes a BOM-less file as the
// system code page and shows "Â°C". The cost is that a program reading the file raw sees U+FEFF at
// the start of the first heading; the ones that matter strip it (Python's utf-8-sig, every
// spreadsheet). A consumer that cannot should drop the first three bytes.

import type { WidgetExportCell, WidgetExportTable } from './widget-export';

// A string that is entirely a plain number cannot start a formula, and quoting it would turn a
// reading like -12.5 into text a spreadsheet will not sum.
const PLAIN_NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/;

// The formula triggers, plus their full-width forms (an IME produces those, and some spreadsheet
// builds normalise them on entry). Matched after any leading whitespace too: an importer that trims
// cells would otherwise hand ` =cmd` to the formula parser with its guard trimmed away.
const FORMULA_LEAD = /^[=+\-@＝＋－＠]/;

/** True when a spreadsheet could read the text as a formula. */
export function formulaLike(text: string): boolean {
  if (/^[\t\r]/.test(text)) return true;
  const lead = text.replace(/^\s+/, '');
  return FORMULA_LEAD.test(lead) && !PLAIN_NUMBER.test(text);
}

/** Encodes one cell: formula-guarded, then quoted when RFC 4180 requires it. */
export function csvCell(value: WidgetExportCell): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : '';
  let text = typeof value === 'string' ? value : String(value);
  if (typeof value === 'string' && formulaLike(text)) text = `'${text}`;
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** The table as CSV text: headings first, CRLF line endings, a trailing CRLF. */
export function toCsv(table: Pick<WidgetExportTable, 'columns' | 'rows'>): string {
  const lines = [table.columns.map(csvCell).join(',')];
  for (const row of table.rows) lines.push(table.columns.map((_c, i) => csvCell(row[i])).join(','));
  return lines.join('\r\n') + '\r\n';
}

// A name safe on every file system: what is not a letter, digit, dot, dash or underscore becomes a
// dash, runs collapse, and a name that is nothing but separators falls to `fallback`. An accented letter
// keeps its base letter (the accent is a combining mark once decomposed, and is dropped). A name Windows
// reserves for a device (`CON`, `nul.csv`, `COM1`: reserved whatever the extension) is prefixed with an
// underscore, since a widget title is the tenant's to choose.
const WINDOWS_RESERVED = /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)/i;
export function safeFileStem(name: string, fallback = 'widget'): string {
  const stem = name
    .normalize('NFKD')
    .replace(/\p{M}+/gu, '')
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/^[-.]+/, '')
    .slice(0, 160)
    .replace(/[-.]+$/, '');
  if (stem === '') return fallback;
  return WINDOWS_RESERVED.test(stem) ? `_${stem}` : stem;
}

// U+FEFF, spelled as a code point so no editor can silently drop the invisible character.
const UTF8_BOM = String.fromCharCode(0xfeff);

/** Saves bytes as a file download. */
export function downloadBlob(filename: string, blob: Blob): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** Saves the table as `<name>.csv`: UTF-8 with a byte-order mark (see the top of this file). */
export function downloadCsv(table: WidgetExportTable): void {
  downloadBlob(`${safeFileStem(table.name)}.csv`, new Blob([UTF8_BOM, toCsv(table)], { type: 'text/csv;charset=utf-8' }));
}
