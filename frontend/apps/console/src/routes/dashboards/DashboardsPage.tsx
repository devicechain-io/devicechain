// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listDashboards,
  createDashboard,
  deleteDashboard,
} from '@/lib/api/dashboards';
import { formatTime } from '@/lib/utils';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { Textarea, errMessage, rowLinkProps, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

const pageSize = 20;

export default function DashboardsPage() {
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listDashboards({ pageNumber, pageSize }),
    [pageNumber, version],
  );

  const results = data?.results ?? [];

  const remove = async (token: string) => {
    if (
      !(await confirm({
        title: 'Delete dashboard',
        description: `Delete “${token}”? This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteDashboard(token);
      toast(`Dashboard “${token}” deleted`);
      // Removing the last row of a page > 1 would strand the user on an empty
      // page (rendered as the misleading "no dashboards" state); step back.
      if (results.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      else reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Dashboards"
      description="Live dashboards for this tenant"
      banner="dashboard"
      action={
        <Button onClick={() => setCreating(true)}>
          <Plus size={16} /> New dashboard
        </Button>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title="New dashboard">
        <DashboardCreateForm
          onDone={(token) => {
            toast(`Dashboard “${token}” created`);
            setCreating(false);
            reload();
            navigate(`/dashboards/${encodeURIComponent(token)}`);
          }}
        />
      </FormDrawer>
      {loading ? (
        <LoadingState description="Loading dashboards…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description="No dashboards yet." />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Description</DataTableHeaderCell>
              <DataTableHeaderCell>Updated</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((dashboard) => (
                <DataTableRow
                  key={dashboard.token}
                  {...rowLinkProps(() =>
                    navigate(`/dashboards/${encodeURIComponent(dashboard.token)}`),
                  )}
                >
                  <DataTableCell className="font-medium text-foreground">
                    {dashboard.name || dashboard.token}
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {dashboard.description || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">
                    {formatTime(dashboard.updatedAt)}
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    {/* This cell hosts an interactive control, so it must swallow
                        the row's click/keyboard activation rather than navigate. */}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={(e) => {
                        e.stopPropagation();
                        void remove(dashboard.token);
                      }}
                      onKeyDown={(e) => e.stopPropagation()}
                    >
                      <Trash2 size={14} /> Delete
                    </Button>
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          <Pagination
            pageNumber={pageNumber}
            pageSize={pageSize}
            pagination={data!.pagination}
            onPageChange={setPageNumber}
            className="mt-4"
          />
        </>
      )}
    </PageShell>
  );
}

// The dashboard create form — a plain token/name/description record (unlike the
// registry instance forms, a dashboard has no classifying type). Seeds an empty
// canonical definition server-side (see createDashboard).
function DashboardCreateForm({ onDone }: { onDone: (token: string) => void }) {
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const t = token.trim();
      const { token: created } = await createDashboard({
        token: t,
        name: name.trim() || undefined,
        description: description.trim() || undefined,
      });
      onDone(created);
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label="Token"
        htmlFor="d-token"
        description="Unique id for this dashboard; it cannot change later."
      >
        <Input
          id="d-token"
          value={token}
          placeholder="ops-overview"
          onChange={(e) => setToken(e.target.value)}
        />
      </FormField>
      <FormField label="Name" htmlFor="d-name">
        <Input id="d-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="d-description">
        <Textarea
          id="d-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !token.trim()}>
          Create dashboard
        </Button>
      </div>
    </div>
  );
}
