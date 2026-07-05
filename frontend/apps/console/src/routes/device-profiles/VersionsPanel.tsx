// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Versions tab of a device profile (ADR-045 slice c/d). A profile is versioned
// as one unit: publishing freezes the current draft (all metric, command, and alarm
// definitions together) into an immutable version, and a device resolves the
// profile's active PUBLISHED version — so draft edits in the other tabs take effect
// only when published here. Rollback re-points the active version at an earlier one
// without touching the draft.

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
      toast(`Published version ${v}`);
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
        title: 'Roll back',
        description: `Make version ${v} the active version devices resolve? The draft is unchanged.`,
        confirmLabel: 'Roll back',
        destructive: false,
      }))
    )
      return;
    setRolling((s) => new Set(s).add(v));
    try {
      await rollbackDeviceProfile(profileToken, v);
      toast(`Rolled back to version ${v}`);
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
              Not published yet — devices resolve nothing on this profile until you publish.
            </p>
          ) : (
            <p>
              Active version: <span className="font-semibold tabular-nums">{activeVersion}</span>
            </p>
          )}
          <p className="text-muted-foreground">
            Publishing snapshots the current draft — all metrics, commands, and alarm rules — into a
            new version. Devices resolve the active version, so draft edits take effect only when
            published.
          </p>
          {deviceTypeCount > 1 && (
            <p className="text-amber-600 dark:text-amber-500">
              Used by {deviceTypeCount} device types — publishing changes what all of them resolve.
            </p>
          )}
        </div>
        {canWrite && (
          <Button size="sm" onClick={() => setPublishing(true)} className="shrink-0">
            <Rocket size={16} /> Publish
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
        title="Publish profile"
        description="Freeze the current draft into a new version and make it the version devices resolve."
      >
        <div className="space-y-4">
          <FormField label="Label" htmlFor="v-label" description="Optional, e.g. a version name.">
            <Input id="v-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="v1.0" />
          </FormField>
          <FormField label="Description" htmlFor="v-desc" description="Optional notes about this version.">
            <Textarea id="v-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
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
                  {v.version === activeVersion && (
                    <span className="ml-2 rounded bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                      active
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
                      <RotateCcw size={14} /> Roll back
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
