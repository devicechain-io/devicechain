// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// lintLayout: a pure check of a definition's placement against what each widget can show.
//
// The parser refuses what cannot be DRAWN (two grid footprints on one cell, a relative widget
// with no host); this is the question it leaves open: is each placement a USABLE one? A widget
// past the right edge, a widget smaller or larger than it declares it can be, a relative widget
// pushed off the canvas. Answered once, here, for every producer of a definition (the sim
// fixtures today; the editor and the publish gate next).
//
// Cells are square and of near-constant size, so a span in cells IS a readable size: a widget
// that declares a sound minSpan cannot be too small to read at any width the board renders
// without reflowing. There is therefore no pixel floor.
//
// Widget knowledge is INJECTED. `dashboards` must not depend on `widgets`, so the span limits a
// widget type declares arrive through `spanLimitsOf`. Overlap is deliberately not a code: the
// parser is where it is refused.

import {
  BASE_BREAKPOINT,
  breakpointColumns,
  gutterPx,
  layoutBreakpoints,
  layoutRects,
  resolvePlacement,
  spanPx,
} from './grid';
import { sameLabel } from './same-label';
import { widgetBinding } from './slots';
import type { DashboardDefinition, Placement, WidgetInstance } from './types';
import { pinnedAxes, type WidgetLayoutSpec, type WidgetSpan } from './widget-descriptor';

export type LayoutLintSeverity = 'error' | 'warning' | 'info';

export type LayoutLintCode =
  | 'span-out-of-limits'
  | 'overrun-columns'
  | 'relative-off-canvas'
  | 'no-layout-declared'
  | 'title-duplicates-subtitle';

export interface LayoutFinding {
  code: LayoutLintCode;
  severity: LayoutLintSeverity;
  widgetId: string;
  message: string;
  // The breakpoint whose placement the finding is about, when it is not 'base'. An editor needs
  // it to point at the right placement, and two breakpoints' findings must not collapse into one.
  breakpoint?: string;
}

// The findings that BLOCK a publish: the placement is outside what the widget can show, or off the
// canvas. A published version is immutable and is what viewers are served, so these are refused
// there; the draft stays editable. The editor cannot normally produce them, so they catch what
// arrives some other way (an import, an older descriptor, the API). The rest (`no-layout-declared`,
// `title-duplicates-subtitle`) are advice.
export const PUBLISH_BLOCKING_LAYOUT_CODES: readonly LayoutLintCode[] = [
  'span-out-of-limits',
  'overrun-columns',
  'relative-off-canvas',
];

export function blockingLayoutFindings(findings: readonly LayoutFinding[]): LayoutFinding[] {
  return findings.filter((f) => PUBLISH_BLOCKING_LAYOUT_CODES.includes(f.code));
}

// What a widget type allows, per axis. Any bound may be absent, on either axis: a widget that
// declares none can be any size, and one that pins only its rows has no column ceiling.
export interface SpanLimits {
  minSpan?: Partial<WidgetSpan>;
  maxSpan?: Partial<WidgetSpan>;
}

export interface LayoutLintOptions {
  // The column count the `base` placements are checked against: the authored count, or a
  // reflow count. A breakpoint that declares its own count is checked at that count.
  columns: number;
  // undefined means the widget declares no limits at all, which is reported (info) rather
  // than assumed fine, so a type that forgot to declare them is visible.
  spanLimitsOf: (widget: WidgetInstance) => SpanLimits | undefined;
}

