// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The controlled AI-provider configuration editor, shared by the create drawer and
// the detail editor (ADR-056 §4). It renders {kind, name, description, endpoint,
// model, params, enabled} and the write-only API-key control. It never sees a stored
// cleartext key — only whether one exists (`existingHasSecret`) — matching the
// ADR-059 write-only contract.

import type { ReactNode } from 'react';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/routes/common';
import type { AiProvider } from '@/lib/api/ai-inference';

// How the API key is being edited. On create the meaningful modes are 'set' (type a
// value) and, implicitly, leaving it empty (no key). On edit, 'keep' preserves the
// stored key, 'set' replaces it, 'clear' removes it.
export type SecretMode = 'keep' | 'set' | 'clear';
export interface SecretState {
  mode: SecretMode;
  value: string;
}

export interface ProviderEditorState {
  kind: string;
  name: string;
  description: string;
  // The provider API base URL, or "" for the kind's built-in default.
  endpoint: string;
  model: string;
  // Opaque per-kind params as a JSON object string, or "" for none.
  params: string;
  enabled: boolean;
  secret: SecretState;
}

// newProviderState builds a fresh editor for a create flow (defaults to the first
// registered kind). The key starts in 'set' (an empty new-credential input).
export function newProviderState(kinds: string[]): ProviderEditorState {
  return {
    kind: kinds[0] ?? '',
    name: '',
    description: '',
    endpoint: '',
    model: '',
    params: '',
    enabled: true,
    secret: { mode: 'set', value: '' },
  };
}

// providerStateFrom reconstructs the editor from a loaded provider for the edit flow.
// The key is never returned, so the secret starts in 'keep' when one exists (preserve
// on save) or 'set' when none does.
export function providerStateFrom(p: AiProvider): ProviderEditorState {
  return {
    kind: p.kind,
    name: p.name ?? '',
    description: p.description ?? '',
    endpoint: p.endpoint ?? '',
    model: p.model,
    params: p.params ?? '',
    enabled: p.enabled,
    secret: { mode: p.hasSecret ? 'keep' : 'set', value: '' },
  };
}

// validateProvider runs the fast client-side shape check, returning a human message
// or null. The server re-validates authoritatively on save. A key is NOT required: a
// provider can be created key-less and filled in later (it just can't serve until one
// is set), so the only hard checks are model + endpoint/params shape.
export function validateProvider(state: ProviderEditorState): string | null {
  if (state.model.trim() === '') return 'Model is required.';
  if (state.endpoint.trim() !== '' && !/^https?:\/\//i.test(state.endpoint.trim())) {
    return 'Endpoint must be an http(s) URL, or blank for the kind default.';
  }
  if (state.params.trim() !== '') {
    try {
      const parsed = JSON.parse(state.params);
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return 'Params must be a JSON object (like { "temperature": 0.2 }).';
      }
    } catch {
      return 'Params must be valid JSON, or blank.';
    }
  }
  return null;
}

// providerSecretArg maps the key editor state to the mutation's write-only `secret`
// argument: undefined ⇒ omit (preserve/none), '' ⇒ clear, value ⇒ set. On create,
// 'keep' cannot occur; an empty 'set' means no key (undefined).
export function providerSecretArg(state: ProviderEditorState, mode: 'create' | 'edit'): string | undefined {
  const s = state.secret;
  if (s.mode === 'keep') return undefined;
  if (s.mode === 'clear') return mode === 'create' ? undefined : '';
  // mode === 'set': an empty input preserves (edit) / means no key (create), both
  // expressed as "omit"; a typed value seals it.
  return s.value === '' ? undefined : s.value;
}

// A minimal styled <select>, matching the connector editor's (there is no shared
// Select primitive; the app uses native selects for closed enums).
function Select({
  value,
  onChange,
  children,
  id,
}: {
  value: string;
  onChange: (v: string) => void;
  children: ReactNode;
  id?: string;
}) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
    >
      {children}
    </select>
  );
}

