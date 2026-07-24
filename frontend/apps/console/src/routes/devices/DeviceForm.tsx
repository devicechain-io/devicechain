// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
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
// the devices list and the device detail page.
export function DeviceForm({
  device,
  onDone,
}: {
  device?: Device;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation('devices');
  return (
    <RegistryInstanceForm
      entity={device}
      singular={t('deviceSingular')}
      typeLabel={t('deviceTypeLabel')}
      typeSingular={t('deviceTypeSingular')}
      tokenPlaceholder={t('tokenPlaceholder')}
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
        updateDevice(token, {
          token: req.token,
          name: req.name,
          description: req.description,
          deviceTypeToken: req.typeToken,
        })
      }
      onDone={onDone}
    />
  );
}
