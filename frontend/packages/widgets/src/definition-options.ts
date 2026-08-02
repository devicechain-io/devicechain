// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The definition-level layer over validateWidgetOptions: "is every widget on this
// board configured with options the renderer actually reads?"
//
// WHY IT IS A SEPARATE FUNCTION RATHER THAN A LOOP AT EACH CALL SITE. Every consumer
// wants the same three things — walk the widgets, keep the issues, and know WHICH
// widget each belongs to — and the third is the one a hand-rolled loop forgets. An
// issue reported without its widget id tells an author that `commandName` is missing
// on a board with three command buttons, which is a report they cannot act on.
//
// 🔑 THE OPTION SCHEMAS ARE TYPESCRIPT, AND THAT DECIDES WHERE THE GATE CAN LIVE.
// dashboard-management stores a definition as opaque JSON and checks only that it is
// a JSON object; it is Go, so it cannot run these schemas, and a Go copy of them
// would be a THIRD representation of the same table (the reason ADR-076 declined a
// cross-language widget manifest). So the strictness belongs to whoever PRODUCES a
// definition, and 🔴 EACH PRODUCER HAS TO WIRE IT FOR ITSELF — there is no chokepoint
// downstream that would catch a producer nobody thought about:
//
//   • the console, by its publish action (apps/console/src/lib/api/dashboards.ts);
//   • the sim, by the golden-fixture gate over frontend/testdata/sim-dashboards, which
//     covers every dashboard of every REGISTERED scenario. It was widgetlab-only when
//     this function was written, and the sentence here claimed otherwise — buildingpulse,
//     the demo board, was gated by nothing. Two reviewers found the same overclaim
//     independently. The gate walks sim.Registry now, so the claim is true by
//     construction rather than by a list someone has to remember to extend.
//
// Anything ELSE writing a definition — raw GraphQL, a future importer — is unchecked,
// because dashboard-management deliberately validates nothing. This is not the
// server-side, ADR-044-style publish gate it superficially resembles, and calling it one
// would misdescribe exactly which documents are protected.

import { type DashboardDefinition, type WidgetInstance } from '@devicechain/dashboards';

import { WIDGET_OPTIONS, validateWidgetOptions, type OptionIssue } from './options';

// A DefinitionOptionIssue is an OptionIssue plus the widget that carries it. `title` is
// the widget's own `options.title` when it has a usable one — an author picks a widget
// out of a board by its title, not by the generated id — and is absent otherwise rather
// than being filled with a placeholder that would read as a real title.
export interface DefinitionOptionIssue extends OptionIssue {
  widgetId: string;
  title?: string;
}

// A definition as this module reads it: only the widget list matters, so the fixture
// tests and the console's in-flight editor state both satisfy it without either having
// to be a fully parsed DashboardDefinition.
type WidgetCarrier = Pick<DashboardDefinition, 'widgets'>;

// validateDefinitionOptions reports every option issue on the board, in widget order.
// An empty result means every widget's bag is one the renderer honors in full.
export function validateDefinitionOptions(definition: WidgetCarrier): DefinitionOptionIssue[] {
  return definition.widgets.flatMap((widget) =>
    validateWidgetOptions(widget.type, widget.options).map((issue) => {
      const title = widget.options?.title;
      return {
        ...issue,
        widgetId: widget.id,
        ...(typeof title === 'string' && title.trim() ? { title } : {}),
      };
    }),
  );
}

// stripUnknownOptions removes every option key no widget of that type reads, leaving
// everything else untouched.
//
// 🔴 IT EXISTS BECAUSE 'unknown' IS THE ONE ISSUE CODE AN AUTHOR CANNOT FIX IN THE UI.
// Every other code sits on a DECLARED key, so the config panel renders a control for it
// and the author edits or clears it. An unknown key is by definition one the panel has
// no control for — and the panel preserves the bag it did not write (it spreads
// `widget.options` on every edit), so the key survives every subsequent change. Without
// this, a publish gate that treats 'unknown' as fatal is a dead end: a dashboard that
// acquired a leftover key — from a version predating an option rename, or from a
// producer other than the console — could never be published again through the UI.
//
// Returns the SAME object when there is nothing to strip. Callers hold definitions in
// React state and compare them to decide "dirty", so a fresh-but-equal object would
// present a no-op as an unsaved edit.
export function stripUnknownOptions<T extends WidgetCarrier>(definition: T): T {
  let changed = false;
  const widgets = definition.widgets.map((widget) => {
    const stripped = stripUnknownWidgetOptions(widget);
    if (stripped !== widget) changed = true;
    return stripped;
  });
  return changed ? { ...definition, widgets } : definition;
}

function stripUnknownWidgetOptions(widget: WidgetInstance): WidgetInstance {
  const bag = widget.options;
  if (!bag) return widget;
  const specs = WIDGET_OPTIONS[widget.type];
  // hasOwnProperty.call, not `in` — see validateWidgetOptions: every specs table
  // inherits Object.prototype, so `in` would report '__proto__' and 'constructor' as
  // declared and leave exactly the keys a JSON.parse'd definition can smuggle in.
  const kept = Object.keys(bag).filter((key) => Object.prototype.hasOwnProperty.call(specs, key));
  if (kept.length === Object.keys(bag).length) return widget;
  const options: Record<string, unknown> = {};
  for (const key of kept) options[key] = bag[key];
  return { ...widget, options };
}