// The limits a descriptor's `layout` block declares, in the shape lintLayout takes: its
// minSpan and maxSpan, with an axis its resize mode pins held at defaultSpan. A descriptor with
// no block yields undefined (reported as `no-layout-declared`), so a host wires
// `spanLimitsOf: (w) => spanLimitsOfLayout(registry.descriptor(w.type)?.layout)`.
export function spanLimitsOfLayout(layout: WidgetLayoutSpec | undefined): SpanLimits | undefined {
  if (!layout) return undefined;
  const min: Partial<WidgetSpan> = { ...layout.minSpan };
  const max: Partial<WidgetSpan> = { ...layout.maxSpan };
  for (const axis of pinnedAxes(layout.resize)) {
    min[axis] = layout.defaultSpan[axis];
    max[axis] = layout.defaultSpan[axis];
  }
  const limits: SpanLimits = {};
  if (Object.keys(min).length > 0) limits.minSpan = min;
  if (Object.keys(max).length > 0) limits.maxSpan = max;
  return limits;
}

// The cell sizes a relative widget's box is measured at. Its position is a straight line in the
// cell size (an offset is in cell pitches, the gutter is fixed px), so a box inside the canvas at
// the smallest cell and at a very large one is inside it at every size between: the smallest is
// where the gutter weighs most, the large one where the cells do.
const RELATIVE_PROBE_CELLS = (minCellSize: number) => [minCellSize, minCellSize * 16];

// Rounding slack, in px, for a box that sits exactly on the canvas edge.
const EDGE_EPSILON = 0.01;

