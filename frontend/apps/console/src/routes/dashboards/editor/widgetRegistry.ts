// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget registry the editor authors against.
//
// The editor is handed ONE registry and reads descriptors from it; it never lists widgets
// itself. A host that composes extra widgets passes its own registry down from the
// workspace; without one, the built-ins are used, built once and shared (a registry is
// sealed, so sharing it is safe).

import type { WidgetRegistry } from '@devicechain/widget-sdk';
import { createBuiltinWidgetRegistry } from '@devicechain/widgets';

export type EditorWidgetRegistry = WidgetRegistry;

let builtin: EditorWidgetRegistry | undefined;

export function defaultWidgetRegistry(): EditorWidgetRegistry {
  builtin ??= createBuiltinWidgetRegistry();
  return builtin;
}
