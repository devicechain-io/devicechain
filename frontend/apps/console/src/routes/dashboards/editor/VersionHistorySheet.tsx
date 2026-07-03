// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// VersionHistorySheet — the dashboard versioning surface (ADR-039 PR G). A drawer
// that lets an author freeze the current (saved) draft into an immutable version
// and roll the draft back to any published version. Publishing snapshots the
// SERVER draft, so it's disabled while there are unsaved edits — the author saves
// first. Rollback replaces the draft server-side; the workspace re-baselines from
// the returned definition (onRolledBack) rather than reloading the page.

import { useState } from 'react';
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
import {
  listDashboardVersions,
  publishDashboard,
  rollbackDashboard,
  CONFLICT_MARKER,
  type DashboardVersion,
} from '@/lib/api/dashboards';

export interface VersionHistorySheetProps {
  token: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
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
}

export function VersionHistorySheet({
  token,
  open,
  onOpenChange,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
}: VersionHistorySheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader className="mb-6">
          <SheetTitle>Version history</SheetTitle>
          <SheetDescription>
            Publish the current draft as an immutable version, or roll the draft back to a
            previous one. History is append-only.
          </SheetDescription>
        </SheetHeader>
        {/* Rendered inside SheetContent, which Radix unmounts when closed, so the
            version list is fetched fresh each time the drawer opens. */}
        {open && (
          <VersionHistoryBody
            token={token}
            dirty={dirty}
            saving={saving}
            canWrite={canWrite}
            expectedUpdatedAt={expectedUpdatedAt}
            onRolledBack={onRolledBack}
            onClose={() => onOpenChange(false)}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function VersionHistoryBody({
  token,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
  onClose,
}: {
  token: string;
  dirty: boolean;
  saving: boolean;
  canWrite: boolean;
  expectedUpdatedAt: string | null;
  onRolledBack: (result: { definition: string; updatedAt: string | null }) => void;
  onClose: () => void;
}) {
  const { toast } = useToast();
  const confirm = useConfirm();

  const [refreshKey, setRefreshKey] = useState(0);
  const { data, loading, error } = useQuery(() => listDashboardVersions(token), [token, refreshKey]);

  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);

  const publish = async () => {
    setBusy(true);
    try {
      const { version } = await publishDashboard(token, { label, description, expectedUpdatedAt });
      toast(`Published version ${version}`);
      setLabel('');
      setDescription('');
      setRefreshKey((k) => k + 1);
    } catch (err) {
      const raw = errMessage(err);
      toast(
        raw.includes(CONFLICT_MARKER)
          ? 'This dashboard changed elsewhere. Reload the page before publishing.'
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
        title: `Roll back to version ${v.version}?`,
        description:
          'This replaces the current draft with that version. The current draft — including saved changes you have not published — will be lost.',
        confirmLabel: 'Roll back',
      }))
    )
      return;
    setBusy(true);
    try {
      const result = await rollbackDashboard(token, v.version);
      onRolledBack(result);
      toast(`Rolled back to version ${v.version}`);
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
          <div className="text-sm font-semibold">Publish current draft</div>
          <FormField label="Label" description="Optional — e.g. a release name or v1.2.0.">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="v1.0.0" />
          </FormField>
          <FormField label="Description">
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What changed in this version?"
            />
          </FormField>
          {dirty ? (
            <p className="text-sm text-muted-foreground">Save your changes before publishing.</p>
          ) : null}
          <Button size="sm" onClick={publish} loading={busy} disabled={busy || dirty || saving}>
            Publish version
          </Button>
        </div>
      )}

      <div>
        <div className="mb-2 text-sm font-semibold">Published versions</div>
        {loading ? (
          <LoadingState description="Loading versions…" />
        ) : error ? (
          <ErrorState description={error} />
        ) : !data || data.length === 0 ? (
          <EmptyState description="No versions published yet." />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Version</DataTableHeaderCell>
              <DataTableHeaderCell>Published</DataTableHeaderCell>
              <DataTableHeaderCell>By</DataTableHeaderCell>
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
                        <RotateCcw size={13} /> Roll back
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
