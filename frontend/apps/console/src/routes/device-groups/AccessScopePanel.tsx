// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { errorCode } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { LoadingState } from '@/components/ui/loading-state';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import { errMessage, useReload } from '@/routes/common';
import {
  MAX_ACCESS_SCOPE_GROUPS,
  accessScopeEligibility,
  GROUP_MOVED_MARKER,
  getGroupAccess,
  publishEntityGroup,
  setGroupAccessScope,
} from '@/lib/api/device-management';

// AccessScopePanel is a device group's "Use as access scope" switch. Only a published dynamic
// group of devices can be one — its members are fixed by a published filter, and follow that
// version — so for any other group the panel says what would make it eligible rather than
// offering a switch the server would refuse. A dynamic group never published can be published
// from here: nothing can depend on a version that does not exist yet, so the first publish
// changes nothing anyone sees. Later publishes change what its members see, and are not
// offered here.
export function AccessScopePanel({ token }: { token: string }) {
  const { t } = useTranslation('members');
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'device:write');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const { data: group, loading, error } = useQuery(() => getGroupAccess(token), [token, version]);

  if (loading && !group) return <LoadingState description={t('groupAccessLoading')} />;
  if (error) return <ErrorState description={error} />;
  if (!group) return null;

  const eligibility = accessScopeEligibility(group);

  const toggle = async (enabled: boolean) => {
    if (
      !enabled &&
      !(await confirm({
        title: t('groupAccessDisableTitle'),
        description: t('groupAccessDisableConfirm', { token }),
        confirmLabel: t('groupAccessDisableTitle'),
      }))
    )
      return;
    setFormError(null);
    setBusy(true);
    try {
      await setGroupAccessScope(token, enabled);
      toast(t(enabled ? 'groupAccessEnabledToast' : 'groupAccessDisabledToast', { token }));
      reload();
    } catch (err) {
      setFormError(
        errorCode(err) === 'LIMIT_EXCEEDED' ? t('groupAccessLimit', { max: MAX_ACCESS_SCOPE_GROUPS }) : errMessage(err),
      );
    } finally {
      setBusy(false);
    }
  };

  const publish = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // The version this view was drawn from: a publish by someone else since then is refused
      // by the server instead of stacking a second version on top of theirs.
      const published = await publishEntityGroup(token, group.activeVersion ?? 0);
      toast(t('groupAccessPublishedToast', { token, version: published }));
      reload();
    } catch (err) {
      if (errMessage(err).includes(GROUP_MOVED_MARKER)) {
        setFormError(t('groupAccessPublishStale'));
        reload();
      } else {
        setFormError(errMessage(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('groupAccessTitle')} description={t('groupAccessDescription')}>
      <div className="space-y-4" data-shot="group-access-scope">
        {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
        {eligibility === 'eligible' ? (
          <>
            <label className="flex items-center gap-2 text-sm font-medium text-foreground">
              <Checkbox
                id="group-access-scope"
                checked={group.accessScope}
                disabled={!canWrite || busy}
                onCheckedChange={(checked) => void toggle(checked === true)}
              />
              {t('groupAccessToggle')}
            </label>
            <p className="text-sm text-muted-foreground">
              {group.accessScope ? t('groupAccessEnabled') : t('groupAccessDisabled')}{' '}
              {group.activeVersion != null && t('groupAccessVersion', { version: group.activeVersion })}
            </p>
          </>
        ) : (
          <>
            <p className="text-sm text-muted-foreground">
              {eligibility === 'notDevices'
                ? t('groupAccessNotDevices')
                : eligibility === 'static'
                  ? t('groupAccessStatic')
                  : t('groupAccessUnpublished')}
            </p>
            {eligibility === 'unpublished' && canWrite && (
              <Button size="sm" onClick={publish} loading={busy} disabled={busy}>
                {t('groupAccessPublish')}
              </Button>
            )}
          </>
        )}
      </div>
    </SectionPanel>
  );
}