export function lintLayout(def: DashboardDefinition, options: LayoutLintOptions): LayoutFinding[] {
  const findings: LayoutFinding[] = [];
  const columnsAt = (bp: string) =>
    bp === BASE_BREAKPOINT || breakpointColumns(def.canvas, bp) === def.canvas.grid.columns
      ? options.columns
      : breakpointColumns(def.canvas, bp);
  const breakpoints = layoutBreakpoints(def.canvas.breakpoints, def.widgets);

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

    for (const [bp, placement] of Object.entries(widget.layout)) {
      const where = bp === BASE_BREAKPOINT ? '' : ` at breakpoint ${bp}`;
      const at = bp === BASE_BREAKPOINT ? {} : { breakpoint: bp };
      const columns = columnsAt(bp);
      if (placement.mode === 'grid' && placement.col + placement.cols > columns) {
        findings.push({
          code: 'overrun-columns',
          severity: 'error',
          widgetId: widget.id,
          ...at,
          message: `spans columns ${placement.col}..${placement.col + placement.cols} of ${columns}${where}`,
        });
      }
      const span = spanOf(def, widget, bp, placement);
      if (limits && span) {
        const problem = spanProblem(span.cols, span.rows, limits);
        if (problem) {
          findings.push({
            code: 'span-out-of-limits',
            severity: 'error',
            widgetId: widget.id,
            ...at,
            message: `${span.cols}x${span.rows} ${problem}${where}`,
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
        message: duplicate,
      });
    }
  }

  // A relative widget is checked at every breakpoint it is SHOWN at, its inherited base placement
  // included, wherever that can differ from base: its host (or its host's host) may have its own
  // placement there, or the breakpoint may be narrower. Where nothing differs, base already said it.
  const byId = new Map(def.widgets.map((w) => [w.id, w]));
  const differsFromBase = (w: WidgetInstance, bp: string): boolean => {
    if (columnsAt(bp) !== options.columns) return true;
    for (let cur: WidgetInstance | undefined = w, hops = 0; cur && hops < 3; hops++) {
      if (Object.prototype.hasOwnProperty.call(cur.layout, bp)) return true;
      const p = resolvePlacement(cur.layout, bp);
      cur = p.mode === 'relative' ? byId.get(p.host) : undefined;
    }
    return false;
  };
  for (const bp of breakpoints) {
    const columns = columnsAt(bp);
    const gutter = gutterPx(def.canvas.grid);
    const off = new Set<string>();
    for (const cell of RELATIVE_PROBE_CELLS(def.canvas.grid.minCellSize)) {
      const width = spanPx(columns, cell, gutter.x);
      for (const [id, r] of layoutRects(def.widgets, bp, cell, gutter)) {
        const w = byId.get(id)!;
        if (resolvePlacement(w.layout, bp).mode !== 'relative' || off.has(id)) continue;
        if (bp !== BASE_BREAKPOINT && !differsFromBase(w, bp)) continue;
        if (r.x < -EDGE_EPSILON || r.y < -EDGE_EPSILON || r.x + r.w > width + EDGE_EPSILON) {
          off.add(id);
          const where = bp === BASE_BREAKPOINT ? '' : ` at breakpoint ${bp}`;
          findings.push({
            code: 'relative-off-canvas',
            severity: 'error',
            widgetId: id,
            ...(bp === BASE_BREAKPOINT ? {} : { breakpoint: bp }),
            message: `falls outside the ${columns}-column canvas${where} (left ${round(r.x)}, top ${round(r.y)}, right ${round(r.x + r.w)} of ${round(width)} px at ${round(cell)} px cells)`,
          });
        }
      }
    }
  }

  return findings;
}

function round(n: number): number {
  return Math.round(n * 10) / 10;
}

// The span a placement covers: a grid footprint's own, a relative widget's size, or for a
// relative widget that fills its host, the host's span (one level of chaining at most).
function spanOf(
  def: DashboardDefinition,
  widget: WidgetInstance,
  bp: string,
  p: Placement,
): { cols: number; rows: number } | undefined {
  if (p.mode === 'grid') return { cols: p.cols, rows: p.rows };
  if (!('fill' in p.size)) return p.size;
  const host = def.widgets.find((w) => w.id === p.host);
  if (!host || host === widget) return undefined;
  return spanOf(def, host, bp, resolvePlacement(host.layout, bp));
}

function spanProblem(cols: number, rows: number, limits: SpanLimits): string | undefined {
  const { minSpan = {}, maxSpan = {} } = limits;
  const bound = (b: Partial<WidgetSpan>) =>
    b.cols === undefined ? `${b.rows}-row` : b.rows === undefined ? `${b.cols}-column` : `${b.cols}x${b.rows}`;
  if (cols < (minSpan.cols ?? 1) || rows < (minSpan.rows ?? 1)) {
    return `is below the ${bound(minSpan)} minimum`;
  }
  if (cols > (maxSpan.cols ?? Infinity) || rows > (maxSpan.rows ?? Infinity)) {
    return `is above the ${bound(maxSpan)} maximum`;
  }
  return undefined;
}

// The frame shows the title option and, under it, the token of the entity the widget is
// bound to. Two ways of putting one identity on screen twice are visible from the
// definition, and both are reported:
//   - the title IS the token (a renderer can also see this one and suppress the subject line);
//   - the title is the bound SLOT's label, and that slot is bound to the entity the subject
//     line names. A slot label is the author's name for the entity, so the card reads e.g.
//     "SP-DZ-0001" over "sp-dozer-01": the same machine under two names, which no
//     string compare in the renderer can catch.
// What it cannot see is a title that names the entity some third way the definition does
// not record (an external id typed straight into the title with no slot label behind it):
// that is for whoever builds the board, not for this check.
function titleDuplicatesSubtitle(def: DashboardDefinition, widget: WidgetInstance): string | undefined {
  const title = widget.options?.title;
  if (typeof title !== 'string' || title.trim() === '') return undefined;
  const binding = widgetBinding(def, widget);
  const subject = binding?.kind === 'device' ? binding.deviceToken : binding?.anchor.targetToken;
  if (!subject) return undefined;
  const ds = widget.datasource;
  const slotLabel = ds?.kind === 'slot' ? def.slots?.[ds.slot]?.label : undefined;
  const same = (a: string | undefined) => sameLabel(a, title);
  if (same(subject)) return `the title and the subject line both read "${title.trim()}"`;
  if (same(slotLabel)) {
    return `the title "${title.trim()}" is the slot's name for "${subject}", which the subject line already shows`;
  }
  return undefined;
}
