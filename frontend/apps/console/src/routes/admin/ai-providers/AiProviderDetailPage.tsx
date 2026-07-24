// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI-provider detail / editor (ADR-056 §4). Edits {kind, name, description,
// endpoint, model, params, enabled, write-only API key} and hosts the operator smoke
// test (testAiProvider) — a live call through the provider's endpoint + key, the
// affordance to validate a provider before granting it to anyone. The key is
// write-only: the editor is told only whether one exists.
//
// Which tenants may USE this provider is not decided here: that is the tier↔provider
// grant surface (ADR-065 decision 10). Editing a model and selling it are separate
// acts with separate audit trails.
//
// An ADMIN-console screen (ADR-065): behind AdminProtectedRoute, calling the
// ai-inference ADMIN plane (identity token, ai:admin on every resolver). Like every
// other admin screen it carries no per-page authority check — and it must not read
// `useAuth().claims`, which are the TENANT access token's and are commonly absent
// here.

import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { CopyToken } from '@/components/ui/copy-token';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Textarea, errMessage } from '@/routes/common';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import {
  getAiProvider,
  listAiProviderKinds,
  updateAiProvider,
  testAiProvider,
  type AiProvider,
} from '@/lib/api/ai-inference-admin';
import {
  ProviderBasicFields,
  ProviderConnectionFields,
  ProviderApiKeyControl,
  providerStateFrom,
  validateProvider,
  providerSecretArg,
  type ProviderEditorState,
  type SecretState,
} from './AiProviderForm';

export default function AiProviderDetailPage() {
  const { t } = useTranslation('aiProviders');
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const { data, loading, error } = useQuery(() => getAiProvider(token), [token]);
  const { data: kinds } = useQuery(listAiProviderKinds, []);
  if (loading) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description={t('loadingProvider')} />
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
        <ErrorState description={t('providerNotFound', { token })} />
      </PageShell>
    );
  }
  // Remount the editor on token change so its state re-seeds from the loaded provider.
  return <AiProviderEditor key={token} loaded={data} kinds={kinds ?? []} />;
}

interface Baseline {
  kind: string;
  name: string;
  description: string;
  endpoint: string;
  model: string;
  params: string;
  enabled: boolean;
  updatedAt: string | null;
  hasSecret: boolean;
}

function baselineFrom(p: AiProvider): Baseline {
  return {
    kind: p.kind,
    name: (p.name ?? '').trim(),
    description: (p.description ?? '').trim(),
    endpoint: (p.endpoint ?? '').trim(),
    model: p.model.trim(),
    params: (p.params ?? '').trim(),
    enabled: p.enabled,
    updatedAt: p.updatedAt ?? null,
    hasSecret: p.hasSecret,
  };
}

