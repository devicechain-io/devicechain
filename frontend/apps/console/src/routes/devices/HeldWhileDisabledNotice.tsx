// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { cancelDeviceCommands, getDeviceCommandHold } from '@/lib/api/command-delivery';

// What the platform is doing with the commands already queued for a DISABLED device: nothing,
// on purpose. They are neither sent nor cancelled; they are held until the device is enabled
// again (then they go out, oldest first) or until each one's own expiry lapses it. This notice
// says so, with the count, and offers the one action that is the operator's to take: discard
// them.
//
// 🔴 THE COUNT IS THE SERVICE'S, NOT A COUNT OF THE ROWS ON SCREEN. The history table below is
// one page of a device's whole record and mixes finished commands in; the number that matters
// is what the platform is holding right now, and only the service that holds it can say.
//
// 🔴 IT SHOWS NOTHING WHEN IT CANNOT BE SURE. A failed read, a device that is enabled, or a
// hold of zero all render no notice at all: a banner asserting a hold the platform is not
// keeping would be worse than a missing one, and a missing one costs the operator a glance at
// the history table that already lists every command's status.
export function HeldWhileDisabledNotice({
  deviceToken,
  reloadKey,
  onCancelled,
}: {
  deviceToken: string;
  reloadKey: number;
  onCancelled: () => void;
}) {
  const { t } = useTranslation('devices');
  const { claims } = useAuth();
  const { toast } = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);

  const { data, error } = useQuery(() => getDeviceCommandHold(deviceToken), [deviceToken, reloadKey]);
  const canWrite = hasAuthority(claims, 'command:write');

  if (error || !data || !data.disabled || data.heldCount < 1) return null;
  const count = data.heldCount;

  const cancelAll = async () => {
    const confirmed = await confirm({
      title: t('cancelHeldConfirmTitle'),
      description: t('cancelHeldConfirmBody', { count }),
      confirmLabel: t('cancelHeldConfirmAction'),
      // Not the default "Cancel": on this dialog "Cancel" names the thing being confirmed, so
      // a dismiss button reading it would be ambiguous.
      cancelLabel: t('cancelHeldDismiss'),
    });
    if (!confirmed) return;
    setBusy(true);
    try {
      const result = await cancelDeviceCommands(deviceToken);
      toast(t('heldCommandsCancelled', { count: result.cancelled }));
      onCancelled();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      role="status"
      data-testid="held-while-disabled"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/40 bg-warning/10 px-4 py-3 text-sm"
    >
      <div className="space-y-0.5">
        <div className="font-medium text-foreground">{t('heldWhileDisabled', { count })}</div>
        <div className="text-muted-foreground">{t('heldWhileDisabledHint')}</div>
      </div>
      {canWrite && (
        <Button variant="outline" size="sm" loading={busy} disabled={busy} onClick={cancelAll}>
          {t('cancelAllHeld')}
        </Button>
      )}
    </div>
  );
}
