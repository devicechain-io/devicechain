// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { storedDefinition, stripDefaultBindings, type DashboardDefinition } from '@devicechain/dashboards';

// The JSON a dashboard exports as, and the only form the import flow is specified against.
//
// storedDefinition, not the parsed model: a widget of a type this console does not know
// must be exported as its stored object, exactly as a save writes it. A plain export keeps
// the slot default bindings (renders as-is); a template strips them so whoever imports it
// must bind each slot. Pretty-printed for a readable file or paste.
export function exportDefinitionJson(definition: DashboardDefinition, template: boolean): string {
  return JSON.stringify(storedDefinition(template ? stripDefaultBindings(definition) : definition), null, 2);
}
