// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How a batch's target was NAMED: an explicit device list, or an entity group resolved
// to its devices when the batch fired. Shared by the list and the detail header so the
// two cannot describe the same batch differently.
//
// The service writes only DEVICE_LIST and GROUP today, but targetKind crosses the wire as
// a plain string with no GraphQL enum behind it — so an unrecognized value renders
// VERBATIM rather than being coerced into one of the two we know. Showing a batch fired
// at something we do not recognize as a device-list batch would be a quiet lie about what
// a fleet actuation was aimed at.

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';

export function TargetKindBadge({ targetKind }: { targetKind: string }) {
  const { t } = useTranslation('commandBatches');
  const label =
    targetKind === 'DEVICE_LIST'
      ? t('targetDeviceList')
      : targetKind === 'GROUP'
        ? t('targetGroup')
        : targetKind;
  return <Badge variant="secondary">{label}</Badge>;
}
