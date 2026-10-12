// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The by-id views of the built-in registrations that older consumers still import.
//
// 🔴 READ-ONLY PROJECTIONS, NOT A REGISTRY. A widget's channel and whether it binds a
// datasource are declared once, on its descriptor (builtin-descriptors.ts), and the
// renderer resolves everything through a registry instance (createWidgetRegistry) -- never
// through these. They exist so code that wants to ask "which channel is this built-in on?"
// without building a registry (the console's config panel, fixtures) keeps working. They are
// frozen, computed from the descriptors at module load, and there is no way to add an
// entry: an extension widget registers through createWidgetRegistry, not here.

import type { BuiltinWidgetId, WidgetChannel } from '@devicechain/dashboards';

import { builtinWidgetDescriptors } from './builtin-descriptors';

// WidgetChannel itself is declared once, in @devicechain/dashboards; re-exported so this
// module's importers are unchanged.
export type { WidgetChannel };

type Descriptors = typeof builtinWidgetDescriptors;

const entries = Object.entries(builtinWidgetDescriptors) as Array<[BuiltinWidgetId, Descriptors[BuiltinWidgetId]]>;

// A built-in's data channel, as its descriptor declares it.
export const WIDGET_CHANNEL: { readonly [K in BuiltinWidgetId]: Descriptors[K]['channel'] } = Object.freeze(
  Object.fromEntries(entries.map(([id, d]) => [id, d.channel])),
) as { readonly [K in BuiltinWidgetId]: Descriptors[K]['channel'] };

// Whether a built-in binds a DATASOURCE -- the entity whose data it reads. Derived from the
// descriptor's datasource mode: `none` binds nothing (label, image, the entity selector).
export const WIDGET_BINDS_DATASOURCE: { readonly [K in BuiltinWidgetId]: boolean } = Object.freeze(
  Object.fromEntries(entries.map(([id, d]) => [id, d.datasource.mode !== 'none'])),
) as { readonly [K in BuiltinWidgetId]: boolean };
