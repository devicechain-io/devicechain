// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The layout lint, as the editor shows it: inline on each widget, and as the publish gate.
//
// The lint itself is @devicechain/dashboards' lintLayout; what lives here is the console's wiring
// of it (the span limits come from the registry the board was parsed with) and its wording. As
// with option issues (optionIssues.ts), the CODE is translated here and the package's English
// message is not what an author reads.

import {
  blockingLayoutFindings,
  DEFAULT_CANVAS_DENSITY,
  DENSITY_SCALE,
  effectiveLayout,
  lintLayout,
  resolvePlacement,
  spanLimitsOfLayout,
  UNKNOWN_WIDGET_TYPE,
  type DashboardDefinition,
  type LayoutFinding,
  type LayoutLintCode,
  type SpanLimits,
  type WidgetInstance,
} from '@devicechain/dashboards';

import type { EditorWidgetRegistry } from './widgetRegistry';

// The span limits a widget's registered descriptor declares. A type the registry does not hold
// (an unresolved extension) has no limits to check against.
export function spanLimitsFor(registry: EditorWidgetRegistry): (widget: WidgetInstance) => SpanLimits | undefined {
  return (widget) =>
    widget.type === UNKNOWN_WIDGET_TYPE ? undefined : spanLimitsOfLayout(registry.descriptor(widget.type)?.layout);
}

// Every finding on a definition, advice included.
export function layoutFindingsOf(def: DashboardDefinition, registry: EditorWidgetRegistry, viewportWidth?: number): LayoutFinding[] {
  if (viewportWidth !== undefined) {
    // Match the viewer's fixed-width cap, density, breakpoint selection and automatic reflow.
    // Flatten only the rendered placements into a temporary base layout so lint does not also
    // report placements belonging to other widths. The authored definition is never changed.
    const sizing = def.canvas.sizing;
    const width = typeof sizing === 'object' && 'width' in sizing ? Math.min(viewportWidth, sizing.width) : viewportWidth;
    const layout = effectiveLayout(def, width, {
      scale: DENSITY_SCALE[def.canvas.density ?? DEFAULT_CANVAS_DENSITY],
      limitsOf: spanLimitsFor(registry),
    });
    const rendered: DashboardDefinition = {
      ...def,
      canvas: { ...def.canvas, grid: { ...def.canvas.grid, columns: layout.columns }, breakpoints: { base: 0 } },
      widgets: def.widgets.map((widget) => ({
        ...widget,
        layout: { base: layout.grid.get(widget.id) ?? resolvePlacement(widget.layout, layout.breakpoint) },
      })),
    };
    return lintLayout(rendered, { columns: layout.columns, spanLimitsOf: spanLimitsFor(registry) });
  }
  return lintLayout(def, { columns: def.canvas.grid.columns, spanLimitsOf: spanLimitsFor(registry) });
}

// The findings that stop a publish.
export function publishLayoutBlockers(def: DashboardDefinition, registry: EditorWidgetRegistry): LayoutFinding[] {
  return blockingLayoutFindings(layoutFindingsOf(def, registry));
}

// The findings worth showing on the widget itself: `no-layout-declared` is a note for the widget's
// author, not something the board's author can act on.
export function inlineFindings(findings: readonly LayoutFinding[]): LayoutFinding[] {
  return findings.filter((f) => f.severity !== 'info');
}

// A translation key per lint code. `Record<LayoutLintCode, …>` is the gate: a code added to the
// package does not build here until it is worded.
export const LAYOUT_FINDING_KEYS: Record<LayoutLintCode, string> = {
  'span-out-of-limits': 'layoutFindingSpan',
  'overrun-columns': 'layoutFindingOverrun',
  'relative-off-canvas': 'layoutFindingOffCanvas',
  'no-layout-declared': 'layoutFindingNoLayout',
  'title-duplicates-subtitle': 'layoutFindingTitle',
};
