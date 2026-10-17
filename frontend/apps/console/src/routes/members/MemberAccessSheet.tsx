// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertTriangle } from 'lucide-react';
import { FormDrawer } from '@/components/registry';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { MultiSelect } from '@/components/ui/multi-select';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { ErrorBanner } from '@/components/ui/error-banner';
import { CopyToken } from '@/components/ui/copy-token';
import type { ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { listCustomers } from '@/lib/api/customers';
import { listAccessScopeGroups } from '@/lib/api/device-management';
import {
  customerAnchor,
  groupAnchor,
  inviteMember,
  listTenantRoles,
  setMemberAccess,
  setMemberRoles,
  setMemberScope,
  type Member,
} from '@/lib/api/members';
import { memberErrorMessage } from './memberErrors';

/** What the sheet is for: inviting someone (optionally limited to preset customers), or editing a member's access. */
export type MemberAccessMode =
  | { kind: 'invite'; presetCustomers?: string[] }
  | { kind: 'edit'; member: Member };

/** Open the sheet to invite someone, optionally limited to the given customers from the start. */
export function inviteMode(presetCustomers?: string[]): MemberAccessMode {
  return { kind: 'invite', presetCustomers };
}

/** Open the sheet to change a member's roles and scope. */
export function editMode(member: Member): MemberAccessMode {
  return { kind: 'edit', member };
}

/**
 * The link an invitee opens. The code rides the URL FRAGMENT, which a browser never sends
 * to a server: it cannot land in an access log, a proxy log or a Referer header on the way
 * to the console, and the console reads it from the address bar without sending it
 * anywhere but acceptInvitation.
 */
export function invitationLink(code: string): string {
  return `${window.location.origin}/invitation#code=${encodeURIComponent(code)}`;
}

type ScopeMode = 'all' | 'limited';

// The customers a member can be limited to, offered in one list. A tenant with more than
// the page shows can still type the token; the server refuses one that names nothing.
const CUSTOMER_PAGE = { pageNumber: 1, pageSize: 1000 };

/** The most entries one member's scope may name, customers and groups together (the server's limit). */
export const MAX_SCOPE_ENTRIES = 16;

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x) => b.includes(x));
}

