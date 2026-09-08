// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm } from '@/components/registry';
import {
  listDeviceTypes,
  createDevice,
  updateDevice,
  getDevice,
  type Device,
} from '@/lib/api/device-management';

// The device create/edit form — the generic instance form (token/name/description
// + required type picker) wired to the device APIs. Used by the create drawer on
// the devices list and the device detail page. Its noun-bearing prose resolves
// from the `entities` catalog under the `device` family prefix.
export function DeviceForm({
  device,
  onDone,
}: {
  device?: Device;
  onDone: (message: string) => void;
}) {
  return (
    <RegistryInstanceForm
      entity={device}
      i18nKey="device"
      entityType="device"
      checkAvailability={(token) => getDevice(token).then((d) => d === null)}
      defaultTypeToken={device?.deviceType.token}
      loadTypes={() => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results)}
      create={(req) =>
        createDevice({
          token: req.token,
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
        })
      }
      update={(token, req) =>
        // A partial update: this form edits the name, the description and the type,
        // so it sends exactly those. externalId — the handle whatever system
        // provisioned the device correlates it by — and metadata survive by being
        // ABSENT. Under the full replace this replaces, renaming a device here
        // deleted both, with a success toast either way.
        updateDevice(token, {
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  );
}
