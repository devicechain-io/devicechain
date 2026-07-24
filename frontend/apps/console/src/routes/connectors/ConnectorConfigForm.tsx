// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The controlled connector configuration editor, shared by the create drawer and
// the detail editor. It renders the type-specific config fields (driven by
// connectorSpec) and the write-only credential control. It never sees a stored
// cleartext secret — only whether one exists (`existingHasSecret`) — matching the
// ADR-059 write-only contract.

import type { ChangeEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/routes/common';
import {
  CONNECTOR_TYPE_SPECS,
  SASL_MECHANISMS,
  specForType,
  emptyFormState,
  deserializeConfig,
  serializeConfig,
  validateConfigForm,
  type ConfigFormState,
} from './connectorSpec';

// How the credential is being edited. On create the only meaningful modes are
// 'set' (type a value) and, implicitly, leaving it empty (no credential). On edit,
// 'keep' preserves the stored secret, 'set' replaces it, 'clear' removes it.
export type SecretMode = 'keep' | 'set' | 'clear';
export interface SecretState {
  mode: SecretMode;
  value: string;
}

export interface ConnectorEditorState {
  type: string;
  // Structured form state for a known type.
  form: ConfigFormState;
  // Raw config JSON, used only when `type` has no registered spec (e.g. a
  // gcp_pubsub connector created via the API before its generator ships).
  raw: string;
  secret: SecretState;
}

// newEditorState builds a fresh editor state for a create flow (defaults to the
// first registered type). The secret starts in 'set' (an empty new-credential input).
export function newEditorState(type = CONNECTOR_TYPE_SPECS[0].type): ConnectorEditorState {
  return { type, form: emptyFormState(specForType(type)), raw: '', secret: { mode: 'set', value: '' } };
}

// editorFromConnector reconstructs the editor state from a loaded connector for the
// edit flow. The credential is never returned, so the secret starts in 'keep' when
// one exists (preserve on save) or 'set' when none does.
export function editorFromConnector(c: {
  type: string;
  config: string;
  hasSecret: boolean;
}): ConnectorEditorState {
  const spec = specForType(c.type);
  return {
    type: c.type,
    form: spec ? deserializeConfig(spec, c.config) : emptyFormState(spec),
    raw: spec ? '' : c.config,
    secret: { mode: c.hasSecret ? 'keep' : 'set', value: '' },
  };
}

// editorConfigJSON produces the connector `config` JSON to send, from either the
// structured form (known type) or the raw JSON (unknown type).
export function editorConfigJSON(state: ConnectorEditorState): string {
  const spec = specForType(state.type);
  return spec ? serializeConfig(spec, state.form) : state.raw;
}

// secretRequiredForState reports whether a credential must be present for the
// current shape: AWS types always require one; kafka requires one only when SASL is
// enabled (the password is the SASL password). mqtt's password is optional.
export function secretRequiredForState(state: ConnectorEditorState): boolean {
  const spec = specForType(state.type);
  if (!spec) return false;
  return spec.secret.required || (spec.sasl === true && state.form.saslEnabled);
}

// validateEditor runs the fast client-side shape check, returning a human message
// or null. The server re-validates authoritatively on save/publish. existingHasSecret
// is whether a credential is already stored (edit flow) — a required secret is
// satisfied by preserving it, so a save that keeps it needs no re-entry. `t` is the
// caller's `connectors` namespace translator — this is a plain utility function, not
// a component, so it can't call useTranslation itself.
export function validateEditor(
  state: ConnectorEditorState,
  t: (key: string, options?: Record<string, unknown>) => string,
  existingHasSecret = false,
): string | null {
  const spec = specForType(state.type);
  if (!spec) {
    try {
      JSON.parse(state.raw || '{}');
    } catch {
      return t('invalidJson');
    }
    return null;
  }
  const shapeErr = validateConfigForm(spec, state.form, t);
  if (shapeErr) return shapeErr;
  if (secretRequiredForState(state)) {
    // A credential will exist after save when: it is being set to a non-empty value,
    // or it is being preserved (keep) and one is already stored. A 'clear', or a 'set'
    // with an empty input and no stored secret, leaves none — reject.
    const s = state.secret;
    const willHaveSecret =
      (s.mode === 'set' && s.value !== '') || (s.mode === 'keep' && existingHasSecret);
    if (!willHaveSecret) {
      return t('secretRequired', { secretLabel: t(spec.secret.label), connectorLabel: spec.label });
    }
  }
  return null;
}

// editorSecretArg maps the secret editor state to the mutation's write-only `secret`
// argument: undefined ⇒ omit (preserve/none), '' ⇒ clear, value ⇒ set. On create,
// 'keep' cannot occur; an empty 'set' means no credential (undefined).
export function editorSecretArg(state: ConnectorEditorState, mode: 'create' | 'edit'): string | undefined {
  const s = state.secret;
  if (s.mode === 'keep') return undefined;
  if (s.mode === 'clear') return mode === 'create' ? undefined : '';
  // mode === 'set': an empty input preserves (edit) / means no credential (create),
  // both expressed as "omit"; a typed value seals it.
  return s.value === '' ? undefined : s.value;
}

// A minimal styled <select>, matching the canvas inspector's (there is no shared
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

export function ConnectorConfigForm({
  state,
  onChange,
  mode,
  existingHasSecret,
}: {
  state: ConnectorEditorState;
  onChange: (next: ConnectorEditorState) => void;
  mode: 'create' | 'edit';
  existingHasSecret: boolean;
}) {
  const { t } = useTranslation('connectors');
  const spec = specForType(state.type);

  const setType = (type: string) => {
    // Switching type resets the config to the new type's empty shape — the field
    // sets differ and a stale field would be an invalid config for the new type.
    // Reset the credential too: a value typed for the old type (e.g. an MQTT
    // password) must not be submitted as the new type's secret (an AWS secret key).
    onChange({ ...state, type, form: emptyFormState(specForType(type)), raw: '', secret: { mode: 'set', value: '' } });
  };

  const setField = (key: string, value: string | boolean) => {
    onChange({ ...state, form: { ...state.form, fields: { ...state.form.fields, [key]: value } } });
  };

  const setSecret = (next: Partial<SecretState>) => {
    onChange({ ...state, secret: { ...state.secret, ...next } });
  };

  return (
    <div className="space-y-4">
      <FormField label={t('typeLabel')} htmlFor="cx-type" description={t('typeDescription')}>
        {mode === 'create' ? (
          <Select id="cx-type" value={state.type} onChange={setType}>
            {CONNECTOR_TYPE_SPECS.map((s) => (
              <option key={s.type} value={s.type}>
                {s.label}
              </option>
            ))}
          </Select>
        ) : (
          // On edit the type is shown read-only: changing a live connector's
          // transport is rare and error-prone (it invalidates every field and the
          // credential shape). Delete and recreate instead.
          <Input id="cx-type" value={spec?.label ?? state.type} readOnly disabled />
        )}
      </FormField>

      {spec ? (
        <>
          {spec.fields.map((f) => {
            const v = state.form.fields[f.key];
            if (f.kind === 'bool') {
              return (
                <label key={f.key} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={v === true}
                    onChange={(e) => setField(f.key, e.target.checked)}
                    className="h-4 w-4 rounded border-input"
                  />
                  <span>{t(f.label)}</span>
                  {f.description && <span className="text-muted-foreground">— {t(f.description)}</span>}
                </label>
              );
            }
            if (f.kind === 'qos') {
              return (
                <FormField key={f.key} label={t(f.label)} htmlFor={`cx-${f.key}`} description={f.description ? t(f.description) : undefined}>
                  <Select id={`cx-${f.key}`} value={typeof v === 'string' ? v : ''} onChange={(nv) => setField(f.key, nv)}>
                    <option value="">{t('qosDefault')}</option>
                    <option value="0">{t('qos0')}</option>
                    <option value="1">{t('qos1')}</option>
                    <option value="2">{t('qos2')}</option>
                  </Select>
                </FormField>
              );
            }
            if (f.kind === 'list') {
              return (
                <FormField
                  key={f.key}
                  label={f.required ? `${t(f.label)} *` : t(f.label)}
                  htmlFor={`cx-${f.key}`}
                  description={f.description ? t(f.description) : undefined}
                >
                  <Textarea
                    id={`cx-${f.key}`}
                    value={typeof v === 'string' ? v : ''}
                    onChange={(e) => setField(f.key, e.target.value)}
                    placeholder={f.placeholder}
                    rows={3}
                  />
                </FormField>
              );
            }
            return (
              <FormField
                key={f.key}
                label={f.required ? `${t(f.label)} *` : t(f.label)}
                htmlFor={`cx-${f.key}`}
                description={f.description ? t(f.description) : undefined}
              >
                <Input
                  id={`cx-${f.key}`}
                  value={typeof v === 'string' ? v : ''}
                  onChange={(e) => setField(f.key, e.target.value)}
                  placeholder={f.placeholder}
                />
              </FormField>
            );
          })}

          {spec.sasl && (
            <div className="space-y-3 rounded-md border border-dashed p-3">
              <label className="flex items-center gap-2 text-sm font-medium">
                <input
                  type="checkbox"
                  checked={state.form.saslEnabled}
                  onChange={(e) => onChange({ ...state, form: { ...state.form, saslEnabled: e.target.checked } })}
                  className="h-4 w-4 rounded border-input"
                />
                <span>{t('useSasl')}</span>
              </label>
              {state.form.saslEnabled && (
                <>
                  <FormField label={t('mechanismLabel')} htmlFor="cx-sasl-mech">
                    <Select
                      id="cx-sasl-mech"
                      value={state.form.saslMechanism}
                      onChange={(nv) => onChange({ ...state, form: { ...state.form, saslMechanism: nv } })}
                    >
                      {SASL_MECHANISMS.map((m) => (
                        <option key={m} value={m}>
                          {m}
                        </option>
                      ))}
                    </Select>
                  </FormField>
                  <FormField label={t('saslUsernameLabel')} htmlFor="cx-sasl-user">
                    <Input
                      id="cx-sasl-user"
                      value={state.form.saslUsername}
                      onChange={(e) => onChange({ ...state, form: { ...state.form, saslUsername: e.target.value } })}
                    />
                  </FormField>
                </>
              )}
            </div>
          )}
        </>
      ) : (
        // Unknown/unsupported type: edit the raw config JSON directly (the backend
        // validates it). Keeps a connector of a not-yet-shipped type editable.
        <FormField
          label={t('rawConfigLabel')}
          htmlFor="cx-raw"
          description={t('rawConfigDescription', { type: state.type })}
        >
          <Textarea
            id="cx-raw"
            value={state.raw}
            onChange={(e) => onChange({ ...state, raw: e.target.value })}
            rows={6}
            className="font-mono text-xs"
          />
        </FormField>
      )}

      <SecretControl
        spec={state.type}
        label={spec ? t(spec.secret.label) : t('credentialLabel')}
        description={spec ? t(spec.secret.description) : t('credentialDescription')}
        mode={mode}
        required={secretRequiredForState(state)}
        existingHasSecret={existingHasSecret}
        secret={state.secret}
        onChange={setSecret}
      />
    </div>
  );
}

// SecretControl implements the write-only credential UX. On create it is a single
// optional input. On edit, a stored credential is shown as "configured" with
// Replace / Clear affordances — the value never comes back from the server, so the
// input is only ever for a NEW value.
function SecretControl({
  spec,
  label,
  description,
  mode,
  required,
  existingHasSecret,
  secret,
  onChange,
}: {
  spec: string;
  label: string;
  description: string;
  mode: 'create' | 'edit';
  // Whether a credential is required for the current shape (AWS always; kafka when
  // SASL is enabled). When required, Clear is not offered — the credential can't be
  // stripped without breaking dispatch.
  required: boolean;
  existingHasSecret: boolean;
  secret: SecretState;
  onChange: (next: Partial<SecretState>) => void;
}) {
  const { t } = useTranslation('connectors');
  // Handlers are named functions (not inline JSX arrows) so the SecretMode
  // discriminants below read as plain code, not user-facing text.
  const handleReplace = () => onChange({ mode: 'set', value: '' });
  const handleClear = () => onChange({ mode: 'clear', value: '' });
  const handleUndo = () => onChange({ mode: existingHasSecret ? 'keep' : 'set', value: '' });
  const handleSecretInput = (e: ChangeEvent<HTMLInputElement>) =>
    onChange({ mode: 'set', value: e.target.value });

  if (mode === 'edit' && existingHasSecret && secret.mode === 'keep') {
    return (
      <FormField label={required ? `${label} *` : label} description={description}>
        <div className="flex items-center gap-3 text-sm">
          <span className="rounded bg-emerald-500/10 px-2 py-1 font-medium text-emerald-600 dark:text-emerald-500">
            {t('credentialConfigured')}
          </span>
          <button type="button" className="text-primary hover:underline" onClick={handleReplace}>
            {t('replace')}
          </button>
          {!required && (
            <button type="button" className="text-destructive hover:underline" onClick={handleClear}>
              {t('clear')}
            </button>
          )}
        </div>
      </FormField>
    );
  }
  if (mode === 'edit' && secret.mode === 'clear') {
    return (
      <FormField label={label} description={description}>
        <div className="flex items-center gap-3 text-sm">
          <span className="rounded bg-destructive/10 px-2 py-1 font-medium text-destructive">
            {t('credentialWillClear')}
          </span>
          <button type="button" className="text-primary hover:underline" onClick={handleUndo}>
            {t('undo')}
          </button>
        </div>
      </FormField>
    );
  }
  return (
    <FormField label={required ? `${label} *` : label} htmlFor={`cx-secret-${spec}`} description={description}>
      <Input
        id={`cx-secret-${spec}`}
        type="password"
        autoComplete="new-password"
        value={secret.value}
        onChange={handleSecretInput}
        placeholder={mode === 'edit' && existingHasSecret ? t('enterNewValue') : ''}
      />
    </FormField>
  );
}
