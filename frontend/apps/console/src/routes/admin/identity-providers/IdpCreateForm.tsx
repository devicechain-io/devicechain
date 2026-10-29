// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The create-connection form. A connection starts DISABLED and unapproved: creating it
// fetches the provider's discovery document through the egress guard and holds it for a
// person to approve on the detail page, so nothing here switches sign-in on.
//
// The client secret is write-only. This form mounts empty, the field is a password input
// that is never given a value from anywhere but the operator's keystrokes, and the server
// never returns one — there is no code path that could pre-fill it.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, X } from 'lucide-react';
import { createIdpConnection, type IdpClaim } from '@/lib/api/identity-providers';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { IconButton } from '@/components/ui/icon-button';
import { Input } from '@/components/ui/input';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { errMessage } from '@/routes/common';
import { CaBundleInput } from './CaBundlePanel';
import {
  createRequest,
  newCreateState,
  splitList,
  validateCreate,
  type IdpCreateState,
  type IdpKind,
} from './idpForm';
import '@/i18n/lazy/identityProviders';

// Kept out of the JSX so the i18n lint rule does not read the discriminants as prose.
const KIND_OPTIONS_KEYS: IdpKind[] = ['tenant', 'instance'];

export function IdpCreateForm({ onDone }: { onDone: (token: string) => void }) {
  const { t } = useTranslation('identityProviders');
  const [state, setState] = useState<IdpCreateState>(newCreateState);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<IdpCreateState>) => setState((s) => ({ ...s, ...patch }));

  const needsMfaAck = state.kind === 'instance' && splitList(state.requiredAmr).length === 0;

  const submit = async () => {
    setFormError(null);
    const problem = validateCreate(state);
    if (problem) {
      setFormError(t(problem));
      return;
    }
    setBusy(true);
    try {
      const { token } = await createIdpConnection(createRequest(state));
      onDone(token);
    } catch (err) {
      // The server's refusal (an issuer that cannot be pinned to one organisation, an
      // unreachable provider, a taken token, a missing public issuer address) is the most
      // useful thing to show, so it is shown as it came.
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      noValidate
    >
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <FormField label={t('kindLabel')} description={t(`kindDescription_${state.kind}`)}>
        <SegmentedControl
          ariaLabel={t('kindLabel')}
          tone="joined"
          value={state.kind}
          onValueChange={(kind) => set({ kind })}
          options={KIND_OPTIONS_KEYS.map((k) => ({ value: k, label: t(`kind_${k}`) }))}
        />
      </FormField>

      <FormField label={t('common:colToken')} htmlFor="idp-token" description={t('tokenDescription')}>
        <Input
          id="idp-token"
          value={state.token}
          onChange={(e) => set({ token: e.target.value })}
          autoComplete="off"
        />
      </FormField>

      <FormField label={t('common:colName')} htmlFor="idp-name">
        <Input id="idp-name" value={state.name} onChange={(e) => set({ name: e.target.value })} />
      </FormField>

      <FormField label={t('issuerLabel')} htmlFor="idp-issuer" description={t('issuerDescription')}>
        <Input
          id="idp-issuer"
          value={state.issuer}
          placeholder={t('issuerPlaceholder')}
          onChange={(e) => set({ issuer: e.target.value })}
          autoComplete="off"
        />
      </FormField>

      <FormField label={t('clientIdLabel')} htmlFor="idp-client-id">
        <Input
          id="idp-client-id"
          value={state.clientId}
          onChange={(e) => set({ clientId: e.target.value })}
          autoComplete="off"
        />
      </FormField>

      <FormField
        label={t('clientSecretLabel')}
        htmlFor="idp-client-secret"
        description={t('clientSecretDescription')}
      >
        <Input
          id="idp-client-secret"
          type="password"
          autoComplete="new-password"
          value={state.clientSecret}
          onChange={(e) => set({ clientSecret: e.target.value })}
        />
      </FormField>

      <FormField label={t('scopesLabel')} htmlFor="idp-scopes" description={t('scopesDescription')}>
        <Input
          id="idp-scopes"
          value={state.scopes}
          placeholder={t('scopesPlaceholder')}
          onChange={(e) => set({ scopes: e.target.value })}
        />
      </FormField>

      <FormField
        label={t('emailClaimLabel')}
        htmlFor="idp-email-claim"
        description={t('emailClaimDescription')}
      >
        <Input
          id="idp-email-claim"
          value={state.emailClaim}
          placeholder={t('emailClaimPlaceholder')}
          onChange={(e) => set({ emailClaim: e.target.value })}
        />
      </FormField>

      <ClaimsEditor
        claims={state.claims}
        onChange={(claims) => set({ claims })}
        description={t('claimsCreateDescription')}
      />

      <FormField label={t('amrLabel')} htmlFor="idp-amr" description={t('amrDescription')}>
        <Input
          id="idp-amr"
          value={state.requiredAmr}
          placeholder={t('amrPlaceholder')}
          onChange={(e) => set({ requiredAmr: e.target.value })}
        />
      </FormField>

      <FormField label={t('caBundleLabel')} description={t('caBundleCreateDescription')}>
        <CaBundleInput value={state.caBundle} onChange={(caBundle) => set({ caBundle })} />
      </FormField>

      {needsMfaAck && (
        <label className="flex items-start gap-2 text-sm">
          <Checkbox
            checked={state.mfaEnforcedByIdp}
            onCheckedChange={(c) => set({ mfaEnforcedByIdp: c === true })}
            aria-label={t('mfaAckLabel')}
          />
          <span>
            <span className="font-medium text-foreground">{t('mfaAckLabel')}</span>
            <span className="block text-muted-foreground">{t('mfaAckDescription')}</span>
          </span>
        </label>
      )}

      <div className="flex gap-2">
        <Button type="submit" loading={busy} disabled={busy}>
          {t('createButton')}
        </Button>
      </div>
    </form>
  );
}

// ClaimsEditor edits the claims every sign-in must carry with exactly these values
// (Google needs `hd`, Microsoft needs `tid`). Controlled; the caller owns saving.
export function ClaimsEditor({
  claims,
  onChange,
  description,
  disabled,
}: {
  claims: IdpClaim[];
  onChange: (claims: IdpClaim[]) => void;
  description?: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation('identityProviders');
  const update = (i: number, patch: Partial<IdpClaim>) =>
    onChange(claims.map((c, j) => (j === i ? { ...c, ...patch } : c)));

  return (
    <FormField label={t('claimsLabel')} description={description}>
      <div className="space-y-2">
        {claims.map((c, i) => (
          <div key={i} className="flex items-center gap-2">
            <Input
              aria-label={t('claimNameLabel')}
              value={c.name}
              placeholder={t('claimNamePlaceholder')}
              disabled={disabled}
              onChange={(e) => update(i, { name: e.target.value })}
            />
            <Input
              aria-label={t('claimValueLabel')}
              value={c.value}
              placeholder={t('claimValuePlaceholder')}
              disabled={disabled}
              onChange={(e) => update(i, { value: e.target.value })}
            />
            <IconButton
              label={t('removeClaim')}
              disabled={disabled}
              onClick={() => onChange(claims.filter((_, j) => j !== i))}
            >
              <X size={14} />
            </IconButton>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={disabled}
          onClick={() => onChange([...claims, { name: '', value: '' }])}
        >
          <Plus size={14} /> {t('addClaim')}
        </Button>
      </div>
    </FormField>
  );
}
