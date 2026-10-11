// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { DeviceConfiguration } from '@/lib/api/device-configuration';

// The one banner the panel shows above the desired/reported comparison. It is chosen
// from the SERVER's fields only: `pending` and `stale` are read, never recomputed from
// the revision numbers. Recomputing would have to re-derive "same revision AND same
// digest AND APPLIED", and a client that disagrees with the server about that is
// worse than one that shows nothing.
export type ConfigurationBanner = 'noRevision' | 'pending' | 'inSync';

export function configurationBanner(c: DeviceConfiguration): ConfigurationBanner {
  if (!c.desired) return 'noRevision';
  return c.pending ? 'pending' : 'inSync';
}

// Display-only: how far behind the device's report is, for the pending sentence.
// Never used to decide pending.
export function reportedRevisionLabel(c: DeviceConfiguration): number | null {
  return c.reported?.revision ?? null;
}
