// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Copy, Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
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
import {
  listDeviceCredentials,
  createDeviceCredential,
  deleteDeviceCredential,
  CREDENTIAL_TYPES,
  type CredentialType,
  type DeviceCredential,
} from '@/lib/api/credentials';

// MQTT_BASIC is the only type that carries a stored secret (the password); the
// others prove possession out of band, so only the id is registered.
const needsSecret = (t: CredentialType) => t === 'MQTT_BASIC';

// DeviceCredentialsPanel lists a device's credentials and registers/removes them
// (ADR-014). The secret is write-only server-side and never displayed; the
// credential id (the device-facing identifier, e.g. an access token) is shown
// with a copy button. Loads independently: without device:read the query errors
// and this panel degrades to an ErrorState rather than breaking the page.
export function DeviceCredentialsPanel({ deviceToken }: { deviceToken: string }) {
  const { t } = useTranslation('devices');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [type, setType] = useState<CredentialType>('ACCESS_TOKEN');
  const [credentialId, setCredentialId] = useState('');
  const [secret, setSecret] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [version, reload] = useReload();
  // These are device credentials, not the operator's own login, so suppress
  // password-manager autofill/save on the fields (see the hook for the mechanism).
  const { noAutofill, rearm } = useNoAutofill();

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
    rearm();
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
    setSubmitting(true);
    try {
      await createDeviceCredential({
        token: crypto.randomUUID(),
        deviceToken,
        credentialType: type,
        credentialId: id,
        credentialValue: needsSecret(type) ? secret : undefined,
        enabled: true,
      });
      toast(t('credentialAdded'));
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
              <Input
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder="••••••••"
                {...noAutofill}
              />
            </FormField>
          </div>
        )}

        {type === 'X509_CERTIFICATE' && (
          <FormField label={t('certificateIdLabel')} description={t('certificateIdHint')}>
            <Input
              value={credentialId}
              onChange={(e) => setCredentialId(e.target.value)}
              placeholder={t('certificateIdPlaceholder')}
              {...noAutofill}
            />
          </FormField>
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
                  <div className="flex items-center gap-1.5">
                    <span className="font-mono text-xs text-foreground">{c.credentialId}</span>
                    <button
                      type="button"
                      onClick={() => copy(c.credentialId)}
                      className="text-muted-foreground transition-colors hover:text-foreground"
                      aria-label={t('copyIdAriaLabel')}
                    >
                      <Copy size={13} />
                    </button>
                  </div>
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
