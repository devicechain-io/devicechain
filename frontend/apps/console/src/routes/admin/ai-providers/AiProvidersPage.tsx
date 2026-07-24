// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI Providers list (ADR-056 §4). Providers are INSTANCE-scoped, operator-managed
// {kind, endpoint, model, write-only API key} configs. Registering a model here does
// not offer it to anyone: which tenants may use it is decided by granting it to a
// tenant TIER (ADR-065 decision 10), which is its own screen — /admin/ai-packaging.
//
// This is an ADMIN-console screen (ADR-065): it sits behind AdminProtectedRoute (a
// superuser identity session) and calls the ai-inference ADMIN plane, which accepts
// only an identity token and gates every resolver on ai:admin. Like every other admin
// screen it therefore carries no per-page authority check of its own — the route is
// the UI gate and the server is the real one.
//
// It deliberately does NOT read `useAuth().claims`: those are the TENANT access
// token's claims, and this screen has no tenant. An operator reaching it commonly has
// no tenant session at all.

import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Package, Plus, Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listAiProviders,
  listAiProviderKinds,
  createAiProvider,
  deleteAiProvider,
  getAiProvider,
} from '@/lib/api/ai-inference-admin';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { errMessage, rowLinkProps, StatusBadge, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import {
  AiProviderForm,
  newProviderState,
  validateProvider,
  providerSecretArg,
  type ProviderEditorState,
} from './AiProviderForm';
import { aiProviderPath } from './paths';

const pageSize = 20;

export default function AiProvidersPage() {
  const { t } = useTranslation('aiProviders');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();

  const { data, loading, error } = useQuery(
    () => listAiProviders({ pageNumber, pageSize }),
    [pageNumber, version],
  );
  const { data: kinds } = useQuery(listAiProviderKinds, []);

  const results = data?.results ?? [];

  const remove = async (token: string) => {
    if (
      !(await confirm({
        title: t('deleteProviderTitle'),
        description: t('deleteProviderDescription', { token }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      await deleteAiProvider(token);
      toast(t('providerDeletedToast', { token }));
      // On the last-row-of-a-later-page case, step back a page; the list keys on both
      // page and version, so it refetches once.
      if (results.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      reload();
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
        <>
          <Button variant="outline" onClick={() => navigate('/admin/ai-packaging')}>
            <Package size={16} /> {t('packagingButton')}
          </Button>
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} /> {t('newProviderButton')}
          </Button>
        </>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title={t('newProviderDrawerTitle')}>
        <AiProviderCreateForm
          kinds={kinds ?? []}
          onDone={(token) => {
            toast(t('providerCreatedToast', { token }));
            setCreating(false);
            reload();
            navigate(aiProviderPath(token));
          }}
        />
      </FormDrawer>

      {loading ? (
        <LoadingState description={t('loadingProviders')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description={t('emptyProviders')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('common:colName')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colKind')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colModel')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('apiKeyLabel')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((p) => (
                <DataTableRow
                  key={p.token}
                  {...rowLinkProps(() => navigate(aiProviderPath(p.token)))}
                >
                  <DataTableCell className="font-medium text-foreground">
                    {p.name || p.token}
                  </DataTableCell>
                  {/* p.kind is the wire enum with no separate display name — see
                      AiProviderForm's ProviderBasicFields. */}
                  <DataTableCell className="text-muted-foreground">{p.kind}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{p.model}</DataTableCell>
                  <DataTableCell>
                    {p.hasSecret ? (
                      <Badge variant="secondary">{t('apiKeyConfiguredBadge')}</Badge>
                    ) : (
                      <Badge variant="outline" className="text-muted-foreground">
                        {t('apiKeyNotSetBadge')}
                      </Badge>
                    )}
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={p.enabled} />
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p.token);
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Trash2 size={14} /> {t('common:delete')}
                      </Button>
                    </div>
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

// The create form collects the full provider shape. It reuses the shared
// AiProviderForm; token is collected here (immutable after create).
function AiProviderCreateForm({ kinds, onDone }: { kinds: string[]; onDone: (token: string) => void }) {
  const { t } = useTranslation('aiProviders');
  const [token, setToken] = useState('');
  const [editor, setEditor] = useState<ProviderEditorState>(() => newProviderState(kinds));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // The kind vocabulary is fetched async by the parent, so the form can mount (the
  // drawer opened) before it lands, leaving the controlled <select> stuck on an empty
  // kind. Adopt the first kind once it arrives — only while still empty, so a user's
  // explicit pick is never overwritten.
  useEffect(() => {
    if (editor.kind === '' && kinds.length > 0) {
      setEditor((e) => (e.kind === '' ? { ...e, kind: kinds[0] } : e));
    }
  }, [kinds, editor.kind]);

  const submit = async () => {
    setFormError(null);
    const shapeErr = validateProvider(editor, t);
    if (shapeErr) {
      setFormError(shapeErr);
      return;
    }
    setBusy(true);
    try {
      const { token: created } = await createAiProvider({
        token: token.trim(),
        name: editor.name.trim() || undefined,
        description: editor.description.trim() || undefined,
        kind: editor.kind,
        endpoint: editor.endpoint.trim() || undefined,
        model: editor.model.trim(),
        params: editor.params.trim() || undefined,
        enabled: editor.enabled,
        secret: providerSecretArg(editor, 'create'),
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
      <FormField label={t('common:colToken')} htmlFor="ai-token">
        <TokenField
          id="ai-token"
          entityType="ai-provider"
          value={token}
          onChange={setToken}
          seed={editor.name}
          placeholder={t('tokenPlaceholder')}
          checkAvailability={(candidate) => getAiProvider(candidate).then((p) => p === null)}
        />
      </FormField>
      <AiProviderForm
        state={editor}
        onChange={setEditor}
        kinds={kinds}
        mode="create"
        existingHasSecret={false}
      />
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !token.trim()}>
          {t('createProviderButton')}
        </Button>
      </div>
    </div>
  );
}
