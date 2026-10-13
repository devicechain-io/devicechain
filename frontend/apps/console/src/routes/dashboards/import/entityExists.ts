// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { getArea } from '@/lib/api/areas';
import { getAsset } from '@/lib/api/assets';
import { getCustomer } from '@/lib/api/customers';
import { getDevice } from '@/lib/api/device-management';
import type { BindingKind } from './importDefinition';

// Whether `token` names an entity of the signed-in tenant. A direct lookup by token, not a
// scan of a listing page: a tenant with more entities than one page would otherwise report
// real devices as foreign. A failed lookup rejects (see checkBindingRefs).
export async function entityExists(kind: BindingKind, token: string): Promise<boolean> {
  switch (kind) {
    case 'device':
      return (await getDevice(token)) !== null;
    case 'customer':
      return (await getCustomer(token)) !== null;
    case 'area':
      return (await getArea(token)) !== null;
    case 'asset':
      return (await getAsset(token)) !== null;
  }
}
