// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a widget hands its frame so the viewer can download the data it shows (the frame's export
// menu). Declared apart from the frame so the menu, loaded only when the viewer opens it, and the
// widgets that build the table share one definition.
//
// 🔴 A table is EXACTLY WHAT THE WIDGET SHOWS: the span and the bucket on screen, not a fresh
// question. The widget builds it from the state it is rendering, and says in `name` what it covers.

export type WidgetExportCell = string | number | boolean | null | undefined;

// What a frame's `exportRows` returns. A promise is allowed so a widget can build its table in code it
// loads only when the viewer asks (the table builders are not part of a board's first download).
export type WidgetExportResult = WidgetExportTable | null | undefined | Promise<WidgetExportTable | null | undefined>;

export interface WidgetExportTable {
  // The file name, without its extension: what the export covers (the widget, its span, its bucket,
  // the instants it spans) so a file found months later still says what it is. The frame makes it
  // safe for a file system.
  name: string;
  // Already-localized headings, units included.
  columns: readonly string[];
  // One entry per row, a cell for each column. A number stays a number (a spreadsheet can sum it); an
  // instant is an ISO-8601 string with its zone.
  rows: readonly (readonly WidgetExportCell[])[];
}
