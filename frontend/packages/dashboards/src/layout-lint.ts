// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// lintLayout: a pure check of a definition's placement against what each widget can show.
//
// The parser clamps the fields of a box but never asks whether the box is a USABLE one: it
// does not look at the right edge, and it has no idea how small a gauge can get before its
// readout is unreadable. This is that question, answered once, here, for every producer of a
// definition (the sim fixtures today; the editor and the publish gate later).
//
// Widget knowledge is INJECTED. `dashboards` must not depend on `widgets`, so the span limits
// a widget type declares arrive through `spanLimitsOf`, and the temporary pixel floor through
// `pixelFloor.minPxOf`. Overlap is deliberately not a code: the parser is where it is refused.

import { widgetBinding } from './slots';
import type { DashboardDefinition, WidgetInstance } from './types';

export type LintSeverity = 'error' | 'warning' | 'info';

export type LayoutLintCode =
  | 'span-out-of-limits'
  | 'overrun-columns'
  | 'no-layout-declared'
  | 'title-duplicates-subtitle'
  // TEMPORARY (the A-slice pixel floor): removed when constant-size square cells make a span
  // in cells a readable size by construction.
  | 'below-readable-size';

export interface LayoutFinding {
  code: LayoutLintCode;
  severity: LintSeverity;
  widgetId: string;
  message: string;
  // The container width a width-dependent finding was measured at (pixel floor only).
  width?: number;
}

export interface Span {
  cols: number;
  rows: number;
}

// What a widget type allows. Either bound may be absent: a widget that declares neither can
// be any size.
export interface SpanLimits {
  minSpan?: Span;
  maxSpan?: Span;
}

export interface PixelSize {
  width: number;
  height: number;
}

export interface LayoutLintOptions {
  // The column count the boxes are checked against: the authored count, or a reflow count.
  columns: number;
  // undefined means the widget declares no limits at all, which is reported (info) rather
  // than assumed fine, so a type that forgot to declare them is visible.
  spanLimitsOf: (widget: WidgetInstance) => SpanLimits | undefined;
  // TEMPORARY. The rendered size of each widget at each container width, against the
  // smallest size the widget can be read at.
  pixelFloor?: {
    widths: number[];
    minPxOf: (widget: WidgetInstance) => PixelSize | undefined;
  };
}

const BASE = 'base';

export function lintLayout(def: DashboardDefinition, options: LayoutLintOptions): LayoutFinding[] {
  const findings: LayoutFinding[] = [];

  for (const widget of def.widgets) {
    const limits = options.spanLimitsOf(widget);
    if (!limits) {
      findings.push({
        code: 'no-layout-declared',
        severity: 'info',
        widgetId: widget.id,
        message: `${widget.type} declares no span limits, so its size cannot be checked`,
      });
    }

    for (const [bp, box] of Object.entries(widget.layout)) {
      const where = bp === BASE ? '' : ` at breakpoint ${bp}`;
      if (box.col < 0 || box.col + box.colSpan > options.columns) {
        findings.push({
          code: 'overrun-columns',
          severity: 'error',
          widgetId: widget.id,
          message:
            `spans columns ${box.col}..${box.col + box.colSpan} of ${options.columns}${where}`,
        });
      }
      if (limits) {
        const problem = spanProblem(box.colSpan, box.rowSpan, limits);
        if (problem) {
          findings.push({
            code: 'span-out-of-limits',
            severity: 'error',
            widgetId: widget.id,
            message: `${box.colSpan}x${box.rowSpan} ${problem}${where}`,
          });
        }
      }
    }

    const duplicate = titleDuplicatesSubtitle(def, widget);
    if (duplicate) {
      findings.push({
        code: 'title-duplicates-subtitle',
        severity: 'warning',
        widgetId: widget.id,
        message: `the title and the subject line both read "${duplicate}"`,
      });
    }

    if (options.pixelFloor) {
      const floor = options.pixelFloor.minPxOf(widget);
      const base = widget.layout[BASE];
      if (floor && base) {
        for (const width of options.pixelFloor.widths) {
          const size = renderedSize(def, options.columns, base.colSpan, base.rowSpan, width);
          if (size.width < floor.width || size.height < floor.height) {
            findings.push({
              code: 'below-readable-size',
              severity: 'error',
              widgetId: widget.id,
              width,
              message:
                `renders ${Math.round(size.width)}x${Math.round(size.height)} px at ${width} px wide; ` +
                `${widget.type} needs at least ${floor.width}x${floor.height}`,
            });
          }
        }
      }
    }
  }

  return findings;
}

function spanProblem(cols: number, rows: number, limits: SpanLimits): string | undefined {
  const { minSpan, maxSpan } = limits;
  if (minSpan && (cols < minSpan.cols || rows < minSpan.rows)) {
    return `is below the ${minSpan.cols}x${minSpan.rows} minimum`;
  }
  if (maxSpan && (cols > maxSpan.cols || rows > maxSpan.rows)) {
    return `is above the ${maxSpan.cols}x${maxSpan.rows} maximum`;
  }
  return undefined;
}

// The grid's own rule, as the renderer lays it out: columns are fluid and share the width
// left after the gutters, rows are fixed px, n tracks cover n*track + (n-1)*gutter.
function renderedSize(
  def: DashboardDefinition,
  columns: number,
  colSpan: number,
  rowSpan: number,
  containerWidth: number,
): PixelSize {
  const { gap, rowHeight } = def.canvas.grid;
  const colGap = typeof gap === 'number' ? gap : gap.col;
  const rowGap = typeof gap === 'number' ? gap : gap.row;
  const cell = (containerWidth - colGap * (columns - 1)) / columns;
  return {
    width: colSpan * cell + (colSpan - 1) * colGap,
    height: rowSpan * rowHeight + (rowSpan - 1) * rowGap,
  };
}

// The frame shows the title option and, under it, the token of the entity the widget is
// bound to. When they read the same the identity is on screen twice.
function titleDuplicatesSubtitle(def: DashboardDefinition, widget: WidgetInstance): string | undefined {
  const title = widget.options?.title;
  if (typeof title !== 'string' || title.trim() === '') return undefined;
  const binding = widgetBinding(def, widget);
  const subject = binding?.kind === 'device' ? binding.deviceToken : binding?.anchor.targetToken;
  if (!subject) return undefined;
  return title.trim().toLowerCase() === subject.trim().toLowerCase() ? title.trim() : undefined;
}
