// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, Trash2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listDashboards,
  createDashboard,
  deleteDashboard,
  getDashboard,
} from '@/lib/api/dashboards';
import { formatTime } from '@/lib/utils';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
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
  const { t } = useTranslation('dashboards');
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
        title: t('deleteTitle'),
        description: t('deleteConfirm', { token }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await deleteDashboard(token);
      toast(t('deleted', { token }));
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
      title={t('title')}
      description={t('description')}
      banner="dashboard"
      action={
        <Button onClick={() => setCreating(true)}>
          <Plus size={16} /> {t('newDashboard')}
        </Button>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title={t('newDashboard')}>
        <DashboardCreateForm
          onDone={(token) => {
            toast(t('created', { token }));
            setCreating(false);
            reload();
            navigate(`/dashboards/${encodeURIComponent(token)}`);
          }}
        />
      </FormDrawer>
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colDescription')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colUpdated')}</DataTableHeaderCell>
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
                      <Trash2 size={14} /> {t('delete')}
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
  const { t } = useTranslation('dashboards');
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
      <FormField label={t('common:colToken')} htmlFor="d-token">
        <TokenField
          id="d-token"
          entityType="dashboard"
          value={token}
          onChange={setToken}
          seed={name}
          placeholder={t('createTokenPlaceholder')}
          checkAvailability={(token) => getDashboard(token).then((d) => d === null)}
        />
      </FormField>
      <FormField label={t('common:colName')} htmlFor="d-name">
        <Input id="d-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="d-description">
        <Textarea
          id="d-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !token.trim()}>
          {t('createDashboard')}
        </Button>
      </div>
    </div>
  );
}
