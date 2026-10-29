// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A connection's editable settings: display name, client id, scopes, email claim,
// required authentication methods, required claims, the write-only client secret, and
// whether signing out also signs people out at the provider. The issuer, kind and token are fixed at creation and shown read-only.
//
// 🔴 THE SECRET NEVER COMES BACK. The read side only says whether one is stored, so the
// secret input is only ever for a NEW value: it mounts empty, it is never seeded from the
// connection, and "Replace" just reveals an empty field. There is no clear action because
// the API has none — a connection's secret is replaced, not removed.
//
// Updates are PARTIAL: the request carries only the fields the operator changed, so a
// field the form never touched cannot be overwritten.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  setIdpConnectionSecret,
  setIdpRequiredClaims,
  updateIdpConnection,
  type IdpClaim,
  type IdpConnection,
  type IdpConnectionChanges,
} from '@/lib/api/identity-providers';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { CopyToken } from '@/components/ui/copy-token';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { errMessage } from '@/routes/common';
import { CaBundlePanel } from './CaBundlePanel';
import { ClaimsEditor } from './IdpCreateForm';
import { splitList } from './idpForm';
import '@/i18n/lazy/identityProviders';

interface PanelProps {
  connection: IdpConnection;
  reading: boolean;
  onChanged: () => void;
}

const sameList = (a: string[], b: string[]) =>
  a.length === b.length && a.every((v, i) => v === b[i]);

export function SettingsPanel(props: PanelProps) {
  return (
    <div className="space-y-4">
      <GeneralSettings {...props} />
      <SecretPanel {...props} />
      <ClaimsPanel {...props} />
      <CaBundlePanel {...props} />
      <SignOutPanel {...props} />
    </div>
  );
}

function GeneralSettings({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const [name, setName] = useState(c.name);
  const [clientId, setClientId] = useState(c.clientId);
  const [emailClaim, setEmailClaim] = useState(c.emailClaim);
  const [scopes, setScopes] = useState(c.scopes.join(' '));
  const [amr, setAmr] = useState(c.requiredAmr.join(' '));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Only what changed goes on the wire.
  const changes: IdpConnectionChanges = {};
  if (name.trim() !== c.name) changes.name = name.trim();
  if (clientId.trim() !== c.clientId) changes.clientId = clientId.trim();
  if (emailClaim.trim() !== c.emailClaim) changes.emailClaim = emailClaim.trim();
  if (!sameList(splitList(scopes), c.scopes)) changes.scopes = splitList(scopes);
  if (!sameList(splitList(amr), c.requiredAmr)) changes.requiredAmr = splitList(amr);
  const dirty = Object.keys(changes).length > 0;

  const save = async () => {
    setError(null);
    if (name.trim() === '') return setError(t('nameRequiredError'));
    if (clientId.trim() === '') return setError(t('clientIdRequiredError'));
    setBusy(true);
    try {
      await updateIdpConnection(c.token, changes);
      toast(t('savedToast'));
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('generalTitle')}>
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}

        <FormField label={t('issuerLabel')} description={t('issuerFixedDescription')}>
          <Input value={c.issuer} readOnly aria-label={t('issuerLabel')} />
        </FormField>
        <FormField label={t('callbackUrlLabel')} description={t('callbackUrlDescription')}>
          <CopyToken value={c.callbackUrl} className="max-w-full" />
        </FormField>
        <FormField label={t('common:colName')} htmlFor="idp-edit-name">
          <Input id="idp-edit-name" value={name} onChange={(e) => setName(e.target.value)} />
        </FormField>
        <FormField label={t('clientIdLabel')} htmlFor="idp-edit-client-id">
          <Input
            id="idp-edit-client-id"
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            autoComplete="off"
          />
        </FormField>
        <FormField label={t('scopesLabel')} htmlFor="idp-edit-scopes" description={t('scopesEditDescription')}>
          <Input id="idp-edit-scopes" value={scopes} onChange={(e) => setScopes(e.target.value)} />
        </FormField>
        <FormField label={t('emailClaimLabel')} htmlFor="idp-edit-email-claim">
          <Input
            id="idp-edit-email-claim"
            value={emailClaim}
            onChange={(e) => setEmailClaim(e.target.value)}
          />
        </FormField>
        <FormField label={t('amrLabel')} htmlFor="idp-edit-amr" description={t('amrEditDescription')}>
          <Input id="idp-edit-amr" value={amr} onChange={(e) => setAmr(e.target.value)} />
        </FormField>
        <Button onClick={() => void save()} loading={busy} disabled={busy || reading || !dirty}>
          {t('saveButton')}
        </Button>
      </div>
    </SectionPanel>
  );
}

