// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Versions tab of a device profile (ADR-045 slice c/d). A profile is versioned
// as one unit: publishing freezes the current draft (all metric, command, and alarm
// definitions together) into an immutable version, and a device resolves the
// profile's active PUBLISHED version — so draft edits in the other tabs take effect
// only when published here. Rollback re-points the active version at an earlier one
// without touching the draft.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Rocket, RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { FormDrawer } from '@/components/registry';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { Textarea, errMessage, useReload } from '@/routes/common';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import {
  listDeviceProfileVersions,
  publishDeviceProfile,
  rollbackDeviceProfile,
} from '@/lib/api/device-management';

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function VersionsPanel({
  profileToken,
  activeVersion,
  deviceTypeCount,
  onChanged,
}: {
  profileToken: string;
  /** The profile's currently-active published version, or null if never published. */
  activeVersion: number | null;
  /** How many device types adopt this profile — publishing affects all of them. */
  deviceTypeCount: number;
  /** Refresh the parent detail so the active-version badge updates after a change. */
  onChanged: () => void;
}) {
  const { t } = useTranslation(['deviceProfiles', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => listDeviceProfileVersions(profileToken), [profileToken, version]);
  const [publishing, setPublishing] = useState(false);
  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [rolling, setRolling] = useState<ReadonlySet<number>>(() => new Set());

  const versions = data ?? [];

  const doPublish = async () => {
    setBusy(true);
    try {
      const v = await publishDeviceProfile(profileToken, label.trim() || undefined, description.trim() || undefined);
      toast(t('deviceProfiles:versionPublishedToast', { version: v }));
      setPublishing(false);
      setLabel('');
      setDescription('');
      reload();
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const doRollback = async (v: number) => {
    if (
      !(await confirm({
        title: t('deviceProfiles:versionRollbackButton'),
        description: t('deviceProfiles:versionRollbackConfirm', { version: v }),
        confirmLabel: t('deviceProfiles:versionRollbackButton'),
        destructive: false,
      }))
    )
      return;
    setRolling((s) => new Set(s).add(v));
    try {
      await rollbackDeviceProfile(profileToken, v);
      toast(t('deviceProfiles:versionRolledBackToast', { version: v }));
      reload();
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setRolling((s) => {
        const n = new Set(s);
        n.delete(v);
        return n;
      });
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="max-w-prose space-y-1 text-sm">
          {activeVersion == null ? (
            <p className="font-medium text-amber-600 dark:text-amber-500">
              {t('deviceProfiles:versionNotPublished')}
            </p>
          ) : (
            <p>
              {t('deviceProfiles:versionActiveLabel')} <span className="font-semibold tabular-nums">{activeVersion}</span>
            </p>
          )}
          <p className="text-muted-foreground">{t('deviceProfiles:versionPublishExplain')}</p>
          {deviceTypeCount > 1 && (
            <p className="text-amber-600 dark:text-amber-500">
              {t('deviceProfiles:versionUsedByWarning', { count: deviceTypeCount })}
            </p>
          )}
        </div>
        {canWrite && (
          <Button size="sm" onClick={() => setPublishing(true)} className="shrink-0">
            <Rocket size={16} /> {t('deviceProfiles:versionPublishButton')}
          </Button>
        )}
      </div>

      <FormDrawer
        open={publishing}
        onOpenChange={(open) => {
          setPublishing(open);
          // Discard an abandoned draft's label/description so the next open is clean.
          if (!open) {
            setLabel('');
            setDescription('');
          }
        }}
        title={t('deviceProfiles:versionPublishDrawerTitle')}
        description={t('deviceProfiles:versionPublishDrawerDescription')}
      >
        <div className="space-y-4">
          <FormField label={t('deviceProfiles:versionLabelFieldLabel')} htmlFor="v-label" description={t('deviceProfiles:versionLabelHint')}>
            <Input
              id="v-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('deviceProfiles:versionLabelPlaceholder')}
            />
          </FormField>
          <FormField label={t('common:colDescription')} htmlFor="v-desc" description={t('deviceProfiles:versionDescHint')}>
            <Textarea id="v-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </FormField>
          <Button onClick={doPublish} loading={busy}>
            {t('deviceProfiles:versionPublishButton')}
          </Button>
        </div>
      </FormDrawer>

      {loading && !data ? (
        <LoadingState description={t('deviceProfiles:versionLoading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : versions.length === 0 ? (
        <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
          {t('deviceProfiles:versionEmpty')}
        </p>
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('deviceProfiles:versionColVersion')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deviceProfiles:versionLabelFieldLabel')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deviceProfiles:versionColPublished')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('deviceProfiles:versionColBy')}</DataTableHeaderCell>
            {canWrite && <DataTableHeaderCell className="text-right">{t('common:colActions')}</DataTableHeaderCell>}
          </DataTableHead>
          <DataTableBody>
            {versions.map((v) => (
              <DataTableRow key={v.version}>
                <DataTableCell>
                  <span className="tabular-nums">{v.version}</span>
                  {v.version === activeVersion && (
                    <span className="ml-2 rounded bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                      {t('deviceProfiles:versionActiveBadge')}
                    </span>
                  )}
                </DataTableCell>
                <DataTableCell>{v.label || <span className="text-muted-foreground">—</span>}</DataTableCell>
                <DataTableCell className="whitespace-nowrap text-muted-foreground">{fmtTime(v.publishedAt)}</DataTableCell>
                <DataTableCell className="text-muted-foreground">{v.publishedBy || '—'}</DataTableCell>
                {canWrite && (
                  <DataTableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => doRollback(v.version)}
                      loading={rolling.has(v.version)}
                      disabled={v.version === activeVersion}
                    >
                      <RotateCcw size={14} /> {t('deviceProfiles:versionRollbackButton')}
                    </Button>
                  </DataTableCell>
                )}
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
