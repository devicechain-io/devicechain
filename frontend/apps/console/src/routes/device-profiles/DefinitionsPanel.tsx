// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A profile-scoped definition editor rendered inside a device-profile detail tab
// (ADR-045 slice d). It is the same list → create/edit-drawer → delete flow the
// registry kit gives top-level entities, but embedded in a tab and scoped to one
// profile, and it drives all three definition kinds (metrics, commands, alarm
// rules) from one generic component — each kind supplies its own columns + form.
//
// Edits here are DRAFT edits: a device resolves the profile's active PUBLISHED
// version, so changes take effect only when the profile is published (see the
// Versions tab). The panel does not say so itself — the Versions tab owns that
// message — but that is why saving a definition does not change live behaviour.

import { useState, type ReactNode } from 'react';
import { Plus, Pencil, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
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
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { FormDrawer } from '@/components/registry';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { cap } from '@/components/registry/forms';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';

export interface DefinitionColumn<TDef> {
  header: string;
  cell: (d: TDef) => ReactNode;
}

export function DefinitionsPanel<TDef extends { id: string; token: string }>({
  profileToken,
  singular,
  description,
  load,
  columns,
  renderForm,
  remove,
  removeConfirm,
}: {
  profileToken: string;
  /** Lowercase singular, e.g. "metric". */
  singular: string;
  description: string;
  load: (profileToken: string) => Promise<TDef[]>;
  columns: DefinitionColumn<TDef>[];
  /** The create/edit form; the profile token is bound by the caller. */
  renderForm: (entity: TDef | undefined, onDone: (message: string) => void) => ReactNode;
  remove: (token: string) => Promise<unknown>;
  removeConfirm: (d: TDef) => string;
}) {
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => load(profileToken), [profileToken, version]);
  const [drawer, setDrawer] = useState<{ open: boolean; entity?: TDef }>({ open: false });
  const [removing, setRemoving] = useState<ReadonlySet<string>>(() => new Set());

  const items = data ?? [];

  const onDone = (message: string) => {
    toast(message);
    setDrawer({ open: false });
    reload();
  };

  const del = async (d: TDef) => {
    if (!(await confirm({ title: `Delete ${singular}`, description: removeConfirm(d), confirmLabel: 'Delete' })))
      return;
    setRemoving((s) => new Set(s).add(d.token));
    try {
      await remove(d.token);
      toast(`${cap(singular)} “${d.token}” deleted`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setRemoving((s) => {
        const n = new Set(s);
        n.delete(d.token);
        return n;
      });
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{description}</p>
        {canWrite && (
          <Button size="sm" onClick={() => setDrawer({ open: true })} className="shrink-0">
            <Plus size={16} /> New {singular}
          </Button>
        )}
      </div>

      <FormDrawer
        open={drawer.open}
        onOpenChange={(open) => setDrawer((s) => ({ ...s, open }))}
        title={`${drawer.entity ? 'Edit' : 'New'} ${singular}`}
      >
        {/* Mount the form only while open so each open starts from fresh state. */}
        {drawer.open && renderForm(drawer.entity, onDone)}
      </FormDrawer>

      {loading && !data ? (
        <LoadingState description={`Loading ${singular} definitions…`} />
      ) : error ? (
        <ErrorState description={error} />
      ) : items.length === 0 ? (
        <p className="rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
          No {singular} definitions yet.
          {canWrite && ` Use “New ${singular}” to add one.`}
        </p>
      ) : (
        <DataTable>
          <DataTableHead>
            {columns.map((c) => (
              <DataTableHeaderCell key={c.header}>{c.header}</DataTableHeaderCell>
            ))}
            {canWrite && <DataTableHeaderCell className="text-right">Actions</DataTableHeaderCell>}
          </DataTableHead>
          <DataTableBody>
            {items.map((d) => (
              <DataTableRow key={d.id}>
                {columns.map((c) => (
                  <DataTableCell key={c.header}>{c.cell(d)}</DataTableCell>
                ))}
                {canWrite && (
                  <DataTableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" size="sm" onClick={() => setDrawer({ open: true, entity: d })}>
                        <Pencil size={14} /> Edit
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => del(d)}
                        loading={removing.has(d.token)}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </div>
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
