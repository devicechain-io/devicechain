// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Versions panel of a connector (ADR-060 C5 / ADR-039 versioning). Publishing
// freezes the current saved draft {type, config} into an immutable version; a REACT
// `publish` action dispatches through the connector's LATEST published version.
// Rollback re-drafts an earlier version's {type, config} back onto the draft (the
// credential is not versioned — it stays as configured); you then publish again to
// make it the version that dispatches.

import { useState } from 'react';
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
import {
  listConnectorVersions,
  publishConnector,
  rollbackConnector,
} from '@/lib/api/connectors';

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function ConnectorVersionsPanel({
  token,
  canWrite,
  dirty,
  expectedUpdatedAt,
  onPublished,
  onRolledBack,
}: {
  token: string;
  canWrite: boolean;
  /** True when the draft has unsaved edits — publishing is blocked until saved. */
  dirty: boolean;
  /** The saved draft's updatedAt, the optimistic-concurrency precondition for publish. */
  expectedUpdatedAt: string | null;
  /** Refresh trigger after a publish. */
  onPublished: () => void;
  /** Re-baseline the editor after a rollback re-drafts an earlier version. */
  onRolledBack: (draft: { type: string; config: string; updatedAt: string | null }) => void;
}) {
  const { toast } = useToast();
  const confirm = useConfirm();
  const [reloadKey, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listConnectorVersions(token),
    [token, reloadKey],
  );
  const [publishing, setPublishing] = useState(false);
  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [rolling, setRolling] = useState<ReadonlySet<number>>(() => new Set());

  const versions = data ?? [];
  // The newest version is the one a `publish` action dispatches through.
  const latest = versions.length > 0 ? versions[0].version : null;

  const doPublish = async () => {
    setBusy(true);
    try {
      const { version } = await publishConnector(token, {
        label: label.trim() || undefined,
        description: description.trim() || undefined,
        expectedUpdatedAt,
      });
      toast(`Published version ${version}`);
      setPublishing(false);
      setLabel('');
      setDescription('');
      reload();
      onPublished();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const doRollback = async (v: number) => {
    if (
      !(await confirm({
        title: 'Restore version',
        description: `Copy version ${v}'s configuration back into the draft? Your current unsaved draft edits will be replaced. Publish afterward to make it the version rules dispatch through.`,
        confirmLabel: 'Restore to draft',
      }))
    )
      return;
    setRolling((s) => new Set(s).add(v));
    try {
      const draft = await rollbackConnector(token, v);
      toast(`Restored version ${v} to the draft`);
      onRolledBack(draft);
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
          {latest == null ? (
            <p className="font-medium text-amber-600 dark:text-amber-500">
              Not published yet — a rule can’t dispatch through this connector until you publish it.
            </p>
          ) : (
            <p>
              Rules dispatch through version{' '}
              <span className="font-semibold tabular-nums">{latest}</span> (the latest published).
            </p>
          )}
          <p className="text-muted-foreground">
            Publishing freezes the saved draft into a new immutable version. Save your draft edits
            first, then publish.
          </p>
        </div>
        {canWrite && (
          <Button
            size="sm"
            onClick={() => setPublishing(true)}
            disabled={dirty}
            title={dirty ? 'Save your draft changes before publishing' : undefined}
            className="shrink-0"
          >
            <Rocket size={16} /> Publish
          </Button>
        )}
      </div>

      <FormDrawer
        open={publishing}
        onOpenChange={(open) => {
          setPublishing(open);
          if (!open) {
            setLabel('');
            setDescription('');
          }
        }}
        title="Publish connector"
        description="Freeze the current draft into a new immutable version."
      >
        <div className="space-y-4">
          <FormField label="Label" htmlFor="cv-label" description="Optional, e.g. a version name.">
            <Input id="cv-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="v1.0" />
          </FormField>
          <FormField label="Description" htmlFor="cv-desc" description="Optional notes about this version.">
            <Textarea id="cv-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </FormField>
          <Button onClick={doPublish} loading={busy}>
            Publish
          </Button>
        </div>
      </FormDrawer>

      {loading && !data ? (
        <LoadingState description="Loading versions…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : versions.length === 0 ? (
        <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
          No versions published yet.
        </p>
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Version</DataTableHeaderCell>
            <DataTableHeaderCell>Label</DataTableHeaderCell>
            <DataTableHeaderCell>Published</DataTableHeaderCell>
            <DataTableHeaderCell>By</DataTableHeaderCell>
            {canWrite && <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>}
          </DataTableHead>
          <DataTableBody>
            {versions.map((v) => (
              <DataTableRow key={v.version}>
                <DataTableCell>
                  <span className="tabular-nums">{v.version}</span>
                  {v.version === latest && (
                    <span className="ml-2 rounded bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                      live
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
                    >
                      <RotateCcw size={14} /> Restore
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
