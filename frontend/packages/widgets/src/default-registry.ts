// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { WidgetRegistry } from '@devicechain/widget-sdk';

import { createBuiltinWidgetRegistry } from './builtin-registry';

// The registry a renderer uses when its host passes none: the built-ins, built once per
// module instance. A registry is sealed and immutable, so sharing this one default between
// hosts carries no state — a host with extension packages passes its own.
let builtin: WidgetRegistry | undefined;

export function defaultWidgetRegistry(): WidgetRegistry {
  builtin ??= createBuiltinWidgetRegistry();
  return builtin;
}
