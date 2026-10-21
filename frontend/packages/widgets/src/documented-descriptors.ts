// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The built-in descriptors AS PUBLISHED: the runtime descriptors of builtin-descriptors.ts with each
// option's authoring `doc` attached from builtin-option-docs.ts.
//
// 🔑 WHY TWO. The renderer needs a widget's channel, options, defaults and binding -- not the prose
// that explains an option to an author. Prose is the heaviest part of a descriptor, and a descriptor
// is read eagerly, so it rode in the first download of every host (the replay player's initial bundle
// grew with each widget for exactly this reason). The runtime descriptors therefore carry no `doc`;
// this module rebuilds the documented form for EVERY public export that always carried it:
// `builtinWidgetDescriptors`, `builtinWidgetCatalog`, `builtinWidgetDefinitions`,
// `createBuiltinWidgetRegistry` and `WIDGET_OPTIONS`. These are published API -- an embedder's config
// panel may read `doc` off any of them -- so none of them may lose it. The doc-free definitions are
// published separately, and additively, as `builtinWidgetDefinitionsWithoutDocs`: what a render-only
// host (the shared host-widgets composition) registers.
//
// Every value here is a /* @__PURE__ */ call, so a host that imports none of them -- the replay
// player, the dashboard viewer -- has the whole module, and the doc table behind it, dropped by its
// bundler. Nothing in this package's own runtime path imports this file; the replay build's size
// gate (its absence check) is what would show it if that changed.

import { createWidgetCatalog, type WidgetDescriptor, type WidgetOptionSpecs } from '@devicechain/dashboards';
import { createWidgetRegistry, type WidgetRegistry } from '@devicechain/widget-sdk';

import { builtinWidgetDescriptors as runtimeDescriptors } from './builtin-descriptors';
import { OPTION_DOCS, OPTION_DOC_OVERRIDES } from './builtin-option-docs';
import { builtinWidgetDefinitions as runtimeDefinitions } from './builtin-registry';

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

// The registrations, each paired with its documented descriptor (the component is the same one).
function documentDefinitions<T extends readonly { descriptor: WidgetDescriptor }[]>(definitions: T): T {
  const byId: Record<string, WidgetDescriptor> = builtinWidgetDescriptors;
  return definitions.map((d) => ({ ...d, descriptor: byId[d.descriptor.id] })) as unknown as T;
}

export const builtinWidgetDefinitions = /* @__PURE__ */ documentDefinitions(runtimeDefinitions);

// A fresh, sealed registry holding exactly the built-ins, documented. Every call returns an
// independent instance.
export function createBuiltinWidgetRegistry(): WidgetRegistry {
  return createWidgetRegistry(builtinWidgetDefinitions);
}

// Every built-in's option schema keyed by id, documented: the same projection options.ts makes of
// the runtime descriptors (its type is that one's), by identity with the documented descriptors.
function optionsById<T extends Record<string, WidgetDescriptor>>(descriptors: T): { readonly [K in keyof T]: T[K]['options'] } {
  return Object.freeze(Object.fromEntries(Object.entries(descriptors).map(([id, d]) => [id, d.options]))) as {
    readonly [K in keyof T]: T[K]['options'];
  };
}

export const WIDGET_OPTIONS = /* @__PURE__ */ optionsById(builtinWidgetDescriptors);
