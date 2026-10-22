// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Copy, KeyRound, Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { IconButton } from '@/components/ui/icon-button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import { FormField } from '@/components/ui/form-field';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { formatTime } from '@/lib/utils';
import { useNoAutofill } from '@/lib/hooks/use-no-autofill';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { generatePassword, passwordBelowMinimum, MIN_PASSWORD_BYTES } from '@/lib/credentialPassword';
import {
  listDeviceCredentials,
  createDeviceCredential,
  rotateDeviceCredentialSecret,
  rotateDeviceCredentialToken,
  deleteDeviceCredential,
  CREDENTIAL_TYPES,
  type CredentialType,
  type DeviceCredential,
} from '@/lib/api/credentials';

// MQTT_BASIC is the only type that carries a stored secret (the password); the
// others prove possession out of band, so only the id is registered.
const needsSecret = (t: CredentialType) => t === 'MQTT_BASIC';

// DeviceCredentialsPanel lists a device's credentials and registers/rotates/removes
// them (ADR-014). The platform keeps only a keyed digest of an MQTT_BASIC password
// and of an ACCESS_TOKEN, so neither can be shown again: the password or token is
// displayed ONCE, in a banner, when it is set (registered or rotated), and the list
// afterwards says only that one is set. The banner is component state: it is gone on
// dismissal, navigation or reload, and nothing read from the API can put a value back
// in it. No form is ever pre-filled with a secret; changing it is the explicit "Set
// new password" or "Set new token" action. An MQTT username is a readable identifier
// by design and is shown with a copy button; an access token IS the secret and has none.
//
// 🔴 Every operation here — the list included — requires device:write, which the
// server enforces on the list query. This panel therefore assumes its caller has
// already checked hasAuthority(claims, 'device:write') and renders it only then —
// DeviceDetailPage hides the whole Credentials tab otherwise. Do not mount it
// unconditionally; every query and mutation in it would 403.
//
// Loads independently of the rest of the page: a query failure degrades to an
// ErrorState here rather than breaking the device detail page.
export function DeviceCredentialsPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [type, setType] = useState<CredentialType>('ACCESS_TOKEN');
  const [credentialId, setCredentialId] = useState('');
  const [secret, setSecret] = useState('');
  const [submitting, setSubmitting] = useState(false);
  // The password or access token just set, shown once. For a password, username is the
  // credential id; a token has no separate username (it is the credential id).
  const [issued, setIssued] = useState<{
    kind: 'password' | 'token';
    username?: string;
    secret: string;
    rotated: boolean;
  } | null>(null);
  // The credential whose "set new password" form is open, and its draft.
  const [rotating, setRotating] = useState<string | null>(null);
  const [newSecret, setNewSecret] = useState('');
  const [rotatingBusy, setRotatingBusy] = useState(false);
  const [version, reload] = useReload();
  // These are device credentials, not the operator's own login, so suppress
  // password-manager autofill/save on the fields (see the hook for the mechanism).
  const { noAutofill, rearm } = useNoAutofill();

  // A different device is a different screen: never carry its one-time password over.
  useEffect(() => {
    setIssued(null);
    setRotating(null);
    setNewSecret('');
  }, [deviceToken]);

  const { data, loading, error } = useQuery(
    () => listDeviceCredentials(deviceToken),
    [deviceToken, version],
  );

  // Switching type resets the fields so a value entered for one type never
  // carries into another. It also re-arms the autofill guard: the new fields are
  // freshly presented, so they start read-only again until focused (otherwise a
  // prior focus would leave them writable and a manager would refill them).
  const changeType = (t: CredentialType) => {
    setType(t);
    setCredentialId('');
    setSecret('');
    setRotating(null);
    rearm();
  };

  const startRotate = (c: DeviceCredential) => {
    setRotating(c.token);
    setNewSecret('');
    rearm();
  };

  const rotate = async (c: DeviceCredential) => {
    if (!newSecret) {
      toast(t('passwordRequired'), 'error');
      return;
    }
    setRotatingBusy(true);
    try {
      await rotateDeviceCredentialSecret(c.token, newSecret);
      setIssued({ kind: 'password', username: c.credentialId, secret: newSecret, rotated: true });
      toast(t('passwordRotated'));
      setRotating(null);
      setNewSecret('');
      rearm();
      reload();
    } catch (err) {
      // The server's refusal (for example a password under the minimum) is shown as is.
      toast(errMessage(err), 'error');
    } finally {
      setRotatingBusy(false);
    }
  };

  // A new access token replaces the old one at once: the device holding it stops
  // authenticating. The server returns the token on this one response and never again.
  const rotateToken = async (c: DeviceCredential) => {
    if (
      !(await confirm({
        title: t('rotateTokenTitle'),
        description: t('rotateTokenConfirm'),
        confirmLabel: t('setNewToken'),
      }))
    ) {
      return;
    }
    try {
      const updated = await rotateDeviceCredentialToken(c.token, crypto.randomUUID());
      setIssued({ kind: 'token', secret: updated.credentialId, rotated: true });
      toast(t('tokenRotated'));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text);
    toast(t('copiedToClipboard'));
  };

  const add = async () => {
    const id = credentialId.trim();
    if (!id) {
      toast(t('credentialIdRequired'), 'error');
      return;
    }
    if (needsSecret(type) && !secret) {
      toast(t('passwordRequired'), 'error');
      return;
    }
    setSubmitting(true);
    try {
      const created = await createDeviceCredential({
        token: crypto.randomUUID(),
        deviceToken,
        credentialType: type,
        credentialId: id,
        credentialValue: needsSecret(type) ? secret : undefined,
        enabled: true,
      });
      toast(t('credentialAdded'));
      if (needsSecret(type)) setIssued({ kind: 'password', username: id, secret, rotated: false });
      // The token comes back from the server on this response alone.
      else if (type === 'ACCESS_TOKEN') setIssued({ kind: 'token', secret: created.credentialId, rotated: false });
      setCredentialId('');
      setSecret('');
      rearm(); // re-arm the autofill guard for the now-empty fields
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const remove = async (c: DeviceCredential) => {
    if (
      !(await confirm({
        title: t('deleteCredentialTitle'),
        description: t('deleteCredentialConfirm', { credentialType: c.credentialType }),
        confirmLabel: t('delete'),
      }))
    ) {
      return;
    }
    try {
      await deleteDeviceCredential(c.token);
      toast(t('credentialDeleted'));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  const credentials = data ?? [];

  return (
    <div className="space-y-6">
      {/* The one-time banner for a password or an access token. Above everything so it
          cannot be scrolled past; it is the only place the value is ever rendered. */}
      {issued && (
        <div role="alert" className="space-y-2 rounded-lg border border-warning bg-warning/10 p-4">
          <div className="text-sm font-medium text-foreground">
            {issued.kind === 'token'
              ? issued.rotated
                ? t('tokenRotatedTitle')
                : t('tokenCreatedTitle')
              : issued.rotated
                ? t('passwordRotatedTitle')
                : t('passwordCreatedTitle')}
          </div>
          <p className="text-xs text-muted-foreground">
            {issued.kind === 'token' ? t('tokenShownOnce') : t('passwordShownOnce')}
          </p>
          {issued.username !== undefined && (
            <div className="text-xs text-muted-foreground">
              {t('usernameLabel')}: <span className="font-mono text-foreground">{issued.username}</span>
            </div>
          )}
          <div className="flex items-center gap-1.5">
            <span
              className="font-mono text-xs break-all text-foreground"
              data-testid={issued.kind === 'token' ? 'issued-token' : 'issued-password'}
              data-shot-mask
            >
              {issued.secret}
            </span>
            <IconButton
              label={issued.kind === 'token' ? t('copyTokenAriaLabel') : t('copyPasswordAriaLabel')}
              variant="quiet"
              size="xs"
              onClick={() => copy(issued.secret)}
            >
              <Copy size={13} />
            </IconButton>
          </div>
          <Button variant="outline" size="sm" onClick={() => setIssued(null)}>
            {t('dismissCredential')}
          </Button>
        </div>
      )}

      {/* Register form, grouped in a contrasting container. The type chooser sits
          on top; the fields below it are specific to the chosen type. */}
      <div className="space-y-4 rounded-lg border border-border bg-muted/40 p-4">
        <div className="max-w-52">
          <FormField label={t('credentialTypeLabel')} description={t('credentialTypeHint')}>
            <Combobox
              options={CREDENTIAL_TYPES.map((ct) => ({ value: ct }))}
              value={type}
              onChange={(v) => changeType(v as CredentialType)}
              allowClear={false}
            />
          </FormField>
        </div>

        {type === 'ACCESS_TOKEN' && (
          <FormField label={t('accessTokenLabel')} description={t('accessTokenHint')}>
            <div className="flex gap-2">
              <Input
                value={credentialId}
                onChange={(e) => setCredentialId(e.target.value)}
                placeholder={t('tokenValuePlaceholder')}
                {...noAutofill}
              />
              <Button type="button" variant="outline" onClick={() => setCredentialId(crypto.randomUUID())}>
                {t('generate')}
              </Button>
            </div>
          </FormField>
        )}

        {type === 'MQTT_BASIC' && (
          <div className="space-y-4">
            <FormField label={t('usernameLabel')}>
              <Input
                value={credentialId}
                onChange={(e) => setCredentialId(e.target.value)}
                placeholder={t('usernamePlaceholder')}
                {...noAutofill}
              />
            </FormField>
            <FormField label={t('passwordLabel')} description={t('passwordHint')}>
              <div className="flex gap-2">
                <Input
                  type="password"
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  placeholder="••••••••"
                  {...noAutofill}
                />
                <Button type="button" variant="outline" onClick={() => setSecret(generatePassword())}>
                  {t('generatePassword')}
                </Button>
              </div>
              {passwordBelowMinimum(secret) && (
                <p role="status" className="mt-1 text-xs text-warning">
                  {t('passwordTooShort', { min: MIN_PASSWORD_BYTES })}
                </p>
              )}
            </FormField>
          </div>
        )}

        <Button onClick={add} loading={submitting} disabled={submitting}>
          <Plus size={14} /> {t('addCredential')}
        </Button>
      </div>

      {/* Existing credentials. */}
      {loading ? (
        <LoadingState description={t('loadingCredentials')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : credentials.length === 0 ? (
        <EmptyState description={t('noCredentials')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('idColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('enabledColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('expiresColumn')}</DataTableHeaderCell>
            <DataTableHeaderCell>&nbsp;</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {credentials.map((c) => (
              <DataTableRow key={c.id}>
                <DataTableCell>
                  <Badge variant="secondary">{c.credentialType}</Badge>
                </DataTableCell>
                <DataTableCell>
                  {c.credentialType === 'ACCESS_TOKEN' ? (
                    // The token is stored only as a digest: there is nothing to show or copy.
                    <div className="text-xs text-muted-foreground">{t('accessTokenIsSet')}</div>
                  ) : (
                    <div className="flex items-center gap-1.5">
                      <span className="font-mono text-xs text-foreground">{c.credentialId}</span>
                      <IconButton
                        label={t('copyIdAriaLabel')}
                        variant="quiet"
                        size="xs"
                        onClick={() => copy(c.credentialId)}
                      >
                        <Copy size={13} />
                      </IconButton>
                    </div>
                  )}
                  {c.credentialType === 'MQTT_BASIC' && (
                    <div className="mt-1 space-y-2">
                      <div className="text-xs text-muted-foreground">{t('passwordIsSet')}</div>
                      {rotating === c.token && (
                        <div className="space-y-2">
                          <div className="flex gap-2">
                            <Input
                              type="password"
                              data-shot="credential-new-password"
                              aria-label={t('newPasswordLabel')}
                              value={newSecret}
                              onChange={(e) => setNewSecret(e.target.value)}
                              placeholder="••••••••"
                              {...noAutofill}
                            />
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              onClick={() => setNewSecret(generatePassword())}
                            >
                              {t('generatePassword')}
                            </Button>
                          </div>
                          {passwordBelowMinimum(newSecret) && (
                            <p role="status" className="text-xs text-warning">
                              {t('passwordTooShort', { min: MIN_PASSWORD_BYTES })}
                            </p>
                          )}
                          <p className="text-xs text-muted-foreground">{t('rotateWarning')}</p>
                          <div className="flex gap-2">
                            <Button size="sm" onClick={() => rotate(c)} data-shot="credential-rotate-save" loading={rotatingBusy} disabled={rotatingBusy}>
                              {t('savePassword')}
                            </Button>
                            <Button
                              size="sm"
                              variant="outline"
                              onClick={() => {
                                setRotating(null);
                                setNewSecret('');
                              }}
                            >
                              {t('cancel')}
                            </Button>
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                </DataTableCell>
                <DataTableCell>
                  {c.enabled ? (
                    <Badge variant="success">{t('enabledBadge')}</Badge>
                  ) : (
                    <Badge variant="outline" className="text-muted-foreground">
                      {t('disabledBadge')}
                    </Badge>
                  )}
                </DataTableCell>
                <DataTableCell className="text-muted-foreground">
                  {c.expiresAt ? formatTime(c.expiresAt) : '—'}
                </DataTableCell>
                <DataTableCell className="text-right">
                  {c.credentialType === 'MQTT_BASIC' && rotating !== c.token && (
                    <Button variant="outline" size="sm" className="mr-2" onClick={() => startRotate(c)} data-shot="credential-rotate">
                      <KeyRound size={13} /> {t('setNewPassword')}
                    </Button>
                  )}
                  {c.credentialType === 'ACCESS_TOKEN' && (
                    <Button variant="outline" size="sm" className="mr-2" onClick={() => rotateToken(c)} data-shot="credential-rotate-token">
                      <KeyRound size={13} /> {t('setNewToken')}
                    </Button>
                  )}
                  <Button variant="outline" size="sm" onClick={() => remove(c)}>
                    <Trash2 size={13} /> {t('delete')}
                  </Button>
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  );
}
