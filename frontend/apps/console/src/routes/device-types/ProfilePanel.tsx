// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Profile tab of a device-type detail (ADR-045 slice d.2). A device type
// references at most one DeviceProfile — the reusable capability contract it
// adopts (metrics, commands, alarm rules). Authoring lives on the profile; this
// panel just chooses which profile the type uses: attach an existing one, create
// one named after the type, or detach. A type with no profile is capability-
// limited (honest, not an error) — its devices are still classified and shown,
// but carry no typed capabilities.

import { useState } from 'react';
import { Link } from 'react-router-dom';
import { AlertTriangle, ArrowRight, Plus, SlidersHorizontal, Unlink } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import {
  listDeviceProfiles,
  createDeviceProfile,
  updateDeviceType,
  deviceTypePreserved,
  type DeviceType,
} from '@/lib/api/device-management';

export function ProfilePanel({
  entity,
  onChanged,
}: {
  entity: DeviceType;
  /** Refresh the parent detail so the attached profile updates after a change. */
  onChanged: () => void;
}) {
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const confirm = useConfirm();
  // The picker (and its adopting-type counts) reloads after each change so a
  // freshly created profile appears and shared counts stay accurate.
  const [version, reloadProfiles] = useReload();
  const { data, loading } = useQuery(
    () => listDeviceProfiles({ pageNumber: 1, pageSize: 1000 }),
    [version],
  );
  const profiles = data?.results ?? [];

  const attached = entity.profile;
  // The attached profile's adopting-type count comes from the picker list (which
  // carries deviceTypeCount) rather than DeviceType.profile, so the device-type
  // list page doesn't pay a per-row count query.
  const attachedCount = attached
    ? profiles.find((p) => p.token === attached.token)?.deviceTypeCount
    : undefined;

  const [picking, setPicking] = useState(false);
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState<'attach' | 'detach' | 'create' | null>(null);

  const options: ComboboxOption[] = profiles
    .filter((p) => p.token !== attached?.token)
    .map((p) => ({
      value: p.token,
      label: p.name ? `${p.name} (${p.token})` : p.token,
      description: [
        p.category || null,
        p.deviceTypeCount === 0
          ? 'unused'
          : `used by ${p.deviceTypeCount} type${p.deviceTypeCount === 1 ? '' : 's'}`,
      ]
        .filter(Boolean)
        .join(' · '),
    }));

  // DeviceType update is full-replace; carry everything forward and override only
  // the profile ref (undefined detaches).
  const setProfile = (profileToken: string | undefined) =>
    updateDeviceType(entity.token, { ...deviceTypePreserved(entity), profileToken });

  const afterChange = () => {
    reloadProfiles();
    onChanged();
  };

  const doAttach = async (token: string) => {
    setBusy('attach');
    try {
      await setProfile(token);
      toast(`Attached profile “${token}”`);
      setPicking(false);
      setSelected('');
      afterChange();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
    }
  };

  const doDetach = async () => {
    if (!attached) return;
    if (
      !(await confirm({
        title: 'Detach profile',
        description: `Detach “${attached.token}” from this device type? Its devices keep working but lose their typed capabilities — no metric typing, commands, or alarms — until a profile is re-attached. The profile itself is not deleted.`,
        confirmLabel: 'Detach',
      }))
    )
      return;
    setBusy('detach');
    try {
      await setProfile(undefined);
      toast('Profile detached');
      setPicking(false);
      afterChange();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
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
      toast(`Created and attached profile “${created.token}”`);
      setPicking(false);
      afterChange();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="max-w-2xl space-y-4">
      <p className="max-w-prose text-sm text-muted-foreground">
        A profile is the reusable capability contract — metrics, commands, and alarm rules — that
        this device type adopts. Authoring lives on the profile; here you choose which one this type
        uses.
      </p>

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
                <div className="text-xs text-muted-foreground">Category: {attached.category}</div>
              )}
            </div>
            {canWrite && (
              <div className="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" onClick={() => setPicking(true)}>
                  Change
                </Button>
                <Button variant="ghost" size="sm" onClick={doDetach} loading={busy === 'detach'}>
                  <Unlink size={14} /> Detach
                </Button>
              </div>
            )}
          </div>
          {attachedCount != null && attachedCount > 1 && (
            <p className="text-xs text-amber-600 dark:text-amber-500">
              Shared by {attachedCount} device types — changes to its definitions affect all of them.
            </p>
          )}
          <Link
            to={`/device-profiles/${attached.token}`}
            className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
          >
            Author this profile <ArrowRight size={14} />
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
                <AlertTriangle size={16} /> Capability-limited — no profile attached
              </p>
              <p className="text-sm text-muted-foreground">
                Devices of this type are still classified and displayed, but report no typed metrics,
                accept no commands, and raise no alarms until a profile is attached.
              </p>
            </div>
          )}
          {canWrite ? (
            <>
              <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                <div className="flex-1 space-y-1.5">
                  <label htmlFor="attach-profile" className="text-xs font-medium text-muted-foreground">
                    Attach an existing profile
                  </label>
                  <Combobox
                    id="attach-profile"
                    options={options}
                    value={selected}
                    onChange={setSelected}
                    placeholder={loading ? 'Loading profiles…' : 'Select a profile…'}
                    emptyMessage="No other profiles."
                    disabled={loading}
                  />
                </div>
                <Button onClick={() => doAttach(selected)} disabled={!selected} loading={busy === 'attach'}>
                  Attach
                </Button>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">or</span>
                <Button variant="outline" size="sm" onClick={doCreateForType} loading={busy === 'create'}>
                  <Plus size={14} /> Create a profile for this type
                </Button>
                {attached && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      setPicking(false);
                      setSelected('');
                    }}
                  >
                    Cancel
                  </Button>
                )}
              </div>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">
              You don’t have permission to change this type’s profile.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
