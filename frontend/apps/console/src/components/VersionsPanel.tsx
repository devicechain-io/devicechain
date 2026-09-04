// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The draft/publish/rollback panel, shared by every versioned tenant resource.
//
// The platform has this pattern in several places — a device profile, a dashboard, a
// connector, an entity group, and now an asset type's property contract — and the
// panel over it is the same panel every time: an active-version line, a publish
// drawer taking an optional label and description, and an append-only history whose
// rows each offer a rollback. Writing a second one for asset types would have made
// two screens that agree today and drift tomorrow.
//
// WHAT VARIES IS THE SENTENCES, NOT THE MECHANICS. "Devices resolve the active
// version" is true of a profile and meaningless for an asset type, so every sentence
// that names what the resource DOES arrives as an already-translated prop, while the
// column headers, buttons and toasts — which say nothing resource-specific — come
// from the shared `versions` namespace. That split is why the component takes copy at
// all instead of an i18n namespace name: a namespace prop would have meant every
// caller carrying a full copy of the generic strings, which is the duplication this
// exists to remove, one level down.

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
import { errMessage, useReload } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** One row of an append-only version history, as every versioned resource returns it. */
export interface PublishedVersion {
  version: number;
  label?: string | null;
  publishedAt: string;
  publishedBy?: string | null;
}

/**
 * The resource-specific sentences. Every one of them names what the resource does with
 * a published version, which is exactly what the shared half cannot know.
 */
export interface VersionsCopy {
  /** Shown in place of the active-version line when nothing has been published. */
  notPublished: string;
  /** The paragraph under it explaining what publishing does for THIS resource. */
  publishExplain: string;
  /** An extra warning line, e.g. "this is used by N other things". */
  notice?: string;
  drawerTitle: string;
  drawerDescription: string;
  /** Built per version because it names the number being rolled back to. */
  rollbackConfirm: (version: number) => string;
}

export function VersionsPanel<V extends PublishedVersion>({
  token,
  activeVersion,
  authority,
  copy,
  list,
  publish,
  rollback,
  onChanged,
}: {
  /** The parent resource's token — also the reload key, so switching rows refetches. */
  token: string;
  /** The currently-active published version, or null if never published. */
  activeVersion: number | null;
  /** The write authority a publish/rollback needs; the actions are hidden without it. */
  authority: string;
  copy: VersionsCopy;
  list: (token: string) => Promise<V[]>;
  publish: (token: string, label?: string, description?: string) => Promise<number>;
  rollback: (token: string, version: number) => Promise<unknown>;
  /** Refresh the parent detail so the active-version badge updates after a change. */
  onChanged: () => void;
}) {
  const { t } = useTranslation(['versions', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, authority);
  const { toast } = useToast();
  const confirm = useConfirm();
  const [reloadKey, reload] = useReload();
  const { data, loading, error } = useQuery(() => list(token), [token, reloadKey]);
  const [publishing, setPublishing] = useState(false);
  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [rolling, setRolling] = useState<ReadonlySet<number>>(() => new Set());

  const versions = data ?? [];

  const doPublish = async () => {
    setBusy(true);
    try {
      const v = await publish(token, label.trim() || undefined, description.trim() || undefined);
      toast(t('versions:publishedToast', { version: v }));
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
        title: t('versions:rollbackButton'),
        description: copy.rollbackConfirm(v),
        confirmLabel: t('versions:rollbackButton'),
        destructive: false,
      }))
    )
      return;
    setRolling((s) => new Set(s).add(v));
    try {
      await rollback(token, v);
      toast(t('versions:rolledBackToast', { version: v }));
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
            <p className="font-medium text-amber-600 dark:text-amber-500">{copy.notPublished}</p>
          ) : (
            <p>
              {t('versions:activeLabel')}{' '}
              <span className="font-semibold tabular-nums">{activeVersion}</span>
            </p>
          )}
          <p className="text-muted-foreground">{copy.publishExplain}</p>
          {copy.notice && <p className="text-amber-600 dark:text-amber-500">{copy.notice}</p>}
        </div>
        {canWrite && (
          <Button size="sm" onClick={() => setPublishing(true)} className="shrink-0">
            <Rocket size={16} /> {t('versions:publishButton')}
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
        title={copy.drawerTitle}
        description={copy.drawerDescription}
      >
        <div className="space-y-4">
          <FormField
            label={t('versions:labelFieldLabel')}
            htmlFor="v-label"
            description={t('versions:labelHint')}
          >
            <Input
              id="v-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('versions:labelPlaceholder')}
            />
          </FormField>
          <FormField
            label={t('common:colDescription')}
            htmlFor="v-desc"
            description={t('versions:descHint')}
          >
            <Textarea
              id="v-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </FormField>
          <Button onClick={doPublish} loading={busy}>
            {t('versions:publishButton')}
          </Button>
        </div>
      </FormDrawer>

      {loading && !data ? (
        <LoadingState description={t('versions:loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : versions.length === 0 ? (
        <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
          {t('versions:empty')}
        </p>
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('versions:colVersion')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('versions:labelFieldLabel')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('versions:colPublished')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('versions:colBy')}</DataTableHeaderCell>
            {canWrite && (
              <DataTableHeaderCell className="text-right">
                {t('common:colActions')}
              </DataTableHeaderCell>
            )}
          </DataTableHead>
          <DataTableBody>
            {versions.map((v) => (
              <DataTableRow key={v.version}>
                <DataTableCell>
                  <span className="tabular-nums">{v.version}</span>
                  {v.version === activeVersion && (
                    <span className="ml-2 rounded bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                      {t('versions:activeBadge')}
                    </span>
                  )}
                </DataTableCell>
                <DataTableCell>
                  {v.label || <span className="text-muted-foreground">—</span>}
                </DataTableCell>
                <DataTableCell className="whitespace-nowrap text-muted-foreground">
                  {fmtTime(v.publishedAt)}
                </DataTableCell>
                <DataTableCell className="text-muted-foreground">
                  {v.publishedBy || '—'}
                </DataTableCell>
                {canWrite && (
                  <DataTableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => doRollback(v.version)}
                      loading={rolling.has(v.version)}
                      disabled={v.version === activeVersion}
                    >
                      <RotateCcw size={14} /> {t('versions:rollbackButton')}
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
