// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// VersionHistorySheet — the dashboard versioning surface (ADR-039 PR G). A drawer
// that lets an author freeze the current (saved) draft into an immutable version
// and roll the draft back to any published version. Publishing snapshots the
// SERVER draft, so it's disabled while there are unsaved edits — the author saves
// first. Rollback replaces the draft server-side; the workspace re-baselines from
// the returned definition (onRolledBack) rather than reloading the page.
//
// Publishing also refuses a board whose widgets carry options the renderer does not
// read. 🔴 THE REFUSAL IS publishDashboard's, NOT THIS COMPONENT'S — everything here
// is the explanation: which widget, which option, and (for a leftover key, the one
// issue no config-panel control can reach) a way to remove it. A published version is
// immutable and is what an embedder mounts, so shipping one whose gauge has no scale
// or whose command button names no command is the one moment worth being strict at;
// the draft it was built from stays unrestricted, the same way a device profile's
// draft is inert until it is published.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { Textarea, errMessage } from '@/routes/common';
import { formatTime } from '@/lib/utils';
import { validateDefinitionOptions } from '@devicechain/widgets';
import { OPTION_ISSUE_MESSAGE_KEYS } from './optionIssues';
import type { DashboardDefinition } from '@devicechain/dashboards';
import {
  listDashboardVersions,
  publishDashboard,
  rollbackDashboard,
  CONFLICT_MARKER,
  INVALID_DEFINITION_MARKER,
  type DashboardVersion,
} from '@/lib/api/dashboards';

export interface VersionHistorySheetProps {
  token: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The definition that will be frozen: the last SAVED one, paired with the
  // expectedUpdatedAt below. Forwarded to publishDashboard, which validates it.
  saved: DashboardDefinition;
  // Publishing snapshots the saved server draft; block it while the editor has
  // unsaved edits so the author isn't surprised by an older snapshot.
  dirty: boolean;
  // A save in flight means the server draft (and its updatedAt) is mid-change —
  // block publish/rollback until it settles so they don't race it.
  saving: boolean;
  canWrite: boolean;
  // The editor's optimistic-concurrency baseline, forwarded to publish so it fails
  // if the server draft moved on since (another writer) rather than freezing it.
  expectedUpdatedAt: string | null;
  // Called after a successful rollback with the new draft so the workspace can
  // re-baseline its working/saved copies (and updatedAt) without a page reload.
  onRolledBack: (result: { definition: string; updatedAt: string | null }) => void;
  // Strips every option key no widget reads from the WORKING copy, leaving the
  // author an unsaved edit to review and save. The workspace owns the definition,
  // so the repair has to happen there; this drawer only offers it.
  onStripUnknownOptions: () => void;
}

