// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI Providers list (ADR-056 §4). Providers are INSTANCE-scoped, operator-managed
// {kind, endpoint, model, write-only API key} configs; at most one is the ACTIVE
// provider used for NL→rule authoring ("default of none" when none is active).
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
import { Plus, Star, Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listAiProviders,
  listAiProviderKinds,
  getActiveAiProvider,
  createAiProvider,
  deleteAiProvider,
  setActiveAiProvider,
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
  const { data: active, error: activeError } = useQuery(() => getActiveAiProvider(), [version]);
  const { data: kinds } = useQuery(listAiProviderKinds, []);

  const results = data?.results ?? [];

  const remove = async (token: string) => {
    if (
      !(await confirm({
        title: 'Delete provider',
        description: `Delete “${token}” and its API key? If it is the active provider, NL→rule authoring falls back to “none active” (feature off) until another is promoted.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteAiProvider(token);
      toast(`Provider “${token}” deleted`);
      // Always reload so the active-provider banner refreshes too (its query keys on
      // version, not pageNumber) — deleting the active provider must not leave the
      // banner naming a row that no longer exists. On the last-row-of-a-later-page
      // case, also step back a page; the list keys on both, so it refetches once.
      if (results.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const promote = async (token: string) => {
    try {
      await setActiveAiProvider(token);
      toast(`“${token}” is now the active provider`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="AI Providers"
      description="AI models available for drafting rules from a description. At most one is active — the model that answers drafting requests."
      banner="dashboard"
      action={
        <Button onClick={() => setCreating(true)}>
          <Plus size={16} /> New provider
        </Button>
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title="New AI provider">
        <AiProviderCreateForm
          kinds={kinds ?? []}
          onDone={(token) => {
            toast(`Provider “${token}” created`);
            setCreating(false);
            reload();
            navigate(aiProviderPath(token));
          }}
        />
      </FormDrawer>

      <ActiveBanner
        activeName={active?.name ?? null}
        activeToken={active?.token ?? null}
        error={activeError ?? null}
      />

      {loading ? (
        <LoadingState description="Loading providers…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description="No providers yet. Create one and promote it to active to enable NL→rule authoring." />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Kind</DataTableHeaderCell>
              <DataTableHeaderCell>Model</DataTableHeaderCell>
              <DataTableHeaderCell>API key</DataTableHeaderCell>
              <DataTableHeaderCell>Status</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((p) => (
                <DataTableRow
                  key={p.token}
                  {...rowLinkProps(() => navigate(aiProviderPath(p.token)))}
                >
                  <DataTableCell className="font-medium text-foreground">
                    <span className="inline-flex items-center gap-2">
                      {p.name || p.token}
                      {p.active && (
                        <Badge variant="success" className="gap-1">
                          <Star size={11} /> Active
                        </Badge>
                      )}
                    </span>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{p.kind}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{p.model}</DataTableCell>
                  <DataTableCell>
                    {p.hasSecret ? (
                      <Badge variant="secondary">Configured</Badge>
                    ) : (
                      <Badge variant="outline" className="text-muted-foreground">
                        Not set
                      </Badge>
                    )}
                  </DataTableCell>
                  <DataTableCell>
                    <StatusBadge enabled={p.enabled} />
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    <div className="flex items-center justify-end gap-1">
                      {!p.active && (
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation();
                            void promote(p.token);
                          }}
                          onKeyDown={(e) => e.stopPropagation()}
                        >
                          <Star size={14} /> Set active
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p.token);
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Trash2 size={14} /> Delete
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

// A one-line status of which provider currently answers authoring requests — the
// "default of none" state is called out because it means the feature is off.
function ActiveBanner({
  activeName,
  activeToken,
  error,
}: {
  activeName: string | null;
  activeToken: string | null;
  error: string | null;
}) {
  return (
    <div className="mb-4 rounded-md border border-border bg-muted/40 px-3 py-2 text-sm">
      {error ? (
        <span className="text-muted-foreground">
          Active-provider status is currently unavailable ({error}).
        </span>
      ) : activeToken ? (
        <span>
          Active provider:{' '}
          <span className="font-medium text-foreground">{activeName || activeToken}</span> — NL→rule
          authoring routes to this model (for tenants that have opted in to external routing).
        </span>
      ) : (
        <span className="text-muted-foreground">
          No active provider — NL→rule authoring is off until one is promoted.
        </span>
      )}
    </div>
  );
}

// The create form collects the full provider shape. It reuses the shared
// AiProviderForm; token is collected here (immutable after create).
function AiProviderCreateForm({ kinds, onDone }: { kinds: string[]; onDone: (token: string) => void }) {
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
    const shapeErr = validateProvider(editor);
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
      <FormField label="Token" htmlFor="ai-token">
        <TokenField
          id="ai-token"
          entityType="ai-provider"
          value={token}
          onChange={setToken}
          seed={editor.name}
          placeholder="claude-primary"
          checkAvailability={(t) => getAiProvider(t).then((p) => p === null)}
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
          Create provider
        </Button>
      </div>
    </div>
  );
}
