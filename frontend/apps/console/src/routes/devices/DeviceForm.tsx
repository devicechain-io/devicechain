// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { RegistryInstanceForm } from '@/components/registry';
import {
  listDeviceTypes,
  createDevice,
  updateDevice,
  devicePreserved,
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
        // RegistryInstanceForm calls update only when editing, so `device` is set.
        // The update is a full replace: before devicePreserved, renaming a device
        // here deleted its externalId — the handle whatever system provisioned it
        // correlates it by — and its metadata, with a success toast either way.
        updateDevice(token, {
          ...devicePreserved(device!),
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  );
}