export function AiProviderForm({
  state,
  onChange,
  kinds,
  mode,
  existingHasSecret,
}: {
  state: ProviderEditorState;
  onChange: (next: ProviderEditorState) => void;
  kinds: string[];
  mode: 'create' | 'edit';
  existingHasSecret: boolean;
}) {
  const set = (patch: Partial<ProviderEditorState>) => onChange({ ...state, ...patch });
  const setSecret = (next: Partial<SecretState>) =>
    onChange({ ...state, secret: { ...state.secret, ...next } });

  // The kind list may not include a legacy/unknown value on an existing provider;
  // include the current kind so the select can render it without silently switching.
  const kindOptions = kinds.includes(state.kind) || state.kind === '' ? kinds : [state.kind, ...kinds];

  return (
    <div className="space-y-4">
      <FormField
        label="Kind"
        htmlFor="ai-kind"
        description="The provider implementation. `anthropic` is the shipped kind at GA."
      >
        <Select id="ai-kind" value={state.kind} onChange={(v) => set({ kind: v })}>
          {kindOptions.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </Select>
      </FormField>

      <FormField label="Name" htmlFor="ai-name">
        <Input id="ai-name" value={state.name} onChange={(e) => set({ name: e.target.value })} />
      </FormField>

      <FormField label="Description" htmlFor="ai-description">
        <Textarea
          id="ai-description"
          value={state.description}
          onChange={(e) => set({ description: e.target.value })}
        />
      </FormField>

      <FormField
        label="Model *"
        htmlFor="ai-model"
        description="The provider model id (e.g. claude-opus-4-8)."
      >
        <Input
          id="ai-model"
          value={state.model}
          placeholder="claude-opus-4-8"
          onChange={(e) => set({ model: e.target.value })}
        />
      </FormField>

      <FormField
        label="Endpoint"
        htmlFor="ai-endpoint"
        description="Override the kind's default base URL (self-hosted / proxied). Blank = the built-in default."
      >
        <Input
          id="ai-endpoint"
          value={state.endpoint}
          placeholder="https://api.anthropic.com"
          onChange={(e) => set({ endpoint: e.target.value })}
        />
      </FormField>

      <FormField
        label="Params (JSON)"
        htmlFor="ai-params"
        description="Opaque per-kind tuning as a JSON object (e.g. output-token cap), or blank."
      >
        <Textarea
          id="ai-params"
          value={state.params}
          placeholder='{ "maxOutputTokens": 1024 }'
          onChange={(e) => set({ params: e.target.value })}
          rows={3}
          className="font-mono text-xs"
        />
      </FormField>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={state.enabled}
          onChange={(e) => set({ enabled: e.target.checked })}
          className="h-4 w-4 rounded border-input"
        />
        <span>Enabled</span>
        <span className="text-muted-foreground">— a disabled provider stays listed but never serves a call.</span>
      </label>

      <ApiKeyControl
        mode={mode}
        existingHasSecret={existingHasSecret}
        secret={state.secret}
        onChange={setSecret}
      />
    </div>
  );
}

// ApiKeyControl implements the write-only key UX. On create it is a single optional
// input. On edit, a stored key is shown as "configured" with Replace / Clear
// affordances — the value never comes back from the server, so the input is only ever
// for a NEW value.
function ApiKeyControl({
  mode,
  existingHasSecret,
  secret,
  onChange,
}: {
  mode: 'create' | 'edit';
  existingHasSecret: boolean;
  secret: SecretState;
  onChange: (next: Partial<SecretState>) => void;
}) {
  const label = 'API key';
  const description =
    'Write-only — sealed in the secret store and never displayed. A provider without a key cannot serve until one is set.';

  if (mode === 'edit' && existingHasSecret && secret.mode === 'keep') {
    return (
      <FormField label={label} description={description}>
        <div className="flex items-center gap-3 text-sm">
          <span className="rounded bg-emerald-500/10 px-2 py-1 font-medium text-emerald-600 dark:text-emerald-500">
            API key configured
          </span>
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => onChange({ mode: 'set', value: '' })}
          >
            Replace
          </button>
          <button
            type="button"
            className="text-destructive hover:underline"
            onClick={() => onChange({ mode: 'clear', value: '' })}
          >
            Clear
          </button>
        </div>
      </FormField>
    );
  }
  if (mode === 'edit' && secret.mode === 'clear') {
    return (
      <FormField label={label} description={description}>
        <div className="flex items-center gap-3 text-sm">
          <span className="rounded bg-destructive/10 px-2 py-1 font-medium text-destructive">
            API key will be cleared on save
          </span>
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => onChange({ mode: existingHasSecret ? 'keep' : 'set', value: '' })}
          >
            Undo
          </button>
        </div>
      </FormField>
    );
  }
  return (
    <FormField label={label} htmlFor="ai-secret" description={description}>
      <Input
        id="ai-secret"
        type="password"
        autoComplete="new-password"
        value={secret.value}
        onChange={(e) => onChange({ mode: 'set', value: e.target.value })}
        placeholder={mode === 'edit' && existingHasSecret ? 'Enter a new value' : 'sk-…'}
      />
    </FormField>
  );
}
