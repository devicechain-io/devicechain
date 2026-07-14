// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Connectors list (ADR-060 C5). Outbound connectors are tenant-scoped, versioned
// {type, config, write-only credential} targets a REACT `publish` action delivers
// through. This page lists them and hosts the create drawer; the detail page edits
// and versions one.

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Plus, Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listConnectors,
  createConnector,
  deleteConnector,
  getConnector,
} from '@/lib/api/connectors';
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
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
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
import { specForType } from './connectorSpec';
import {
  ConnectorConfigForm,
  newEditorState,
  editorConfigJSON,
  validateEditor,
  editorSecretArg,
  type ConnectorEditorState,
} from './ConnectorConfigForm';

const pageSize = 20;

function typeLabel(type: string): string {
  return specForType(type)?.label ?? type;
}

export default function ConnectorsPage() {
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'connector:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listConnectors({ pageNumber, pageSize }),
    [pageNumber, version],
  );

  const results = data?.results ?? [];

  const remove = async (token: string) => {
    if (
      !(await confirm({
        title: 'Delete connector',
        description: `Delete “${token}”? Any rule that publishes through it will fail to deliver until repointed. This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteConnector(token);
      toast(`Connector “${token}” deleted`);
      if (results.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      else reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Connectors"
      description="Outbound targets a rule can publish to — brokers, cloud queues, and webhooks"
      banner="dashboard"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} /> New connector
          </Button>
        ) : undefined
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title="New connector">
        <ConnectorCreateForm
          onDone={(token) => {
            toast(`Connector “${token}” created`);
            setCreating(false);
            reload();
            navigate(`/connectors/${encodeURIComponent(token)}`);
          }}
        />
      </FormDrawer>
      {loading ? (
        <LoadingState description="Loading connectors…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : results.length === 0 ? (
        <EmptyState description="No connectors yet." />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>Name</DataTableHeaderCell>
              <DataTableHeaderCell>Type</DataTableHeaderCell>
              <DataTableHeaderCell>Description</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {results.map((c) => (
                <DataTableRow
                  key={c.token}
                  {...rowLinkProps(() => navigate(`/connectors/${encodeURIComponent(c.token)}`))}
                >
                  <DataTableCell className="font-medium text-foreground">
                    {c.name || c.token}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{typeLabel(c.type)}</DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {c.description || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-right">
                    {canWrite && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(c.token);
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Trash2 size={14} /> Delete
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

// The create form collects the full connector shape (a connector's config is
// non-nullable and validated on create, so it can't be created empty and filled in
// later). It reuses the shared ConnectorConfigForm.
function ConnectorCreateForm({ onDone }: { onDone: (token: string) => void }) {
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [editor, setEditor] = useState<ConnectorEditorState>(() => newEditorState());
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    const shapeErr = validateEditor(editor);
    if (shapeErr) {
      setFormError(shapeErr);
      return;
    }
    setBusy(true);
    try {
      const { token: created } = await createConnector({
        token: token.trim(),
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        type: editor.type,
        config: editorConfigJSON(editor),
        secret: editorSecretArg(editor, 'create'),
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
      <FormField label="Token" htmlFor="cx-token">
        <TokenField
          id="cx-token"
          entityType="connector"
          value={token}
          onChange={setToken}
          seed={name}
          placeholder="pagerduty-webhook"
          checkAvailability={(t) => getConnector(t).then((c) => c === null)}
        />
      </FormField>
      <FormField label="Name" htmlFor="cx-name">
        <Input id="cx-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="cx-description">
        <Textarea
          id="cx-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>
      <ConnectorConfigForm state={editor} onChange={setEditor} mode="create" existingHasSecret={false} />
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !token.trim()}>
          Create connector
        </Button>
      </div>
    </div>
  );
}