export function MemberAccessSheet({
  open,
  onOpenChange,
  mode,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mode: MemberAccessMode;
  /** Called with a confirmation to show once the change is saved (or the invitation link has been shown). */
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation('members');
  const editing = mode.kind === 'edit' ? mode.member : null;
  const title = editing ? t('editAccessTitle', { email: editing.email }) : t('inviteTitle');
  return (
    <FormDrawer
      shot={editing ? 'member-access' : 'member-invite'}
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={editing ? t('editAccessDescription') : t('inviteDescription')}
    >
      {/* Rendered inside the sheet, which unmounts it when closed: every opening starts
          from the member's stored access, and a shown invitation link does not survive
          closing the sheet. */}
      <MemberAccessForm mode={mode} onDone={onDone} />
    </FormDrawer>
  );
}

function MemberAccessForm({ mode, onDone }: { mode: MemberAccessMode; onDone: (message: string) => void }) {
  const { t } = useTranslation('members');
  const editing = mode.kind === 'edit' ? mode.member : null;
  const initialRoles = useMemo(() => (editing ? editing.roles.map((r) => r.token) : []), [editing]);
  const initialCustomers = useMemo(
    () =>
      editing
        ? editing.scope.filter((a) => a.kind === 'CUSTOMER').map((a) => a.token)
        : mode.kind === 'invite'
          ? (mode.presetCustomers ?? [])
          : [],
    [editing, mode],
  );
  const initialGroups = useMemo(
    () => (editing ? editing.scope.filter((a) => a.kind === 'GROUP').map((a) => a.token) : []),
    [editing],
  );

  const [email, setEmail] = useState('');
  const [roles, setRoles] = useState<string[]>(initialRoles);
  const [scopeMode, setScopeMode] = useState<ScopeMode>(
    initialCustomers.length + initialGroups.length > 0 ? 'limited' : 'all',
  );
  const [customers, setCustomers] = useState<string[]>(initialCustomers);
  const [groups, setGroups] = useState<string[]>(initialGroups);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The one-time code, once an invitation has been issued. Held only in this component's
  // state: never stored, never logged, gone when the sheet closes.
  const [issued, setIssued] = useState<{ email: string; code: string } | null>(null);

  const { data: roleList } = useQuery(listTenantRoles, []);
  const { data: customerPage } = useQuery(() => listCustomers(CUSTOMER_PAGE), []);
  // Only the groups that are access scopes are offered: a member limited to any other group
  // would see nothing. The field's description says where a missing one is enabled.
  const { data: accessGroups } = useQuery(listAccessScopeGroups, []);

  const roleOptions = useMemo<ComboboxOption[]>(
    () =>
      (roleList ?? []).map((r) => ({
        value: r.token,
        label: r.name || r.token,
        description: r.owner === 'INSTANCE' ? t('roleOwnerInstance') : t('roleOwnerTenant'),
      })),
    [roleList, t],
  );
  const customerOptions = useMemo<ComboboxOption[]>(() => {
    const known = (customerPage?.results ?? []).map((c) => ({ value: c.token, label: c.name || c.token }));
    // A customer already in the scope stays selectable even if it is past the first page.
    const extra = customers.filter((c) => !known.some((k) => k.value === c)).map((c) => ({ value: c }));
    return [...known, ...extra];
  }, [customerPage, customers]);
  const groupOptions = useMemo<ComboboxOption[]>(() => {
    const known = (accessGroups ?? []).map((g) => ({ value: g.token, label: g.name || g.token }));
    // A group already in the scope stays shown even once it stops being an access scope, so
    // saving the sheet does not silently drop it; the member sees nothing through it until
    // it is enabled again.
    const extra = groups.filter((g) => !known.some((k) => k.value === g)).map((g) => ({ value: g }));
    return [...known, ...extra];
  }, [accessGroups, groups]);

  const limited = scopeMode === 'limited';
  const scope = limited ? [...customers.map(customerAnchor), ...groups.map(groupAnchor)] : [];
  const scopeIncomplete = limited && customers.length + groups.length === 0;
  const scopeTooLong = limited && customers.length + groups.length > MAX_SCOPE_ENTRIES;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (!editing) {
        const res = await inviteMember({ email: email.trim(), roleTokens: roles, scope });
        setIssued({ email: email.trim(), code: res.invitationCode });
        return;
      }
      const sameRoles = sameSet(roles, initialRoles);
      const sameScope =
        sameSet(limited ? customers : [], initialCustomers) && sameSet(limited ? groups : [], initialGroups);
      // When both change they go in ONE call, so a refusal of either leaves both unchanged
      // — the state the form still shows — instead of saving the roles and then failing on
      // the scope. A single change keeps its own call.
      if (!sameRoles && !sameScope) await setMemberAccess(editing.email, roles, scope);
      else if (!sameRoles) await setMemberRoles(editing.email, roles);
      else if (!sameScope) await setMemberScope(editing.email, scope);
      onDone(t('accessSavedToast', { email: editing.email }));
    } catch (err) {
      setFormError(memberErrorMessage(err, t, { CONFLICT: 'members:errorAlreadyMember', NOT_FOUND: 'members:errorUnknownRoleOrScope' }));
    } finally {
      setBusy(false);
    }
  };

  if (issued) {
    return <InvitationIssued email={issued.email} code={issued.code} onDone={() => onDone(t('invitedToast', { email: issued.email }))} />;
  }

  const scopeOptions = [
    { value: 'all' as const, label: t('scopeWholeTenant') },
    { value: 'limited' as const, label: t('scopeLimited') },
  ];

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {!editing && (
        <FormField label={t('emailLabel')} htmlFor="m-email">
          <Input
            id="m-email"
            type="email"
            autoComplete="off"
            value={email}
            placeholder={t('emailPlaceholder')}
            onChange={(e) => setEmail(e.target.value)}
          />
        </FormField>
      )}
      <FormField label={t('rolesLabel')} htmlFor="m-roles" description={t('rolesDescription')}>
        <MultiSelect
          id="m-roles"
          options={roleOptions}
          value={roles}
          onChange={setRoles}
          placeholder={t('rolesPlaceholder')}
          searchPlaceholder={t('rolesSearchPlaceholder')}
          emptyMessage={t('rolesEmpty')}
        />
      </FormField>
      <FormField label={t('scopeLabel')} description={t('scopeDescription')}>
        <SegmentedControl
          options={scopeOptions}
          value={scopeMode}
          onValueChange={setScopeMode}
          ariaLabel={t('scopeLabel')}
          tone="joined"
          size="sm"
        />
      </FormField>
      {limited && (
        <>
          <FormField label={t('scopeCustomersLabel')} htmlFor="m-customers" description={t('scopeCustomersDescription')}>
            <MultiSelect
              id="m-customers"
              options={customerOptions}
              value={customers}
              onChange={setCustomers}
              placeholder={t('scopeCustomersPlaceholder')}
              searchPlaceholder={t('scopeCustomersSearchPlaceholder')}
              emptyMessage={t('scopeCustomersEmpty')}
            />
          </FormField>
          <FormField label={t('scopeGroupsLabel')} htmlFor="m-groups" description={t('scopeGroupsDescription')}>
            <MultiSelect
              id="m-groups"
              options={groupOptions}
              value={groups}
              onChange={setGroups}
              placeholder={t('scopeGroupsPlaceholder')}
              searchPlaceholder={t('scopeGroupsSearchPlaceholder')}
              emptyMessage={t('scopeGroupsEmpty')}
            />
          </FormField>
          <p
            className={scopeTooLong ? 'text-sm text-destructive' : 'text-sm text-muted-foreground'}
            data-shot="member-scope-hint"
          >
            {scopeTooLong
              ? t('scopeTooLong', { max: MAX_SCOPE_ENTRIES })
              : t('scopeLimitedHint', { max: MAX_SCOPE_ENTRIES })}
          </p>
        </>
      )}
      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || scopeIncomplete || scopeTooLong || (!editing && (!email.trim() || roles.length === 0))}
        >
          {editing ? t('common:saveChanges') : t('inviteButton')}
        </Button>
      </div>
    </div>
  );
}

// InvitationIssued shows the one-time invitation link. It is the ONLY time the link can be
// seen: the server keeps a hash, so there is nothing to read back, and losing it means
// reissuing — which revokes this one.
export function InvitationIssued({ email, code, onDone }: { email: string; code: string; onDone: () => void }) {
  const { t } = useTranslation('members');
  const link = invitationLink(code);
  return (
    <div className="space-y-4" data-shot="invitation-link">
      <p className="text-sm text-foreground">{t('invitationIssued', { email })}</p>
      <FormField label={t('invitationLinkLabel')} htmlFor="m-link">
        <div className="flex items-center gap-2">
          <Input id="m-link" readOnly value={link} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
          <CopyToken value={link} display="" />
        </div>
      </FormField>
      <p className="flex items-start gap-2 text-sm text-muted-foreground">
        <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warning" />
        <span>{t('invitationShownOnce')}</span>
      </p>
      <Button onClick={onDone}>{t('done')}</Button>
    </div>
  );
}
