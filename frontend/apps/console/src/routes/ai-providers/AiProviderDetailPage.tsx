// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The AI-provider detail / editor (ADR-056 §4). Edits {kind, name, description,
// endpoint, model, params, enabled, write-only API key}, promotes/clears the active
// provider, and hosts the operator smoke test (testAiProvider) — a live call through
// the provider's endpoint + key, the ai:admin affordance to validate a provider
// before promoting it. The key is write-only: the editor is told only whether one
// exists.

import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Star, StarOff } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, BackLink, errMessage } from '@/routes/common';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import {
  getAiProvider,
  listAiProviderKinds,
  updateAiProvider,
  setActiveAiProvider,
  clearActiveAiProvider,
  testAiProvider,
  type AiProvider,
} from '@/lib/api/ai-inference';
import {
  AiProviderForm,
  providerStateFrom,
  validateProvider,
  providerSecretArg,
  type ProviderEditorState,
} from './AiProviderForm';

export default function AiProviderDetailPage() {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = rawToken ?? '';
  const { claims } = useAuth();
  const canManage = hasAuthority(claims, 'ai:admin');
  // Skip the fetches for an unauthorized visitor (they'd 403); the notice renders below.
  const { data, loading, error } = useQuery(
    () => (canManage ? getAiProvider(token) : Promise.resolve(null)),
    [token, canManage],
  );
  const { data: kinds } = useQuery(
    () => (canManage ? listAiProviderKinds() : Promise.resolve([])),
    [canManage],
  );

  if (!canManage) {
    return (
      <PageShell title={token} banner="dashboard">
        <p className="text-sm text-muted-foreground">
          You don’t have permission to manage inference providers (
          <span className="font-mono">ai:admin</span>).
        </p>
      </PageShell>
    );
  }
  if (loading) {
    return (
      <PageShell title={token} banner="dashboard">
        <LoadingState description="Loading provider…" />
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
        <ErrorState description={`Provider “${token}” not found.`} />
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
  const { toast } = useToast();

  const [editor, setEditor] = useState<ProviderEditorState>(() => providerStateFrom(loaded));
  const [baseline, setBaseline] = useState<Baseline>(() => baselineFrom(loaded));
  const [active, setActive] = useState(loaded.active);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [activeBusy, setActiveBusy] = useState(false);

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
    const shapeErr = validateProvider(editor);
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
      toast('Provider saved');
    } catch (err) {
      toast(errMessage(err), 'error');
      setFormError(errMessage(err));
    } finally {
      setSaving(false);
    }
  };

  // Both set-active and clear-active touch the row's updated_at server-side, so the
  // editor's optimistic-concurrency baseline must advance with them — otherwise the
  // very next Save fails as a spurious "modified by another writer". clearActive is
  // also global (it clears WHOEVER is active), so before clearing we re-read our own
  // active state: if another operator promoted a different provider since this page
  // loaded, we must NOT clear (that would turn THEIRS off) — we resync and bail.
  const toggleActive = async () => {
    const wantClear = active; // intent, from what the operator currently sees
    setActiveBusy(true);
    try {
      if (wantClear) {
        const fresh = await getAiProvider(loaded.token);
        setActive(fresh?.active ?? false);
        if (fresh) setBaseline((b) => ({ ...b, updatedAt: fresh.updatedAt ?? b.updatedAt }));
        if (!fresh?.active) {
          toast('This provider is no longer active — refreshed', 'error');
          return;
        }
        await clearActiveAiProvider();
        // The clear touched our row; re-read to fold in the fresh updatedAt + active=false.
        const after = await getAiProvider(loaded.token);
        setActive(after?.active ?? false);
        if (after) setBaseline((b) => ({ ...b, updatedAt: after.updatedAt ?? b.updatedAt }));
        toast('Cleared the active provider — NL→rule authoring is now off');
      } else {
        const res = await setActiveAiProvider(loaded.token);
        setActive(res.active);
        setBaseline((b) => ({ ...b, updatedAt: res.updatedAt ?? b.updatedAt }));
        toast(`“${loaded.token}” is now the active provider`);
      }
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setActiveBusy(false);
    }
  };

  return (
    <PageShell
      title={loaded.name || loaded.token}
      description={<BackLink to="/ai-providers">All providers</BackLink>}
      banner="dashboard"
      action={
        <Button variant={active ? 'outline' : 'default'} onClick={toggleActive} loading={activeBusy} disabled={activeBusy}>
          {active ? (
            <>
              <StarOff size={16} /> Clear active
            </>
          ) : (
            <>
              <Star size={16} /> Set as active
            </>
          )}
        </Button>
      }
    >
      <div className="max-w-2xl space-y-8">
        <section className="space-y-4">
          {active ? (
            <Badge variant="success" className="gap-1">
              <Star size={11} /> Active provider
            </Badge>
          ) : (
            <Badge variant="outline" className="text-muted-foreground">
              Not active
            </Badge>
          )}
          {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
          <AiProviderForm
            state={editor}
            onChange={setEditor}
            kinds={kinds}
            mode="edit"
            existingHasSecret={baseline.hasSecret}
          />
          <div className="flex items-center gap-3">
            <Button onClick={save} loading={saving} disabled={!dirty || saving}>
              Save provider
            </Button>
            {dirty && <span className="text-sm text-muted-foreground">Unsaved changes</span>}
          </div>
        </section>

        <TestInferPanel token={loaded.token} hasSecret={baseline.hasSecret} />
      </div>
    </PageShell>
  );
}

// TestInferPanel is the operator smoke test: it runs a prompt through THIS provider
// (bypassing the tenant-consent gate — it's operator config, not a tenant request) to
// validate the endpoint + key before promoting it. The returned candidate is shown
// verbatim; downstream it would be validated by the deterministic rule compiler, but
// here it just proves the provider answers.
function TestInferPanel({ token, hasSecret }: { token: string; hasSecret: boolean }) {
  const [prompt, setPrompt] = useState('Reply with the single word: ok');
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
    <section className="space-y-4 rounded-lg border border-border p-4">
      <div>
        <h2 className="text-sm font-semibold text-foreground">Test provider</h2>
        <p className="text-sm text-muted-foreground">
          Run a prompt live through this provider’s endpoint and key to validate it. This is an
          operator smoke test — it does not apply the per-tenant external-routing consent gate.
        </p>
      </div>
      {!hasSecret && (
        <p className="text-sm text-warning">
          No API key is configured — set and save one above before testing.
        </p>
      )}
      {testError && <ErrorBanner message={testError} onDismiss={() => setTestError(null)} />}
      <FormField label="System prompt (optional)" htmlFor="ai-test-system">
        <Textarea
          id="ai-test-system"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          rows={2}
        />
      </FormField>
      <FormField label="Prompt" htmlFor="ai-test-prompt">
        <Textarea
          id="ai-test-prompt"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          rows={3}
        />
      </FormField>
      <Button onClick={run} loading={busy} disabled={busy || prompt.trim() === ''}>
        Test provider
      </Button>
      {result && (
        <div className="space-y-2 rounded-md bg-muted/40 p-3">
          <p className="text-xs text-muted-foreground">
            Answered by <span className="font-mono">{result.model}</span>
          </p>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-xs text-foreground">
            {result.candidate}
          </pre>
        </div>
      )}
    </section>
  );
}
