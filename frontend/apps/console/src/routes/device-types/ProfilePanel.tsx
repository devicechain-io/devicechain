// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Profile tab of a device-type detail (ADR-045 slice d.2). A device type
// references at most one DeviceProfile — the reusable capability contract it
// adopts (metrics, commands, alarm rules). Authoring lives on the profile; this
// panel just chooses which profile the type uses: attach an existing one, create
// one named after the type, or detach. A type with no profile is capability-
// limited (honest, not an error) — its devices are still classified and shown,
// but carry no typed capabilities.

import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, ArrowRight, Plus, SlidersHorizontal, Unlink } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, typeCountLabel, useReload } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import {
  listDeviceProfiles,
  createDeviceProfile,
  updateDeviceType,
  deviceTypePreserved,
  type DeviceType,
} from '@/lib/api/device-management';

// The attached-profile shape carried on DeviceType.profile ({token,name,category}).
type AttachedProfile = NonNullable<DeviceType['profile']>;

export function ProfilePanel({
  entity,
  onChanged,
}: {
  entity: DeviceType;
  /** Refresh the parent detail so the attached profile updates after a change. */
  onChanged: () => void;
}) {
  const { t } = useTranslation(['devices', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const confirm = useConfirm();
  // The picker (and its adopting-type counts) reloads after each change so a
  // freshly created profile appears and shared counts stay accurate.
  const [version, reloadProfiles] = useReload();
  const { data, loading, error } = useQuery(
    () => listDeviceProfiles({ pageNumber: 1, pageSize: 1000 }),
    [version],
  );
  const profiles = data?.results ?? [];

  const [picking, setPicking] = useState(false);
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState<'attach' | 'detach' | 'create' | null>(null);

  // Optimistic override: on a successful change the attached card / amber notice
  // would otherwise briefly show the OLD state (which comes from the parent's
  // entity) while the parent refetches, contradicting the success toast. Hold the
  // new value here until entity.profile catches up, then drop back to entity.
  const [override, setOverride] = useState<{ profile: AttachedProfile | null } | null>(null);
  useEffect(() => {
    setOverride(null);
  }, [entity.profile?.token]);
  const attached = override ? override.profile : (entity.profile ?? null);

  // The attached profile's adopting-type count comes from the picker list (which
  // carries deviceTypeCount) rather than DeviceType.profile, so the device-type
  // list page doesn't pay a per-row count query.
  const attachedCount = attached
    ? profiles.find((p) => p.token === attached.token)?.deviceTypeCount
    : undefined;

  const options: ComboboxOption[] = profiles
    .filter((p) => p.token !== attached?.token)
    .map((p) => ({
      value: p.token,
      label: p.name ? `${p.name} (${p.token})` : p.token,
      description: [p.category || null, typeCountLabel(p.deviceTypeCount, t)].filter(Boolean).join(' · '),
    }));

  const closePicker = () => {
    setPicking(false);
    setSelected('');
  };

  // DeviceType update is full-replace; carry everything forward and override only
  // the profile ref (undefined detaches).
  const setProfile = (profileToken: string | undefined) =>
    updateDeviceType(entity.token, { ...deviceTypePreserved(entity), profileToken });

  const doAttach = async (token: string) => {
    setBusy('attach');
    try {
      await setProfile(token);
      toast(t('devices:typeProfileAttached', { token }));
      const picked = profiles.find((p) => p.token === token);
      setOverride({
        profile: { token, name: picked?.name ?? null, category: picked?.category ?? null },
      });
      closePicker();
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
      reloadProfiles();
    }
  };

  const doDetach = async () => {
    if (!attached) return;
    if (
      !(await confirm({
        title: t('devices:typeProfileDetachTitle'),
        description: t('devices:typeProfileDetachConfirm', { token: attached.token }),
        confirmLabel: t('devices:typeProfileDetachAction'),
      }))
    )
      return;
    setBusy('detach');
    try {
      await setProfile(undefined);
      toast(t('devices:typeProfileDetached'));
      setOverride({ profile: null });
      closePicker();
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
      reloadProfiles();
    }
  };

  const doCreateForType = async () => {
    setBusy('create');
    try {
      const created = await createDeviceProfile({
        token: `${entity.token}-profile`,
        name: entity.name ? `${entity.name} Profile` : undefined,
      });
      await setProfile(created.token);
      toast(t('devices:typeProfileCreatedAttached', { token: created.token }));
      setOverride({
        profile: { token: created.token, name: created.name ?? null, category: created.category ?? null },
      });
      closePicker();
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
      reloadProfiles();
    }
  };

  return (
    <div className="max-w-2xl space-y-4">
      <p className="max-w-prose text-sm text-muted-foreground">{t('devices:typeProfileIntro')}</p>

      {attached && !picking ? (
        <div className="space-y-3 rounded-md border p-4">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 space-y-1">
              <div className="flex items-center gap-2">
                <SlidersHorizontal size={16} className="shrink-0 text-muted-foreground" />
                <span className="font-medium">{attached.name || attached.token}</span>
              </div>
              <div className="font-mono text-xs text-muted-foreground">{attached.token}</div>
              {attached.category && (
                <div className="text-xs text-muted-foreground">
                  {t('devices:typeProfileCategory', { category: attached.category })}
                </div>
              )}
            </div>
            {canWrite && (
              <div className="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" onClick={() => setPicking(true)}>
                  {t('devices:typeProfileChange')}
                </Button>
                <Button variant="ghost" size="sm" onClick={doDetach} loading={busy === 'detach'}>
                  <Unlink size={14} /> {t('devices:typeProfileDetach')}
                </Button>
              </div>
            )}
          </div>
          {attachedCount != null && attachedCount > 1 && (
            <p className="text-xs text-amber-600 dark:text-amber-500">
              {t('devices:typeProfileShared', { count: attachedCount })}
            </p>
          )}
          <Link
            to={`/device-profiles/${attached.token}`}
            className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
          >
            {t('devices:typeProfileAuthorLink')} <ArrowRight size={14} />
          </Link>
        </div>
      ) : (
        <div
          className={cn(
            'space-y-4 rounded-md border p-4',
            !attached && 'border-amber-500/40 bg-amber-500/5',
          )}
        >
          {!attached && (
            <div className="space-y-1">
              <p className="flex items-center gap-2 text-sm font-medium text-amber-600 dark:text-amber-500">
                <AlertTriangle size={16} /> {t('devices:typeProfileCapabilityLimited')}
              </p>
              <p className="text-sm text-muted-foreground">
                {t('devices:typeProfileCapabilityLimitedBody')}
              </p>
            </div>
          )}
          {canWrite ? (
            <>
              {error && !data ? (
                <p className="text-sm text-destructive">{t('devices:typeProfileLoadError')}</p>
              ) : (
                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="flex-1 space-y-1.5">
                    <label htmlFor="attach-profile" className="text-xs font-medium text-muted-foreground">
                      {t('devices:typeProfileAttachLabel')}
                    </label>
                    <Combobox
                      id="attach-profile"
                      options={options}
                      value={selected}
                      onChange={setSelected}
                      placeholder={
                        loading ? t('devices:typeProfileLoadingProfiles') : t('devices:typeProfileSelectProfile')
                      }
                      emptyMessage={t('devices:typeProfileNoOtherProfiles')}
                      disabled={loading}
                    />
                  </div>
                  <Button
                    onClick={() => doAttach(selected)}
                    disabled={!selected}
                    loading={busy === 'attach'}
                  >
                    {t('devices:typeProfileAttach')}
                  </Button>
                </div>
              )}
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">{t('devices:typeProfileOr')}</span>
                <Button variant="outline" size="sm" onClick={doCreateForType} loading={busy === 'create'}>
                  <Plus size={14} /> {t('devices:typeProfileCreate')}
                </Button>
                {attached && (
                  <Button variant="ghost" size="sm" onClick={closePicker}>
                    {t('common:cancel')}
                  </Button>
                )}
              </div>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">{t('devices:typeProfileNoPermission')}</p>
          )}
        </div>
      )}
    </div>
  );
}
