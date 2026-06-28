// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { MultiSelect } from '@/components/ui/multi-select';
import type { ComboboxOption } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { listAuthorities, createRole, updateRole, type AdminRole } from '@/lib/api/admin';
import { errMessage } from '@/routes/common';

type Scope = 'system' | 'tenant';

// RoleForm creates a role (role absent) or edits one (role present, with its
// scope + token fixed). It loads the authority vocabulary itself so both the new
// and detail pages can render it without duplicating that wiring.
export function RoleForm({ role, onDone }: { role?: AdminRole; onDone: (message: string) => void }) {
  const editing = role != null;
  const { data: authorityVocab } = useQuery(listAuthorities, []);

  // Offer the known authority vocabulary as a checklist; "*" is the full-access
  // super-authority, called out so it isn't granted by accident.
  const authorityOptions = useMemo<ComboboxOption[]>(
    () =>
      (authorityVocab ?? []).map((a) =>
        a === '*' ? { value: '*', label: '*', description: 'Full access (super-authority)' } : { value: a },
      ),
    [authorityVocab],
  );

  const [scope, setScope] = useState<Scope>((role?.scope as Scope) ?? 'tenant');
  const [token, setToken] = useState(role?.token ?? '');
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [authorities, setAuthorities] = useState<string[]>(role ? [...role.authorities] : []);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateRole(role.scope, role.token, {
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities,
        });
        onDone(`Role “${role.token}” updated`);
      } else {
        await createRole({
          scope,
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities,
        });
        onDone(`Role “${token.trim()}” created`);
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField label="Scope">
        <div className="flex gap-2">
          {(['tenant', 'system'] as Scope[]).map((s) => (
            <Button
              key={s}
              type="button"
              variant={scope === s ? 'default' : 'outline'}
              size="sm"
              disabled={editing}
              onClick={() => setScope(s)}
            >
              {s}
            </Button>
          ))}
        </div>
      </FormField>
      <FormField label="Token" htmlFor="r-token">
        <Input
          id="r-token"
          value={token}
          disabled={editing}
          placeholder="operator"
          onChange={(e) => setToken(e.target.value)}
        />
      </FormField>
      <FormField label="Name" htmlFor="r-name">
        <Input id="r-name" value={name} placeholder="Operator" onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="r-desc">
        <Input id="r-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <FormField
        label="Authorities"
        htmlFor="r-auths"
        description='The capabilities this role grants. Use "*" for full access.'
      >
        <MultiSelect
          id="r-auths"
          options={authorityOptions}
          value={authorities}
          onChange={setAuthorities}
          placeholder="Select authorities…"
          searchPlaceholder="Filter authorities…"
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create role'}
        </Button>
      </div>
    </div>
  );
}
