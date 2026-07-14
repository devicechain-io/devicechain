// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connector detail / editor (ADR-060 C5). Edits the draft {name, description,
// type, config, credential} and hosts the Versions panel (publish / restore). The
// credential is write-only: the editor is told only whether one exists.

import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage, BackLink } from '@/routes/common';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import { getConnector, updateConnector, type Connector } from '@/lib/api/connectors';
import {
  ConnectorConfigForm,
  editorFromConnector,
  editorConfigJSON,
  validateEditor,
  editorSecretArg,
  type ConnectorEditorState,
} from './ConnectorConfigForm';
import { ConnectorVersionsPanel } from './ConnectorVersionsPanel';

export default function ConnectorDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  const { data, loading, error } = useQuery(() => getConnector(token), [token]);

  if (loading) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description="Loading connector…" />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!data) {
    return (
      <PageShell title={token} banner="dashboard">
        <ErrorState description={`Connector “${token}” not found.`} />
      </PageShell>
    );
  }
  // Remount the editor on token change so its state re-seeds from the loaded draft.
  return <ConnectorEditor key={token} loaded={data} />;
}

function ConnectorEditor({ loaded }: { loaded: Connector }) {
  const { toast } = useToast();
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'connector:write');

  const initialEditor = useMemo(() => editorFromConnector(loaded), [loaded]);
  const [name, setName] = useState(loaded.name ?? '');
  const [description, setDescription] = useState(loaded.description ?? '');
  const [editor, setEditor] = useState<ConnectorEditorState>(initialEditor);
  // The saved baseline the optimistic-concurrency precondition + dirty check ride on.
  // config is stored in NORMALIZED form (round-tripped through the editor) — the
  // server returns the config as Postgres jsonb text (keys reordered, spaces added),
  // so comparing the editor's compact JSON against the raw server string would always
  // read as dirty. Normalizing both sides through editorConfigJSON makes the compare
  // semantic.
  const [baseline, setBaseline] = useState({
    name: (loaded.name ?? '').trim(),
    description: (loaded.description ?? '').trim(),
    config: editorConfigJSON(initialEditor),
    updatedAt: loaded.updatedAt ?? null,
    hasSecret: loaded.hasSecret,
  });
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // The editor is dirty when identity, config, or the credential differs from the
  // saved baseline. A credential change is a pending clear, or a set with a typed
  // value — an empty 'set' (the resting state of a secret-less connector) is NOT a
  // change, so it mirrors editorSecretArg and doesn't dead-end Publish.
  const dirty = useMemo(() => {
    const configChanged = editorConfigJSON(editor) !== baseline.config;
    const identityChanged = name.trim() !== baseline.name || description.trim() !== baseline.description;
    const s = editor.secret;
    const secretChanged = s.mode === 'clear' || (s.mode === 'set' && s.value !== '');
    return configChanged || identityChanged || secretChanged;
  }, [editor, name, description, baseline]);

  const save = async () => {
    setFormError(null);
    const shapeErr = validateEditor(editor, baseline.hasSecret);
    if (shapeErr) {
      setFormError(shapeErr);
      return;
    }
    setSaving(true);
    try {
      const config = editorConfigJSON(editor);
      const secret = editorSecretArg(editor, 'edit');
      const trimmedName = name.trim();
      const trimmedDescription = description.trim();
      const res = await updateConnector(loaded.token, {
        name: trimmedName || null,
        description: trimmedDescription || null,
        type: editor.type,
        config,
        secret,
        expectedUpdatedAt: baseline.updatedAt,
      });
      // Re-baseline: fold in the saved values + advance the concurrency token. A set
      // with a value seals a credential (hasSecret becomes true); a clear removes it;
      // an empty set / keep leaves it unchanged.
      const nextHasSecret =
        editor.secret.mode === 'set'
          ? editor.secret.value !== '' || baseline.hasSecret
          : editor.secret.mode === 'clear'
            ? false
            : baseline.hasSecret;
      // Sync the inputs to the trimmed values we persisted so the editor isn't left
      // reading as dirty on a name that only differed by surrounding whitespace.
      setName(trimmedName);
      setDescription(trimmedDescription);
      setBaseline({
        name: trimmedName,
        description: trimmedDescription,
        config,
        updatedAt: res.updatedAt,
        hasSecret: nextHasSecret,
      });
      // Reset the credential control back to 'keep'/'set' now that it's persisted.
      setEditor((e) => ({ ...e, secret: { mode: nextHasSecret ? 'keep' : 'set', value: '' } }));
      toast('Connector saved');
    } catch (err) {
      toast(errMessage(err), 'error');
      setFormError(errMessage(err));
    } finally {
      setSaving(false);
    }
  };

  // After a rollback re-drafts an earlier version, re-seed the config editor from it
  // and re-baseline to the NORMALIZED config (not the raw server jsonb text) + the
  // fresh updatedAt, so the editor reads clean and the next save's precondition holds.
  const onRolledBack = (draft: { type: string; config: string; updatedAt: string | null }) => {
    const next = editorFromConnector({ type: draft.type, config: draft.config, hasSecret: baseline.hasSecret });
    setEditor(next);
    setBaseline((b) => ({ ...b, config: editorConfigJSON(next), updatedAt: draft.updatedAt }));
  };

  return (
    <PageShell
      title={loaded.name || loaded.token}
      description={<BackLink to="/connectors">All connectors</BackLink>}
      banner="dashboard"
    >
      <Tabs defaultValue="config">
        <TabsList>
          <TabsTrigger value="config">Configuration</TabsTrigger>
          <TabsTrigger value="versions">Versions</TabsTrigger>
        </TabsList>

        <TabsContent value="config">
          <div className="max-w-2xl space-y-4">
            {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
            <FormField label="Name" htmlFor="cx-name">
              <Input
                id="cx-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={!canWrite}
              />
            </FormField>
            <FormField label="Description" htmlFor="cx-description">
              <Textarea
                id="cx-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                disabled={!canWrite}
              />
            </FormField>
            <ConnectorConfigForm
              state={editor}
              onChange={setEditor}
              mode="edit"
              existingHasSecret={baseline.hasSecret}
            />
            {canWrite && (
              <div className="flex items-center gap-3">
                <Button onClick={save} loading={saving} disabled={!dirty || saving}>
                  Save draft
                </Button>
                {dirty && <span className="text-sm text-muted-foreground">Unsaved changes</span>}
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="versions">
          <ConnectorVersionsPanel
            token={loaded.token}
            canWrite={canWrite}
            dirty={dirty}
            expectedUpdatedAt={baseline.updatedAt}
            onPublished={() => {
              /* the panel reloads its own list; nothing else to advance here */
            }}
            onRolledBack={onRolledBack}
          />
        </TabsContent>
      </Tabs>
    </PageShell>
  );
}
