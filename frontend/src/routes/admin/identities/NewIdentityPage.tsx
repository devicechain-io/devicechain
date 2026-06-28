// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
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
import { BackLink, errMessage } from '@/routes/common';

// toOptions turns a token+name record into combobox options.
function toOptions(items: { token: string; name?: string | null }[] | null | undefined): ComboboxOption[] {
  return (items ?? []).map((i) => ({
    value: i.token,
    label: i.name || i.token,
    description: i.name ? i.token : undefined,
  }));
}

export default function NewIdentityPage() {
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
      toast(`Identity “${email.trim()}” created`);
      navigate(`/admin/identities/${encodeURIComponent(normalized)}`);
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <PageShell
      title="New identity"
      description="An email-keyed global principal. Add tenant memberships after creating it."
      action={<BackLink to="/admin/identities">Identities</BackLink>}
    >
      <SectionPanel>
        <div className="space-y-4">
          {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label="Email" htmlFor="i-email">
              <Input id="i-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
            </FormField>
            <FormField label="Password" htmlFor="i-pw">
              <Input id="i-pw" type="password" value={password} onChange={(e) => setPasswordValue(e.target.value)} />
            </FormField>
            <FormField label="First name" htmlFor="i-fn">
              <Input id="i-fn" value={firstName} onChange={(e) => setFirstName(e.target.value)} />
            </FormField>
            <FormField label="Last name" htmlFor="i-ln">
              <Input id="i-ln" value={lastName} onChange={(e) => setLastName(e.target.value)} />
            </FormField>
          </div>
          <FormField
            label="System roles"
            htmlFor="i-sys"
            description="System roles gate the admin API (e.g. superuser). Leave empty for none."
          >
            <MultiSelect
              id="i-sys"
              options={systemRoleOptions}
              value={roles}
              onChange={setRoles}
              placeholder="Select system roles…"
              searchPlaceholder="Filter roles…"
              emptyMessage="No system roles defined."
            />
          </FormField>
          <div className="flex gap-2">
            <Button onClick={submit} loading={busy} disabled={busy || !email.trim() || !password}>
              Create identity
            </Button>
            <Button variant="ghost" onClick={() => navigate('/admin/identities')} disabled={busy}>
              Cancel
            </Button>
          </div>
        </div>
      </SectionPanel>
    </PageShell>
  );
}
