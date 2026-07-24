// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { MultiSelect } from '@/components/ui/multi-select';
import type { ComboboxOption } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import { listAuthorities, createRole, updateRole, type AdminRole } from '@/lib/api/admin';
import { errMessage } from '@/routes/common';

type Scope = 'system' | 'tenant';

// SCOPE_LABEL_KEY maps the role scope enum to its localized human-readable label.
// The raw values are also the wire tokens sent on create/update, so only the
// RENDERED text goes through the map — the values themselves are never translated.
const SCOPE_LABEL_KEY: Record<Scope, string> = {
  system: 'scopeSystem',
  tenant: 'scopeTenant',
};

// The scope picker's options, in display order. Hoisted out of the JSX below so
// the i18next lint rule (jsx-only mode) doesn't mistake the enum values for
// user-facing text — the rendered label for each comes from SCOPE_LABEL_KEY.
const SCOPES: Scope[] = ['tenant', 'system'];

// RoleForm creates a role (role absent) or edits one (role present, with its
// scope + token fixed). It loads the authority vocabulary itself so both the new
// and detail pages can render it without duplicating that wiring.
export function RoleForm({ role, onDone }: { role?: AdminRole; onDone: (message: string) => void }) {
  const { t } = useTranslation('roles');
  const editing = role != null;
  const [scope, setScope] = useState<Scope>((role?.scope as Scope) ?? 'tenant');

  // Ask for the vocabulary this role's SCOPE may grant, and re-ask when the scope
  // changes: an authority's tier and a role's scope must agree (ADR-065), so the
  // unscoped list would offer a tenant role `ai:admin` and the operator would learn
  // the rule from a rejected save.
  const { data: authorityVocab } = useQuery(() => listAuthorities(scope), [scope]);

  // "*" is the full-access super-authority, called out so it isn't granted by
  // accident. It is offered at either scope and means "everything at this scope".
  const authorityOptions = useMemo<ComboboxOption[]>(
    () =>
      (authorityVocab ?? []).map((a) =>
        a === '*'
          ? {
              value: '*',
              label: '*',
              description: t('fullAccessDescription', { scope: t(SCOPE_LABEL_KEY[scope]) }),
            }
          : { value: a },
      ),
    [authorityVocab, scope, t],
  );
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
        onDone(t('roleUpdatedToast', { token: role.token }));
      } else {
        await createRole({
          scope,
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          authorities,
        });
        onDone(t('roleCreatedToast', { token: token.trim() }));
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
      <FormField label={t('scopeLabel')}>
        <div className="flex gap-2">
          {SCOPES.map((s) => (
            <Button
              key={s}
              type="button"
              variant={scope === s ? 'default' : 'outline'}
              size="sm"
              disabled={editing}
              onClick={() => {
                setScope(s);
                // The two scopes' vocabularies are disjoint apart from "*" (ADR-065),
                // so anything picked for the old scope would be refused on save. Drop
                // it here rather than carry an invalid selection into a save error.
                setAuthorities((prev) => prev.filter((a) => a === '*'));
              }}
            >
              {t(SCOPE_LABEL_KEY[s])}
            </Button>
          ))}
        </div>
      </FormField>
      <FormField label={t('common:colToken')} htmlFor="r-token">
        {editing ? (
          <Input id="r-token" value={token} disabled />
        ) : (
          <TokenField
            id="r-token"
            entityType="role"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('tokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="r-name">
        <Input id="r-name" value={name} placeholder={t('namePlaceholder')} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="r-desc">
        <Input id="r-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <FormField label={t('authoritiesLabel')} htmlFor="r-auths" description={t('authoritiesDescription')}>
        <MultiSelect
          id="r-auths"
          options={authorityOptions}
          value={authorities}
          onChange={setAuthorities}
          placeholder={t('selectAuthoritiesPlaceholder')}
          searchPlaceholder={t('filterAuthoritiesPlaceholder')}
          emptyMessage={t('noAuthoritiesMessage')}
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? t('common:saveChanges') : t('createRoleButton')}
        </Button>
      </div>
    </div>
  );
}
