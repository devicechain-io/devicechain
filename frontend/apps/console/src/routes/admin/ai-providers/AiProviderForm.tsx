// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The controlled AI-provider configuration editor, shared by the create drawer and
// the detail editor (ADR-056 §4). It renders {kind, name, description, endpoint,
// model, params, enabled} and the write-only API-key control. It never sees a stored
// cleartext key — only whether one exists (`existingHasSecret`) — matching the
// ADR-059 write-only contract.

import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { Textarea } from '@/routes/common';
import type { AiProvider } from '@/lib/api/ai-inference-admin';

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
// is set), so the only hard checks are model + endpoint/params shape. `t` is the
// caller's `aiProviders`-namespace translator (this is a plain utility, not a
// component).
export function validateProvider(state: ProviderEditorState, t: TFunction): string | null {
  if (state.kind.trim() === '') return t('kindRequiredError');
  if (state.model.trim() === '') return t('modelRequiredError');
  if (state.endpoint.trim() !== '' && !/^https?:\/\//i.test(state.endpoint.trim())) {
    return t('endpointInvalidError');
  }
  if (state.params.trim() !== '') {
    try {
      const parsed = JSON.parse(state.params);
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return t('paramsNotObjectError');
      }
    } catch {
      return t('paramsInvalidJsonError');
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

// ProviderBasicFields renders the provider's IDENTITY: kind, name, description, and the
// enabled toggle — the "what is this" facts. Split out so the detail page can put it on a
// Basic tab while the create drawer stacks it with the rest.
export function ProviderBasicFields({
  state,
  onChange,
  kinds,
}: {
  state: ProviderEditorState;
  onChange: (next: ProviderEditorState) => void;
  kinds: string[];
}) {
  const { t } = useTranslation('aiProviders');
  const set = (patch: Partial<ProviderEditorState>) => onChange({ ...state, ...patch });

  // The kind list may not include a legacy/unknown value on an existing provider; include
  // the current kind so the picker can render it without silently switching.
  const kindOptions = kinds.includes(state.kind) || state.kind === '' ? kinds : [state.kind, ...kinds];

  return (
    <div className="space-y-4">
      <FormField label={t('kindLabel')} htmlFor="ai-kind" description={t('kindDescription')}>
        {/* A closed enum, but on our styled picker (not a native select) so it reads like
            every other dropdown in the console. Never clearable — a provider always has a
            kind. */}
        <Combobox
          id="ai-kind"
          value={state.kind}
          onChange={(v) => set({ kind: v })}
          placeholder={t('selectKindPlaceholder')}
          searchPlaceholder={t('searchKindsPlaceholder')}
          emptyMessage={t('noKindsMessage')}
          allowClear={false}
          // A provider kind (openai/anthropic/…) has no separate human-readable
          // display name — the wire value IS the label a caller sees elsewhere
          // (list column, detail page), so it is shown verbatim rather than run
          // through a label map that does not exist server-side.
          options={kindOptions.map((k) => ({ value: k, label: k }))}
        />
      </FormField>

      <FormField label={t('common:colName')} htmlFor="ai-name">
        <Input id="ai-name" value={state.name} onChange={(e) => set({ name: e.target.value })} />
      </FormField>

      <FormField label={t('common:colDescription')} htmlFor="ai-description">
        <Textarea
          id="ai-description"
          value={state.description}
          onChange={(e) => set({ description: e.target.value })}
        />
      </FormField>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={state.enabled}
          onChange={(e) => set({ enabled: e.target.checked })}
          className="h-4 w-4 rounded border-input"
        />
        <span>{t('common:enabled')}</span>
        <span className="text-muted-foreground">{t('enabledHint')}</span>
      </label>
    </div>
  );
}

// ProviderConnectionFields renders HOW to reach the model: its id, an optional endpoint
// override, and opaque per-kind params. The write-only API key is a sibling
// (ProviderApiKeyControl) rather than folded in here, so a caller can lay the credential
// out on its own.
export function ProviderConnectionFields({
  state,
  onChange,
}: {
  state: ProviderEditorState;
  onChange: (next: ProviderEditorState) => void;
}) {
  const { t } = useTranslation('aiProviders');
  const set = (patch: Partial<ProviderEditorState>) => onChange({ ...state, ...patch });

  return (
    <div className="space-y-4">
      <FormField label={t('modelLabel')} htmlFor="ai-model" description={t('modelDescription')}>
        <Input
          id="ai-model"
          value={state.model}
          placeholder={t('modelPlaceholder')}
          onChange={(e) => set({ model: e.target.value })}
        />
      </FormField>

      <FormField label={t('endpointLabel')} htmlFor="ai-endpoint" description={t('endpointDescription')}>
        <Input
          id="ai-endpoint"
          value={state.endpoint}
          placeholder={t('endpointPlaceholder')}
          onChange={(e) => set({ endpoint: e.target.value })}
        />
      </FormField>

      <FormField label={t('paramsLabel')} htmlFor="ai-params" description={t('paramsDescription')}>
        <Textarea
          id="ai-params"
          value={state.params}
          placeholder={t('paramsPlaceholder')}
          onChange={(e) => set({ params: e.target.value })}
          rows={3}
          className="font-mono text-xs"
        />
      </FormField>
    </div>
  );
}

// AiProviderForm is the flat composition used by the create drawer: identity, then
// connection, then the credential — one scroll. The detail page composes the same three
// pieces into tabs instead.
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
  const setSecret = (next: Partial<SecretState>) =>
    onChange({ ...state, secret: { ...state.secret, ...next } });

  return (
    <div className="space-y-4">
      <ProviderBasicFields state={state} onChange={onChange} kinds={kinds} />
      <ProviderConnectionFields state={state} onChange={onChange} />
      <ProviderApiKeyControl
        mode={mode}
        existingHasSecret={existingHasSecret}
        secret={state.secret}
        onChange={setSecret}
      />
    </div>
  );
}

// ProviderApiKeyControl implements the write-only key UX. On create it is a single
// optional input. On edit, a stored key is shown as "configured" with Replace / Clear
// affordances — the value never comes back from the server, so the input is only ever
// for a NEW value. Exported so the detail page can place the credential on its own tab.
export function ProviderApiKeyControl({
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
  const { t } = useTranslation('aiProviders');
  const label = t('apiKeyLabel');
  const description = t('apiKeyDescription');

  // Hoisted out of the JSX below so the i18next lint rule (jsx-only mode) does not
  // mistake the SecretMode enum literal for user-facing text — see RoleForm's
  // SCOPES for the same technique. (Only this one call site tripped the rule; the
  // sibling `onChange({ mode: 'set', value: '' })` calls below did not, but the
  // same hazard applies to all of them, so the fix is the shape, not the instance.)
  const setNewValue = (value: string) => onChange({ mode: 'set', value });

  if (mode === 'edit' && existingHasSecret && secret.mode === 'keep') {
    return (
      <FormField label={label} description={description}>
        <div className="flex items-center gap-3 text-sm">
          <span className="rounded bg-emerald-500/10 px-2 py-1 font-medium text-emerald-600 dark:text-emerald-500">
            {t('apiKeyConfiguredStatus')}
          </span>
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => onChange({ mode: 'set', value: '' })}
          >
            {t('replaceButton')}
          </button>
          <button
            type="button"
            className="text-destructive hover:underline"
            onClick={() => onChange({ mode: 'clear', value: '' })}
          >
            {t('clearButton')}
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
            {t('apiKeyClearedStatus')}
          </span>
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => onChange({ mode: existingHasSecret ? 'keep' : 'set', value: '' })}
          >
            {t('undoButton')}
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
        onChange={(e) => setNewValue(e.target.value)}
        placeholder={mode === 'edit' && existingHasSecret ? t('apiKeyReplacePlaceholder') : t('apiKeyNewPlaceholder')}
      />
    </FormField>
  );
}
