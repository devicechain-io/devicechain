// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget composition every DeviceChain host app shares.
//
// ONE registry, built once from the built-in widgets plus the operator's extension list
// (./extensions.ts), and handed -- registry AND its catalog -- to every path that touches a
// widget: parseDashboardDefinition(raw, { catalog }), the option validators, the renderer,
// the editor's menu and settings, the synthetic preview and the recording player. A host that
// parsed with no catalog would read an extension as a placeholder and never check a built-in's
// settings version, which is the defect this module exists to make impossible: there is no
// "parse without the catalog" spelling here to reach for.

import { createWidgetRegistry, type ErasedWidgetDefinition, type WidgetRegistry } from '@devicechain/widget-sdk';
// The built-ins WITHOUT their option docs: no host here shows an option's `doc` (the console's
// panel reads its own message catalog), and the prose would otherwise ride in every host's first
// download -- the replay player's size gate checks it stays out.
import { builtinWidgetDefinitionsWithoutDocs as builtinWidgetDefinitions } from '@devicechain/widgets';

import { extensionWidgets } from './extensions';

// createHostWidgetRegistry composes the built-ins with `extra`. The default is the operator's
// list; a test (or an embedder composing its own host) passes another. Throws the registry's
// one WidgetRegistryError, listing every fault, when the composition is invalid -- a broken
// package fails the host's start rather than one widget at a time.
export function createHostWidgetRegistry(extra: readonly ErasedWidgetDefinition[] = extensionWidgets): WidgetRegistry {
  return createWidgetRegistry([...builtinWidgetDefinitions, ...extra]);
}

let shared: WidgetRegistry | undefined;

// hostWidgetRegistry is the deployment's registry, built on first use and then shared. A
// registry is sealed, so sharing it carries no state.
export function hostWidgetRegistry(): WidgetRegistry {
  shared ??= createHostWidgetRegistry();
  return shared;
}

// hostWidgetCatalog is the React-free view of that registry, for parsing and headless checks.
export function hostWidgetCatalog(): WidgetRegistry['catalog'] {
  return hostWidgetRegistry().catalog;
}

export { extensionWidgets };
// So a host app types its props without taking its own dependency on the SDK.
export type { WidgetRegistry } from '@devicechain/widget-sdk';
