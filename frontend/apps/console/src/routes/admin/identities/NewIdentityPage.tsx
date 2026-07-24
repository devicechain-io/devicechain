// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { listRoles, createIdentity } from '@/lib/api/admin';
import { errMessage } from '@/routes/common';

// toOptions turns a token+name record into combobox options.
function toOptions(items: { token: string; name?: string | null }[] | null | undefined): ComboboxOption[] {
  return (items ?? []).map((i) => ({
    value: i.token,
    label: i.name || i.token,
    description: i.name ? i.token : undefined,
  }));
}

export default function NewIdentityPage() {
  const { t } = useTranslation('identities');
  const navigate = useNavigate();
  const { toast } = useToast();
  const { data: systemRoles } = useQuery(() => listRoles('system'), []);
  const systemRoleOptions = useMemo(() => toOptions(systemRoles), [systemRoles]);

  const [email, setEmail] = useState('');
  const [password, setPasswordValue] = useState('');
  const [firstName, setFirstName] = useState('');
  const [lastName, setLastName] = useState('');
  const [roles, setRoles] = useState<string[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const normalized = email.trim().toLowerCase();
      await createIdentity({
        email: email.trim(),
        password,
        firstName: firstName.trim() || undefined,
        lastName: lastName.trim() || undefined,
        enabled: true,
        systemRoles: roles,
      });
      toast(t('identityCreatedToast', { email: email.trim() }));
      navigate(`/admin/identities/${encodeURIComponent(normalized)}`);
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <PageShell title={t('newIdentity')} description={t('newIdentityDescription')}>
      <SectionPanel>
        <div className="space-y-4">
          {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label={t('colEmail')} htmlFor="i-email">
              <Input id="i-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
            </FormField>
            <FormField label={t('passwordLabel')} htmlFor="i-pw">
              <Input id="i-pw" type="password" value={password} onChange={(e) => setPasswordValue(e.target.value)} />
            </FormField>
            <FormField label={t('firstNameLabel')} htmlFor="i-fn">
              <Input id="i-fn" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
            </FormField>
            <FormField label={t('lastNameLabel')} htmlFor="i-ln">
              <Input id="i-ln" value={lastName} onChange={(e) => setLastName(e.target.value)} />
            </FormField>
          </div>
          <FormField label={t('systemRoles')} htmlFor="i-sys" description={t('systemRolesFormDescription')}>
            <MultiSelect
              id="i-sys"
              options={systemRoleOptions}
              value={roles}
              onChange={setRoles}
              placeholder={t('selectSystemRolesPlaceholder')}
              searchPlaceholder={t('filterRolesPlaceholder')}
              emptyMessage={t('noSystemRolesMessage')}
            />
          </FormField>
          <div className="flex gap-2">
            <Button onClick={submit} loading={busy} disabled={busy || !email.trim() || !password}>
              {t('createIdentityButton')}
            </Button>
            <Button variant="ghost" onClick={() => navigate('/admin/identities')} disabled={busy}>
              {t('common:cancel')}
            </Button>
          </div>
        </div>
      </SectionPanel>
    </PageShell>
  );
}
