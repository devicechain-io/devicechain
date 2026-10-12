// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The widget registry the editor authors against.
//
// The editor is handed ONE registry and reads descriptors from it; it never lists widgets
// itself. The default is the deployment's shared composition (@devicechain/host-widgets) --
// the same registry /dash and the replay player build -- so a package an operator installs
// there is authorable here with no change to the console. A test composes its own through
// the `registry` prop.

import { hostWidgetRegistry, type WidgetRegistry } from '@devicechain/host-widgets';

export type EditorWidgetRegistry = WidgetRegistry;

export function defaultWidgetRegistry(): EditorWidgetRegistry {
  return hostWidgetRegistry();
}
