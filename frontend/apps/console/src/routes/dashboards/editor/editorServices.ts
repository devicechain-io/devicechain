// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The host services the config panel hands a registered editor (see controlEditors.tsx).
// Each is a plain function an editor calls through its props; the editor never imports the
// API module itself.

import type { EditorCommandChoices } from '@devicechain/widget-sdk';

import {
  getDeviceCommandVocabulary,
  listCommandDefinitionsForDevice,
} from '@/lib/api/device-management';
import { commandChoices } from '@/routes/devices/commandVocabulary';

// The commands a device accepts, reconciled the way the enqueue gate does (published when the
// profile constrains the vocabulary, drafts too when it does not). A module-level function,
// so its identity is stable across renders and an editor can key a query on it.
export async function commandChoicesService(deviceToken: string): Promise<EditorCommandChoices> {
  const [vocabulary, drafts] = await Promise.all([
    getDeviceCommandVocabulary(deviceToken),
    listCommandDefinitionsForDevice(deviceToken),
  ]);
  return commandChoices(vocabulary, drafts);
}
