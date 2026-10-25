// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A device's self-registration provenance: the provisioning profile it is earmarked for and the
// profile whose registration issued its credential, with the operator's way back from a revoke
// (Reset provisioning). It renders NOTHING for a device that has neither, so a hand-made device
// carries no empty box.
//
// For members who may see the whole tenant only. Both facts come from fields the server refuses
// to a member limited to part of the tenant (an error, not null), so the page that hosts this
// panel does not mount it for one, and the read lives in its own document rather than on the
// shared device query that a scoped member also issues.

import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { RotateCcw } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { getDeviceProvisioning, resetDeviceProvisioning, type ProfileRef } from '@/lib/api/provisioning';

const profileLink = (p: ProfileRef) => `/provisioning-profiles/${encodeURIComponent(p.token)}`;

export function DeviceProvisioningPanel({ deviceToken, canWrite }: { deviceToken: string; canWrite: boolean }) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const { data } = useQuery(() => getDeviceProvisioning(deviceToken), [deviceToken, version]);
  // A failed read renders nothing rather than an error box on a page whose main job is elsewhere.
  if (!data || (!data.earmark && data.issued.length === 0)) return null;

  const reset = async () => {
    const names = data.issued.map((i) => i.profile.name || i.profile.token).join(', ');
    if (
      !(await confirm({
        title: t('resetProvisioningTitle'),
        description: t('resetProvisioningConfirm', { token: deviceToken, profile: names }),
        confirmLabel: t('resetProvisioningAction'),
      }))
    )
      return;
    try {
      await resetDeviceProvisioning(deviceToken);
      toast(t('resetProvisioningToast', { token: deviceToken }));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <SectionPanel title={t('provisioningTitle')} description={t('provisioningDescription')}>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm" data-shot="device-provisioning">
        {data.issued.map((i) => (
          <div key={i.credentialToken} className="contents">
            <dt className="text-muted-foreground">{t('provisionedByLabel')}</dt>
            <dd className="flex flex-wrap items-center gap-2 text-foreground">
              <Link className="text-primary underline-offset-2 hover:underline" to={profileLink(i.profile)}>
                {i.profile.name || i.profile.token}
              </Link>
              {!i.enabled && <Badge variant="warning">{t('provisioningRevoked')}</Badge>}
            </dd>
          </div>
        ))}
        {data.earmark && (
          <>
            <dt className="text-muted-foreground">{t('expectedProfileLabel')}</dt>
            <dd className="text-foreground">
              <Link className="text-primary underline-offset-2 hover:underline" to={profileLink(data.earmark)}>
                {data.earmark.name || data.earmark.token}
              </Link>
            </dd>
          </>
        )}
      </dl>
      {canWrite && data.issued.length > 0 && (
        <div className="mt-3">
          <Button variant="outline" size="sm" onClick={reset}>
            <RotateCcw size={14} /> {t('resetProvisioningAction')}
          </Button>
        </div>
      )}
    </SectionPanel>
  );
}