// Signing out at the provider too (OpenID Connect RP-initiated logout). Off unless switched
// on, because it ends the person's session at the provider for every application it serves.
// The switch saves at once (it is one field, and a partial update), and the panel says
// plainly when it can have no effect: the approved snapshot names no sign-out endpoint.
function SignOutPanel({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const noEndpoint = !c.discovery.endSessionEndpoint || !c.discovery.approved;

  const toggle = async (on: boolean) => {
    setError(null);
    setBusy(true);
    try {
      await updateIdpConnection(c.token, { providerSignOut: on });
      toast(on ? t('signOutOnToast') : t('signOutOffToast'));
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('signOutTitle')} description={t('signOutDescription')}>
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <label className="flex items-start gap-2 text-sm">
          <Checkbox
            checked={c.providerSignOut}
            disabled={busy || reading}
            onCheckedChange={(v) => void toggle(v === true)}
            aria-label={t('signOutToggleLabel')}
          />
          <span>{t('signOutToggleLabel')}</span>
        </label>
        {c.providerSignOut && noEndpoint && (
          <p className="text-sm text-muted-foreground">{t('signOutNoEndpoint')}</p>
        )}
        {c.signOutReturnUrls.length > 0 && (
          <FormField label={t('signOutReturnUrlsLabel')} description={t('signOutReturnUrlsDescription')}>
            <ul className="space-y-1">
              {c.signOutReturnUrls.map((u) => (
                <li key={u} className="flex items-center gap-2 text-sm">
                  <span className="break-all font-mono text-xs">{u}</span>
                  <CopyToken value={u} />
                </li>
              ))}
            </ul>
          </FormField>
        )}
      </div>
    </SectionPanel>
  );
}

function SecretPanel({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const [replacing, setReplacing] = useState(false);
  // Only ever the operator's own keystrokes — never a stored value.
  const [value, setValue] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const showInput = replacing || !c.hasSecret;

  const save = async () => {
    setError(null);
    if (value === '') return setError(t('secretRequiredError'));
    setBusy(true);
    try {
      await setIdpConnectionSecret(c.token, value);
      setValue('');
      setReplacing(false);
      toast(t('secretSavedToast'));
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('secretTitle')} description={t('clientSecretDescription')}>
      <div className="space-y-3">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <div className="flex items-center gap-3 text-sm">
          {c.hasSecret ? (
            <Badge variant="secondary">{t('secretConfiguredBadge')}</Badge>
          ) : (
            <Badge variant="outline">{t('secretNotSetBadge')}</Badge>
          )}
          {c.hasSecret && !replacing && (
            <Button variant="link" size="inline" onClick={() => setReplacing(true)} disabled={reading}>
              {t('replaceButton')}
            </Button>
          )}
        </div>
        {showInput && (
          <div className="flex items-center gap-2">
            <Input
              type="password"
              autoComplete="new-password"
              aria-label={t('clientSecretLabel')}
              value={value}
              placeholder={c.hasSecret ? t('secretReplacePlaceholder') : t('secretNewPlaceholder')}
              onChange={(e) => setValue(e.target.value)}
            />
            <Button onClick={() => void save()} loading={busy} disabled={busy || reading || value === ''}>
              {t('saveSecretButton')}
            </Button>
            {replacing && (
              <Button
                variant="ghost"
                onClick={() => {
                  setReplacing(false);
                  setValue('');
                }}
              >
                {t('common:cancel')}
              </Button>
            )}
          </div>
        )}
      </div>
    </SectionPanel>
  );
}

function ClaimsPanel({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const [claims, setClaims] = useState<IdpClaim[]>(() =>
    c.requiredClaims.map((r) => ({ name: r.name, value: r.value })),
  );
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setError(null);
    if (claims.some((r) => r.name.trim() === '' || r.value.trim() === '')) {
      return setError(t('claimIncompleteError'));
    }
    setBusy(true);
    try {
      await setIdpRequiredClaims(
        c.token,
        claims.map((r) => ({ name: r.name.trim(), value: r.value.trim() })),
      );
      toast(t('savedToast'));
      onChanged();
    } catch (err) {
      // e.g. a Google connection cannot drop `hd`, nor a Microsoft one `tid`.
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('claimsTitle')}>
      <div className="space-y-3">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <ClaimsEditor claims={claims} onChange={setClaims} description={t('claimsEditDescription')} />
        <Button onClick={() => void save()} loading={busy} disabled={busy || reading}>
          {t('saveClaimsButton')}
        </Button>
      </div>
    </SectionPanel>
  );
}
