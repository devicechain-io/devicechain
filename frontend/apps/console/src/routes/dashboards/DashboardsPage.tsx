// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Copy, Plus, Share2, Trash2, Upload } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { isScoped } from '@devicechain/client';
import { mayWrite } from '@/lib/scope';
import { ShareDashboardDrawer } from './ShareDashboardDrawer';
import { useAuth } from '@/auth/AuthProvider';
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
import { errMessage, rowLinkProps, useReload } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';
import { FormDrawer } from '@/components/registry';
import { DashboardImportDrawer, type ImportSource } from './import/DashboardImportDrawer';
import { DASHBOARD_TEMPLATES } from './import/templates';
import { defaultWidgetRegistry } from './editor/widgetRegistry';
import { type DashboardDefinitionError, migrateToSlots, parseDashboardDefinition, serializeDefinition } from '@devicechain/dashboards';
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
  // Clone reads the source's DRAFT and creates a dashboard, both of which need
  // dashboard:write -- the check the editor's own Clone item uses. A read-only member is
  // not offered a button that can only fail.
  const { claims } = useAuth();
  //
  // A member limited to part of the tenant (a customer user) is served only the published
  // dashboards shared with their customers, and may change none of them, so they are
  // offered no authoring at all — whatever their role says.
  const canWrite = mayWrite(claims, 'dashboard:write');
  const scoped = isScoped(claims);
  const [sharing, setSharing] = useState<{ token: string; name: string; published: boolean } | null>(null);
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  // The import drawer's input: pasted/uploaded JSON (no text), a template, or a clone.
  const [importing, setImporting] = useState<ImportSource | null>(null);
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

  // Clone reads the stored draft and hands it to the import flow, so a clone is checked,
  // given a new token and created as a draft exactly like any other import.
  const clone = async (token: string) => {
    try {
      const source = await getDashboard(token);
      if (!source) {
        toast(t('detailNotFound', { token }), 'error');
        return;
      }
      // Normalized through the editor's own parser so the clone is what the editor shows.
      const parsed = migrateToSlots(
        parseDashboardDefinition(JSON.parse(source.definition), { catalog: defaultWidgetRegistry().catalog }),
      );
      const name = source.name || parsed.title || token;
      setImporting({
        // Compact, as a save writes it: the pretty-printed export form can be larger than
        // the size limit for a board that is well inside it.
        text: serializeDefinition(parsed),
        name: t('cloneName', { name }),
        description: source.description ?? undefined,
      });
    } catch (err) {
      toast((err as DashboardDefinitionError | null)?.code === 'schemaTooNew' ? t('schemaTooNew') : errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={t('title')}
      description={scoped ? t('descriptionShared') : t('description')}
      banner="dashboard"
      action={
        canWrite && (
          <div className="flex items-center gap-2">
            <Button variant="outline" onClick={() => setImporting({})} data-shot="dashboard-import-button">
              <Upload size={16} /> {t('importDashboard')}
            </Button>
            <Button onClick={() => setCreating(true)} data-shot="new-button">
              <Plus size={16} /> {t('newDashboard')}
            </Button>
          </div>
        )
      }
    >
      {sharing && (
        <ShareDashboardDrawer
          token={sharing.token}
          name={sharing.name}
          published={sharing.published}
          onClose={() => setSharing(null)}
          onShared={(count) => {
            toast(count === 0 ? t('sharedWithNobody', { name: sharing.name }) : t('sharedToast', { name: sharing.name, count }));
            setSharing(null);
          }}
        />
      )}
      <DashboardImportDrawer
        // Remounts per source so a template/clone starts clean.
        key={importing?.text ?? 'paste'}
        open={importing !== null}
        onOpenChange={(open) => !open && setImporting(null)}
        title={t('importDashboard')}
        source={importing ?? {}}
        onCreated={(token) => {
          toast(t('created', { token }));
          setImporting(null);
          reload();
          navigate(`/dashboards/${encodeURIComponent(token)}`);
        }}
      />
      <FormDrawer shot="dashboard-new" open={creating} onOpenChange={setCreating} title={t('newDashboard')}>
        <DashboardCreateForm
          onDone={(token) => {
            toast(t('created', { token }));
            setCreating(false);
            reload();
            navigate(`/dashboards/${encodeURIComponent(token)}`);
          }}
        />
        <div className="mt-8 space-y-2 border-t border-border pt-4">
          <p className="text-sm font-medium">{t('templatesHeading')}</p>
          <p className="text-sm text-muted-foreground">{t('templatesNote')}</p>
          {DASHBOARD_TEMPLATES.map((template) => (
            <Button
              key={template.id}
              data-shot="dashboard-template"
              variant="outline"
              className="h-auto w-full flex-col items-start gap-0 whitespace-normal py-2 text-left"
              onClick={() => {
                setCreating(false);
                setImporting({ text: template.json, name: t(template.nameKey) });
              }}
            >
              <span className="font-medium">{t(template.nameKey)}</span>
              <span className="text-xs font-normal text-muted-foreground">{t(template.descriptionKey)}</span>
            </Button>
          ))}
        </div>
      </FormDrawer>
      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={scoped ? t('emptyShared') : t('empty')} />
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
                    {canWrite && (
                      <Button
                        variant="ghost"
                        size="sm"
                        data-shot="dashboard-share-button"
                        onClick={(e) => {
                          e.stopPropagation();
                          setSharing({
                            token: dashboard.token,
                            name: dashboard.name || dashboard.token,
                            published: dashboard.publishedVersion != null,
                          });
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Share2 size={14} /> {t('share')}
                      </Button>
                    )}
                    {canWrite && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          void clone(dashboard.token);
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Copy size={14} /> {t('clone')}
                      </Button>
                    )}
                    {canWrite && (
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
                    )}
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
