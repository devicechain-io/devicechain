// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-in descriptors AS PUBLISHED: the runtime descriptors of builtin-descriptors.ts with each
// option's authoring `doc` attached from builtin-option-docs.ts.
//
// 🔑 WHY TWO. The renderer needs a widget's channel, options, defaults and binding -- not the prose
// that explains an option to an author. Prose is the heaviest part of a descriptor, and a descriptor
// is read eagerly, so it rode in the first download of every host (the replay player's initial bundle
// grew with each widget for exactly this reason). The runtime descriptors therefore carry no `doc`;
// this module rebuilds the documented form for the two public exports that always had it,
// `builtinWidgetDescriptors` and `builtinWidgetCatalog`. (`WIDGET_OPTIONS` and the other by-id views stay
// projections of the REGISTERED descriptors by identity, so they are doc-free like those.)
//
// Every export here is a /* @__PURE__ */ call, so a host that imports none of them -- the replay
// player, the dashboard viewer -- has the whole module, and the doc table behind it, dropped by its
// bundler. Nothing in this package's own runtime path imports this file; the dist-shape test and the
// replay build's size gate are what would show it if that changed.

import { createWidgetCatalog, type WidgetDescriptor, type WidgetOptionSpecs } from '@devicechain/dashboards';

import { builtinWidgetDescriptors as runtimeDescriptors } from './builtin-descriptors';
import { OPTION_DOCS, OPTION_DOC_OVERRIDES } from './builtin-option-docs';

// A copy of an option table with every spec's (and list item field's) doc attached. A COPY, never an
// edit in place: the runtime descriptors are frozen by the runtime catalog, and a documented
// descriptor sharing their spec objects would freeze/alias them too.
function documentOptions(widgetId: string, specs: WidgetOptionSpecs, prefix: string): WidgetOptionSpecs {
  const out: Record<string, unknown> = {};
  for (const [key, spec] of Object.entries(specs)) {
    const path = `${prefix}${key}`;
    const copy: Record<string, unknown> = { ...spec };
    const doc = OPTION_DOC_OVERRIDES[widgetId]?.[path] ?? OPTION_DOCS[path];
    if (doc !== undefined) copy.doc = doc;
    if ('item' in spec && spec.item) copy.item = documentOptions(widgetId, spec.item as WidgetOptionSpecs, `${path}.`);
    // Arrays and nested plain data (enum values, labels) are shared by reference with the
    // runtime spec; they are read-only data on both sides.
    out[key] = copy;
  }
  return out as WidgetOptionSpecs;
}

function documentDescriptor<D extends WidgetDescriptor>(descriptor: D): D {
  return { ...descriptor, options: documentOptions(descriptor.id, descriptor.options, '') };
}

function documentAll<T extends Record<string, WidgetDescriptor>>(descriptors: T): T {
  return Object.fromEntries(Object.entries(descriptors).map(([id, d]) => [id, documentDescriptor(d)])) as T;
}

export const builtinWidgetDescriptors = /* @__PURE__ */ documentAll(runtimeDescriptors);

function catalogOf(descriptors: Record<string, WidgetDescriptor>) {
  return createWidgetCatalog(Object.values(descriptors));
}

export const builtinWidgetCatalog = /* @__PURE__ */ catalogOf(builtinWidgetDescriptors);

// Same shape as before: every built-in's option schema, keyed by id, now with docs.
export function documentedWidgetOptions<T extends Record<string, WidgetDescriptor>>(
  descriptors: T,
): { readonly [K in keyof T]: T[K]['options'] } {
  return Object.freeze(
    Object.fromEntries(Object.entries(descriptors).map(([id, d]) => [id, d.options])),
  ) as { readonly [K in keyof T]: T[K]['options'] };
}