function AiProviderEditor({ loaded, kinds }: { loaded: AiProvider; kinds: string[] }) {
  const { t } = useTranslation('aiProviders');
  const { toast } = useToast();

  const [editor, setEditor] = useState<ProviderEditorState>(() => providerStateFrom(loaded));
  const [baseline, setBaseline] = useState<Baseline>(() => baselineFrom(loaded));
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Dirty when any field or the API key differs from the saved baseline. A key change
  // is a pending clear or a set with a typed value — an empty 'set' (the resting state
  // of a key-less provider) is NOT a change (mirrors providerSecretArg).
  const dirty = useMemo(() => {
    const s = editor.secret;
    const secretChanged = s.mode === 'clear' || (s.mode === 'set' && s.value !== '');
    return (
      secretChanged ||
      editor.kind !== baseline.kind ||
      editor.name.trim() !== baseline.name ||
      editor.description.trim() !== baseline.description ||
      editor.endpoint.trim() !== baseline.endpoint ||
      editor.model.trim() !== baseline.model ||
      editor.params.trim() !== baseline.params ||
      editor.enabled !== baseline.enabled
    );
  }, [editor, baseline]);

  const save = async () => {
    setFormError(null);
    const shapeErr = validateProvider(editor, t);
    if (shapeErr) {
      setFormError(shapeErr);
      return;
    }
    setSaving(true);
    try {
      const res = await updateAiProvider(loaded.token, {
        name: editor.name.trim() || null,
        description: editor.description.trim() || null,
        kind: editor.kind,
        endpoint: editor.endpoint.trim() || null,
        model: editor.model.trim(),
        params: editor.params.trim() || null,
        enabled: editor.enabled,
        secret: providerSecretArg(editor, 'edit'),
        expectedUpdatedAt: baseline.updatedAt,
      });
      // Re-baseline: fold in the saved values + advance the concurrency token. A set
      // with a value seals a key (hasSecret true); a clear removes it; an empty
      // set / keep leaves it unchanged.
      const nextHasSecret =
        editor.secret.mode === 'set'
          ? editor.secret.value !== '' || baseline.hasSecret
          : editor.secret.mode === 'clear'
            ? false
            : baseline.hasSecret;
      setBaseline({
        kind: editor.kind,
        name: editor.name.trim(),
        description: editor.description.trim(),
        endpoint: editor.endpoint.trim(),
        model: editor.model.trim(),
        params: editor.params.trim(),
        enabled: editor.enabled,
        updatedAt: res.updatedAt,
        hasSecret: nextHasSecret,
      });
      // Reset the key control back to 'keep'/'set' now that it's persisted.
      setEditor((e) => ({ ...e, secret: { mode: nextHasSecret ? 'keep' : 'set', value: '' } }));
      toast(t('providerSavedToast'));
    } catch (err) {
      toast(errMessage(err), 'error');
      setFormError(errMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const setSecret = (next: Partial<SecretState>) =>
    setEditor((e) => ({ ...e, secret: { ...e.secret, ...next } }));

  // The Save button lives on BOTH editing tabs (Basic + Connection). A provider update is
  // a full replacement of every field, so the two tabs are two views of ONE save — each
  // persists the whole provider from the shared editor state, exactly as the tenant/tier
  // forms do. Splitting them into per-tab submits would let one tab's save omit a field
  // the other owns and silently blank it.
  const saveBar = (
    <div className="flex items-center gap-3">
      <Button onClick={save} loading={saving} disabled={!dirty || saving}>
        {t('saveProviderButton')}
      </Button>
      {dirty && <span className="text-sm text-muted-foreground">{t('unsavedChanges')}</span>}
    </div>
  );

  return (
    <PageShell
      title={loaded.name || loaded.token}
      titleAdornment={loaded.name ? <CopyToken value={loaded.token} /> : undefined}
      banner="dashboard"
    >
      <div className="space-y-4">
        {/* Above the tabs so a validation error (which may belong to a field on the other
            tab — e.g. "Model is required" while you saved from Basic) is always visible. */}
        {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
        <Tabs defaultValue="basic">
          <TabsList>
            <TabsTrigger value="basic">{t('basicTab')}</TabsTrigger>
            <TabsTrigger value="connection">{t('connectionTab')}</TabsTrigger>
            <TabsTrigger value="test">{t('testTab')}</TabsTrigger>
          </TabsList>
          <TabsContent value="basic">
            <SectionPanel>
              <div className="space-y-4">
                <ProviderBasicFields state={editor} onChange={setEditor} kinds={kinds} />
                {saveBar}
              </div>
            </SectionPanel>
          </TabsContent>
          <TabsContent value="connection">
            <SectionPanel>
              <div className="space-y-4">
                <ProviderConnectionFields state={editor} onChange={setEditor} />
                <ProviderApiKeyControl
                  mode="edit"
                  existingHasSecret={baseline.hasSecret}
                  secret={editor.secret}
                  onChange={setSecret}
                />
                {saveBar}
              </div>
            </SectionPanel>
          </TabsContent>
          <TabsContent value="test">
            <SectionPanel title={t('testProviderTitle')} description={t('testProviderDescription')}>
              <TestInferPanel token={loaded.token} hasSecret={baseline.hasSecret} dirty={dirty} />
            </SectionPanel>
          </TabsContent>
        </Tabs>
      </div>
    </PageShell>
  );
}

// TestInferPanel is the operator smoke test: it runs a prompt through THIS provider
// (bypassing the tenant-consent gate — it's operator config, not a tenant request) to
// validate the endpoint + key before promoting it. The returned candidate is shown
// verbatim; downstream it would be validated by the deterministic rule compiler, but
// here it just proves the provider answers.
function TestInferPanel({
  token,
  hasSecret,
  dirty,
}: {
  token: string;
  hasSecret: boolean;
  // The smoke test runs against the LAST SAVED provider (by token), not the editor's
  // unsaved edits — so a dirty editor means the test would validate the old config.
  dirty: boolean;
}) {
  const { t } = useTranslation('aiProviders');
  const [prompt, setPrompt] = useState(t('defaultTestPrompt'));
  const [system, setSystem] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ candidate: string; model: string } | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  const run = async () => {
    setTestError(null);
    setResult(null);
    setBusy(true);
    try {
      const res = await testAiProvider(token, {
        prompt,
        system: system.trim() === '' ? null : system,
      });
      setResult({ candidate: res.candidate, model: res.model });
    } catch (err) {
      setTestError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {!hasSecret && <p className="text-sm text-warning">{t('noApiKeyWarning')}</p>}
      {dirty && <p className="text-sm text-muted-foreground">{t('dirtyTestWarning')}</p>}
      {testError && <ErrorBanner message={testError} onDismiss={() => setTestError(null)} />}
      <FormField label={t('systemPromptLabel')} htmlFor="ai-test-system">
        <Textarea
          id="ai-test-system"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          rows={2}
        />
      </FormField>
      <FormField label={t('promptLabel')} htmlFor="ai-test-prompt">
        <Textarea
          id="ai-test-prompt"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          rows={3}
        />
      </FormField>
      <Button onClick={run} loading={busy} disabled={busy || prompt.trim() === ''}>
        {t('testProviderButton')}
      </Button>
      {result && (
        <div className="space-y-2 rounded-md bg-muted/40 p-3">
          <p className="text-xs text-muted-foreground">
            <Trans
              t={t}
              i18nKey="answeredBy"
              values={{ model: result.model }}
              components={{ model: <span className="font-mono" /> }}
            />
          </p>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-xs text-foreground">
            {result.candidate}
          </pre>
        </div>
      )}
    </div>
  );
}
