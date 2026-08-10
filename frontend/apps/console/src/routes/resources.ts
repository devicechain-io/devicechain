// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The registry families served by the generic list/detail pages, in one place.
//
// This lives apart from App.tsx for a reason that is not tidiness: it is the set a
// test can ENUMERATE. `resources.test.tsx` walks it and proves each family's edit
// carries the entity's metadata back to the server, and a family added to this
// array is covered by that the moment it is added — whereas a list re-declared in
// the test would only ever cover what someone remembered to add twice.

import type { RegistryResource } from '@/components/registry';
import { deviceTypeResource } from '@/routes/device-types/resource';
import { deviceProfileResource } from '@/routes/device-profiles/resource';
import { deviceGroupResource } from '@/routes/device-groups/resource';
import { assetResource } from '@/routes/assets/resource';
import { assetTypeResource } from '@/routes/asset-types/resource';
import { assetGroupResource } from '@/routes/asset-groups/resource';
import { customerResource } from '@/routes/customers/resource';
import { customerTypeResource } from '@/routes/customer-types/resource';
import { customerGroupResource } from '@/routes/customer-groups/resource';
import { areaResource } from '@/routes/areas/resource';
import { areaTypeResource } from '@/routes/area-types/resource';
import { areaGroupResource } from '@/routes/area-groups/resource';
import { geofenceResource } from '@/routes/geofences/resource';

// `any` because the array mixes RegistryResource<DeviceType | Asset | Customer | …>
// and the element T is invariant; each is consumed by a generic page that re-narrows it.
export const REGISTRY_RESOURCES: RegistryResource<any>[] = [
  deviceTypeResource,
  deviceProfileResource,
  deviceGroupResource,
  assetResource,
  assetTypeResource,
  assetGroupResource,
  customerResource,
  customerTypeResource,
  customerGroupResource,
  areaResource,
  areaTypeResource,
  areaGroupResource,
  geofenceResource,
];