export function VersionHistorySheet({
  token,
  open,
  onOpenChange,
  saved,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
  onStripUnknownOptions,
}: VersionHistorySheetProps) {
  const { t } = useTranslation(['dashboards', 'common']);
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader className="mb-6">
          <SheetTitle>{t('versionHistoryTitle')}</SheetTitle>
          <SheetDescription>{t('versionHistoryDescription')}</SheetDescription>
        </SheetHeader>
        {/* Rendered inside SheetContent, which Radix unmounts when closed, so the
            version list is fetched fresh each time the drawer opens. */}
        {open && (
          <VersionHistoryBody
            token={token}
            saved={saved}
            dirty={dirty}
            saving={saving}
            canWrite={canWrite}
            expectedUpdatedAt={expectedUpdatedAt}
            onRolledBack={onRolledBack}
            onStripUnknownOptions={onStripUnknownOptions}
            onClose={() => onOpenChange(false)}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function VersionHistoryBody({
  token,
  saved,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
  onStripUnknownOptions,
  onClose,
}: {
  token: string;
  saved: DashboardDefinition;
  dirty: boolean;
  saving: boolean;
  canWrite: boolean;
  expectedUpdatedAt: string | null;
  onRolledBack: (result: { definition: string; updatedAt: string | null }) => void;
  onStripUnknownOptions: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const { toast } = useToast();
  const confirm = useConfirm();

  const [refreshKey, setRefreshKey] = useState(0);
  const { data, loading, error } = useQuery(() => listDashboardVersions(token), [token, refreshKey]);

  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);

  // The option issues on the document that would be frozen — the SAVED definition,
  // never the working copy. While the editor is dirty those differ, and reporting the
  // working copy's issues would tell an author their board is fine (or broken) about a
  // document publish will not touch.
  const issues = useMemo(() => validateDefinitionOptions(saved), [saved]);
  const strippable = issues.some((i) => i.code === 'unknown');

  const publish = async () => {
    setBusy(true);
    try {
      const { version } = await publishDashboard(token, {
        definition: saved,
        label,
        description,
        expectedUpdatedAt,
      });
      toast(t('versionPublished', { version }));
      setLabel('');
      setDescription('');
      setRefreshKey((k) => k + 1);
    } catch (err) {
      const raw = errMessage(err);
      toast(
        raw.includes(CONFLICT_MARKER)
          ? t('versionPublishConflict')
          : raw.includes(INVALID_DEFINITION_MARKER)
            ? t('versionPublishInvalid')
            : raw,
        'error',
      );
    } finally {
      setBusy(false);
    }
  };

  const rollback = async (v: DashboardVersion) => {
    if (
      !(await confirm({
        title: t('versionRollbackConfirmTitle', { version: v.version }),
        description: t('versionRollbackConfirmDescription'),
        confirmLabel: t('versionRollback'),
      }))
    )
      return;
    setBusy(true);
    try {
      const result = await rollbackDashboard(token, v.version);
      onRolledBack(result);
      toast(t('versionRolledBack', { version: v.version }));
      onClose();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      {canWrite && (
        <div className="space-y-3 rounded-md border border-border p-4">
          <div className="text-sm font-semibold">{t('versionPublishHeading')}</div>
          <FormField label={t('versionLabelField')} description={t('versionLabelHint')}>
            <Input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('versionLabelPlaceholder')}
            />
          </FormField>
          <FormField label={t('common:colDescription')}>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('versionDescriptionPlaceholder')}
            />
          </FormField>
          {dirty ? (
            <p className="text-sm text-muted-foreground">{t('versionSaveBeforePublish')}</p>
          ) : null}
          {issues.length > 0 && (
            <div className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-3">
              <p className="text-sm font-medium">
                {t('versionPublishBlocked', { count: issues.length })}
              </p>
              <ul className="space-y-1 text-sm text-muted-foreground">
                {issues.map((issue) => (
                  // Keyed by widget + option + code: one widget can carry several
                  // issues, and the same option can be both missing and out of range
                  // on different widgets of the same type.
                  <li key={`${issue.widgetId}:${issue.key}:${issue.code}`}>
                    <span className="text-foreground">{issue.title || issue.widgetId}</span>
                    <span className="ml-1">({issue.widgetType})</span>
                    {/* The CODE is translated, not the schema's English message — see
                        optionIssues.ts. `key` is an option name, not user text. */}
                    <span className="ml-1">
                      — {t(OPTION_ISSUE_MESSAGE_KEYS[issue.code], { key: issue.key })}
                    </span>
                  </li>
                ))}
              </ul>
              {strippable && (
                <Button variant="outline" size="sm" onClick={onStripUnknownOptions} disabled={busy}>
                  {t('versionStripUnknownOptions')}
                </Button>
              )}
            </div>
          )}
          <Button
            size="sm"
            onClick={publish}
            loading={busy}
            disabled={busy || dirty || saving || issues.length > 0}
          >
            {t('versionPublishButton')}
          </Button>
        </div>
      )}

      <div>
        <div className="mb-2 text-sm font-semibold">{t('versionPublishedHeading')}</div>
        {loading ? (
          <LoadingState description={t('versionLoading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !data || data.length === 0 ? (
          <EmptyState description={t('versionEmpty')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('versionColumnVersion')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('versionColumnPublished')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('versionColumnBy')}</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {data.map((v) => (
                <DataTableRow key={v.version}>
                  <DataTableCell className="font-medium text-foreground">
                    <span className="font-mono">{v.version}</span>
                    {v.label ? <span className="ml-2 text-muted-foreground">{v.label}</span> : null}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {formatTime(v.publishedAt)}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{v.publishedBy || '—'}</DataTableCell>
                  <DataTableCell className="text-right">
                    {canWrite && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => rollback(v)}
                        disabled={busy || saving}
                      >
                        <RotateCcw size={13} /> {t('versionRollback')}
                      </Button>
                    )}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>
    </div>
  );
}
