// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a connection SERVES: the tenants its sign-ins may enter, and the email domains it
// speaks for. Both are access-widening, so each is its own deliberate save/confirm.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { listTenants } from '@/lib/api/admin';
import {
  addIdpDomain,
  removeIdpDomain,
  setIdpConnectionTenants,
  type IdpConnection,
} from '@/lib/api/identity-providers';
import { useQuery } from '@/lib/hooks/use-query';
import { Button } from '@/components/ui/button';
import { useConfirm } from '@/components/ui/confirm-dialog';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { MultiSelect } from '@/components/ui/multi-select';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { formatTime } from '@/lib/utils';
import { errMessage } from '@/routes/common';
import { idpErrorMessage } from './idpError';
import '@/i18n/lazy/identityProviders';

interface PanelProps {
  connection: IdpConnection;
  reading: boolean;
  onChanged: () => void;
}

export function TenantsPanel({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const { data: tenants } = useQuery(listTenants, []);
  const [selected, setSelected] = useState<string[]>(c.tenants);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (c.kind === 'instance') {
    return (
      <SectionPanel title={t('tenantsTitle')}>
        <p className="text-sm text-muted-foreground">{t('instanceServesNoTenants')}</p>
      </SectionPanel>
    );
  }

  // A tenant already served but missing from the catalog (deleted since) must stay visible
  // so it can be removed, not silently dropped from the picker.
  const known = new Set((tenants ?? []).map((x) => x.token));
  const options = [
    ...(tenants ?? []).map((x) => ({ value: x.token, label: x.name || x.token })),
    ...c.tenants.filter((tok) => !known.has(tok)).map((tok) => ({ value: tok, label: tok })),
  ];
  const dirty =
    selected.length !== c.tenants.length || selected.some((tok) => !c.tenants.includes(tok));

  const save = async () => {
    setError(null);
    setBusy(true);
    try {
      await setIdpConnectionTenants(c.token, selected);
      toast(t('tenantsSavedToast'));
      onChanged();
    } catch (err) {
      setError(idpErrorMessage(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('tenantsTitle')} description={t('tenantsDescription')}>
      <div className="space-y-3">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        <FormField label={t('servedTenantsLabel')} htmlFor="idp-tenants">
          <MultiSelect
            id="idp-tenants"
            options={options}
            value={selected}
            onChange={setSelected}
            placeholder={t('servedTenantsPlaceholder')}
          />
        </FormField>
        <Button onClick={() => void save()} loading={busy} disabled={busy || reading || !dirty}>
          {t('saveTenantsButton')}
        </Button>
      </div>
    </SectionPanel>
  );
}

export function DomainsPanel({ connection: c, reading, onChanged }: PanelProps) {
  const { t } = useTranslation('identityProviders');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [domain, setDomain] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const add = async () => {
    setError(null);
    const value = domain.trim().toLowerCase();
    if (value === '') return setError(t('domainRequiredError'));
    setBusy(true);
    try {
      await addIdpDomain(c.token, value);
      setDomain('');
      toast(t('domainAddedToast', { domain: value }));
      onChanged();
    } catch (err) {
      // Freemail providers and public suffixes are refused, and a domain belongs to one
      // connection; the server says which.
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (value: string) => {
    if (
      !(await confirm({
        title: t('removeDomainTitle'),
        description: t('removeDomainDescription', { domain: value }),
        confirmLabel: t('removeButton'),
      }))
    )
      return;
    setError(null);
    setBusy(true);
    try {
      await removeIdpDomain(c.token, value);
      toast(t('domainRemovedToast', { domain: value }));
      onChanged();
    } catch (err) {
      setError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SectionPanel title={t('domainsTitle')} description={t('domainsDescription')}>
      <div className="space-y-4">
        {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
        {c.domains.length === 0 ? (
          <EmptyState description={t('emptyDomains')} />
        ) : (
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colDomain')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colVerifiedBy')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colVerifiedAt')}</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {c.domains.map((d) => (
                <DataTableRow key={d.domain}>
                  <DataTableCell className="font-medium text-foreground">{d.domain}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{d.verifiedBy}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatTime(d.verifiedAt)}</DataTableCell>
                  <DataTableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy || reading}
                      aria-label={t('removeDomainAria', { domain: d.domain })}
                      onClick={() => void remove(d.domain)}
                    >
                      <Trash2 size={14} /> {t('removeButton')}
                    </Button>
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
        <div className="flex items-end gap-2">
          <div className="flex-1">
            <FormField label={t('addDomainLabel')} htmlFor="idp-domain">
              <Input
                id="idp-domain"
                value={domain}
                placeholder={t('domainPlaceholder')}
                onChange={(e) => setDomain(e.target.value)}
              />
            </FormField>
          </div>
          <Button onClick={() => void add()} loading={busy} disabled={busy || reading}>
            <Plus size={16} /> {t('addDomainButton')}
          </Button>
        </div>
      </div>
    </SectionPanel>
  );
}
