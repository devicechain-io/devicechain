// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Ban, CircleCheck } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { setDeviceEnabled } from '@/lib/api/device-management';
import { errMessage } from '@/routes/common';

// DeviceEnabledAction disables an enabled device, or enables a disabled one, after the
// operator confirms. Disabling is reversible but immediate: the device's events stop
// being stored and commands to it are refused, so both directions ask first.
export function DeviceEnabledAction({
  device,
  onChanged,
}: {
  device: { token: string; enabled: boolean };
  onChanged: () => void;
}) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const enable = !device.enabled;

  const run = async () => {
    if (
      !(await confirm({
        title: t(enable ? 'enableDeviceTitle' : 'disableDeviceTitle'),
        description: t(enable ? 'enableDeviceConfirm' : 'disableDeviceConfirm', { token: device.token }),
        confirmLabel: t(enable ? 'enableDevice' : 'disableDevice'),
        destructive: !enable,
      }))
    )
      return;
    try {
      await setDeviceEnabled(device.token, enable);
      toast(t(enable ? 'deviceEnabledToast' : 'deviceDisabledToast', { token: device.token }));
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <Button variant="outline" size="sm" onClick={run}>
      {enable ? <CircleCheck size={14} /> : <Ban size={14} />} {t(enable ? 'enableDevice' : 'disableDevice')}
    </Button>
  );
}
