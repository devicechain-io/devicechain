// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, MoreHorizontal, Plus } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { IconButton } from '@/components/ui/icon-button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { MultiSelect } from '@/components/ui/multi-select';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { ErrorBanner } from '@/components/ui/error-banner';
import { CopyToken } from '@/components/ui/copy-token';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { SectionPanel } from '@/components/ui/section-panel';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import type { ComboboxOption } from '@/components/ui/combobox';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { FormDrawer } from '@/components/registry';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import { formatTime } from '@/lib/utils';
import { useReload } from '@/routes/common';
import { listCustomers } from '@/lib/api/customers';
import { listAccessScopeGroups } from '@/lib/api/device-management';
import { customerAnchor, groupAnchor, listTenantRoles } from '@/lib/api/members';
import {
  createServiceAccount,
  deleteServiceAccount,
  listServiceAccounts,
  rotateServiceAccountSecret,
  setServiceAccountAccess,
  setServiceAccountEnabled,
  type ServiceAccount,
  type ServiceAccountCredentials,
} from '@/lib/api/service-accounts';
import { ScopeBadges, scopeRefs, useScopeNames } from '@/routes/members/ScopeBadges';
import { MAX_SCOPE_ENTRIES } from '@/routes/members/MemberAccessSheet';
import { memberErrorMessage } from '@/routes/members/memberErrors';

const PAGE_SIZE = 50;
const CUSTOMER_PAGE = { pageNumber: 1, pageSize: 1000 };
/** The server's bounds on a rotation's overlap (hours) and a secret's lifetime (days). */
export const MAX_OVERLAP_HOURS = 168;
export const DEFAULT_OVERLAP_HOURS = 24;

type Sheet =
  | { kind: 'create' }
  | { kind: 'edit'; account: ServiceAccount }
  | { kind: 'rotate'; account: ServiceAccount }
  | { kind: 'issued'; credentials: ServiceAccountCredentials };

const createSheet = (): Sheet => ({ kind: 'create' });
const editSheet = (account: ServiceAccount): Sheet => ({ kind: 'edit', account });
const rotateSheet = (account: ServiceAccount): Sheet => ({ kind: 'rotate', account });

export default function ServiceAccountsPage() {
  const { t } = useTranslation('members');
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'member:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listServiceAccounts({ pageNumber, pageSize: PAGE_SIZE }),
    [pageNumber, version],
  );
  const accounts = data?.page.results ?? [];
  const names = useScopeNames(accounts.flatMap((a) => scopeRefs(a.scope)));
  const [sheet, setSheet] = useState<Sheet | null>(null);
  const { toast } = useToast();

  const close = () => setSheet(null);
  const issued = (credentials: ServiceAccountCredentials) => {
    reload();
    setSheet({ kind: 'issued', credentials });
  };

  return (
    <PageShell
      title={t('saTitle')}
      description={t('saDescription')}
      action={
        canWrite && (
          <Button onClick={() => setSheet(createSheet())} data-shot="new-button">
            <Plus size={16} /> {t('saNewButton')}
          </Button>
        )
      }
    >
      {sheet?.kind === 'create' && (
        <FormDrawer shot="service-account-new" open onOpenChange={(o) => !o && close()} title={t('saNewTitle')} description={t('saNewDescription')}>
          <AccountForm onIssued={issued} />
        </FormDrawer>
      )}
      {sheet?.kind === 'edit' && (
        <FormDrawer
          shot="service-account-access"
          open
          onOpenChange={(o) => !o && close()}
          title={t('saEditTitle', { token: sheet.account.token })}
          description={t('saEditDescription')}
        >
          <AccountForm
            account={sheet.account}
            onSaved={() => {
              toast(t('saSavedToast', { token: sheet.account.token }));
              close();
              reload();
            }}
          />
        </FormDrawer>
      )}
      {sheet?.kind === 'rotate' && (
        <FormDrawer
          shot="service-account-rotate"
          open
          onOpenChange={(o) => !o && close()}
          title={t('saRotateTitle', { token: sheet.account.token })}
          description={t('saRotateDescription')}
        >
          <RotateForm account={sheet.account} onIssued={issued} />
        </FormDrawer>
      )}
      {sheet?.kind === 'issued' && (
        <FormDrawer shot="service-account-secret" open onOpenChange={(o) => !o && close()} title={t('saIssuedTitle')}>
          <SecretIssued credentials={sheet.credentials} onDone={close} />
        </FormDrawer>
      )}
      <div className="space-y-6">
        {loading && !data ? (
          <LoadingState description={t('saLoading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : accounts.length === 0 ? (
          <EmptyState description={t('saEmpty')} />
        ) : (
          <>
            <DataTable>
              <DataTableHead>
                <DataTableHeaderCell>{t('saColAccount')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colRoles')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('colAccess')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('saColSecrets')}</DataTableHeaderCell>
                <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
                {canWrite && <DataTableHeaderCell> </DataTableHeaderCell>}
              </DataTableHead>
              <DataTableBody>
                {accounts.map((a) => (
                  <DataTableRow key={a.token}>
                    <DataTableCell>
                      <div className="flex flex-col">
                        <span className="font-medium text-foreground">{a.name || a.token}</span>
                        <span className="font-mono text-xs text-muted-foreground">{a.clientId}</span>
                      </div>
                    </DataTableCell>
                    <DataTableCell>
                      <div className="flex flex-wrap gap-1">
                        {a.roles.length === 0 ? (
                          <span className="text-sm text-muted-foreground">{t('noRoles')}</span>
                        ) : (
                          a.roles.map((r) => (
                            <Badge key={r.token} variant="outline">
                              {r.name || r.token}
                            </Badge>
                          ))
                        )}
                      </div>
                    </DataTableCell>
                    <DataTableCell>
                      <ScopeBadges refs={scopeRefs(a.scope)} names={names} />
                    </DataTableCell>
                    <DataTableCell>
                      <SecretsSummary account={a} />
                    </DataTableCell>
                    <DataTableCell>
                      {a.enabled ? (
                        <Badge variant="success">{t('common:enabled')}</Badge>
                      ) : (
                        <Badge variant="outline" className="text-muted-foreground">
                          {t('common:disabled')}
                        </Badge>
                      )}
                    </DataTableCell>
                    {canWrite && (
                      <DataTableCell className="text-right">
                        <AccountActions
                          account={a}
                          onEdit={() => setSheet(editSheet(a))}
                          onRotate={() => setSheet(rotateSheet(a))}
                          onChanged={reload}
                        />
                      </DataTableCell>
                    )}
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
            {data && (
              <Pagination
                pageNumber={pageNumber}
                pageSize={PAGE_SIZE}
                pagination={data.page.pagination}
                onPageChange={setPageNumber}
              />
            )}
          </>
        )}
        {data && <TokenEndpointPanel endpoint={data.tokenEndpoint} />}
      </div>
    </PageShell>
  );
}

/** The live secrets of an account and when the next one expires. */
function SecretsSummary({ account }: { account: ServiceAccount }) {
  const { t } = useTranslation('members');
  const live = account.secrets.filter((s) => s.active);
  if (live.length === 0) {
    return <Badge variant="warning">{t('saNoLiveSecret')}</Badge>;
  }
  const soonest = live.map((s) => s.expiresAt).sort()[0];
  return (
    <div className="flex flex-col text-sm">
      <span>{t('saLiveSecrets', { count: live.length, hints: live.map((s) => `…${s.hint}`).join(', ') })}</span>
      <span className="text-xs text-muted-foreground">{t('saExpires', { when: formatTime(soonest) })}</span>
    </div>
  );
}

function AccountActions({
  account,
  onEdit,
  onRotate,
  onChanged,
}: {
  account: ServiceAccount;
  onEdit: () => void;
  onRotate: () => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation('members');
  const { toast } = useToast();
  const confirm = useConfirm();
  const run = async (op: () => Promise<unknown>, done: string) => {
    try {
      await op();
      toast(done);
      onChanged();
    } catch (err) {
      toast(memberErrorMessage(err, t), 'error');
    }
  };
  const remove = async () => {
    if (
      !(await confirm({
        title: t('saDeleteTitle'),
        description: t('saDeleteConfirm', { token: account.token }),
        confirmLabel: t('saDeleteButton'),
        destructive: true,
      }))
    )
      return;
    await run(() => deleteServiceAccount(account.token), t('saDeletedToast', { token: account.token }));
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <IconButton label={t('actionsFor', { email: account.token })} size="sm">
          <MoreHorizontal size={16} />
        </IconButton>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={onEdit}>{t('editAccess')}</DropdownMenuItem>
        <DropdownMenuItem onClick={onRotate}>{t('saRotate')}</DropdownMenuItem>
        <DropdownMenuItem
          onClick={() =>
            void run(
              () => setServiceAccountEnabled(account.token, !account.enabled),
              t(account.enabled ? 'saDisabledToast' : 'saEnabledToast', { token: account.token }),
            )
          }
        >
          {account.enabled ? t('disableMember') : t('enableMember')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => void remove()} className="text-destructive">
          {t('saDeleteButton')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

type ScopeMode = 'all' | 'limited';

/** Create an account (no `account`) or change an existing one's roles and scope. */
export function AccountForm({
  account,
  onIssued,
  onSaved,
}: {
  account?: ServiceAccount;
  onIssued?: (c: ServiceAccountCredentials) => void;
  onSaved?: () => void;
}) {
  const { t } = useTranslation('members');
  const initialCustomers = account ? account.scope.filter((a) => a.kind === 'CUSTOMER').map((a) => a.token) : [];
  const initialGroups = account ? account.scope.filter((a) => a.kind === 'GROUP').map((a) => a.token) : [];
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [roles, setRoles] = useState<string[]>(account ? account.roles.map((r) => r.token) : []);
  const [scopeMode, setScopeMode] = useState<ScopeMode>(initialCustomers.length + initialGroups.length > 0 ? 'limited' : 'all');
  const [customers, setCustomers] = useState<string[]>(initialCustomers);
  const [groups, setGroups] = useState<string[]>(initialGroups);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const { data: roleList } = useQuery(listTenantRoles, []);
  const { data: customerPage } = useQuery(() => listCustomers(CUSTOMER_PAGE), []);
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
    return [...known, ...customers.filter((c) => !known.some((k) => k.value === c)).map((c) => ({ value: c }))];
  }, [customerPage, customers]);
  const groupOptions = useMemo<ComboboxOption[]>(() => {
    const known = (accessGroups ?? []).map((g) => ({ value: g.token, label: g.name || g.token }));
    return [...known, ...groups.filter((g) => !known.some((k) => k.value === g)).map((g) => ({ value: g }))];
  }, [accessGroups, groups]);

  const limited = scopeMode === 'limited';
  const scope = limited ? [...customers.map(customerAnchor), ...groups.map(groupAnchor)] : [];
  const scopeIncomplete = limited && customers.length + groups.length === 0;
  const scopeTooLong = limited && customers.length + groups.length > MAX_SCOPE_ENTRIES;
  const scopeOptions = [
    { value: 'all' as const, label: t('scopeWholeTenant') },
    { value: 'limited' as const, label: t('scopeLimited') },
  ];

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (account) {
        await setServiceAccountAccess(account.token, roles, scope);
        onSaved?.();
      } else {
        const res = await createServiceAccount({
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          roleTokens: roles,
          scope,
        });
        onIssued?.(res);
      }
    } catch (err) {
      setFormError(memberErrorMessage(err, t, { CONFLICT: 'members:saErrorTokenTaken', NOT_FOUND: 'members:errorUnknownRoleOrScope' }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {!account && (
        <>
          <FormField label={t('saTokenLabel')} htmlFor="sa-token" description={t('saTokenDescription')}>
            <Input id="sa-token" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} />
          </FormField>
          <FormField label={t('saNameLabel')} htmlFor="sa-name">
            <Input id="sa-name" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <FormField label={t('saDescriptionLabel')} htmlFor="sa-description">
            <Input id="sa-description" autoComplete="off" value={description} onChange={(e) => setDescription(e.target.value)} />
          </FormField>
        </>
      )}
      <FormField label={t('rolesLabel')} htmlFor="sa-roles" description={t('saRolesDescription')}>
        <MultiSelect
          id="sa-roles"
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
          <FormField label={t('scopeCustomersLabel')} htmlFor="sa-customers">
            <MultiSelect
              id="sa-customers"
              options={customerOptions}
              value={customers}
              onChange={setCustomers}
              placeholder={t('scopeCustomersPlaceholder')}
              searchPlaceholder={t('scopeCustomersSearchPlaceholder')}
              emptyMessage={t('scopeCustomersEmpty')}
            />
          </FormField>
          <FormField label={t('scopeGroupsLabel')} htmlFor="sa-groups">
            <MultiSelect
              id="sa-groups"
              options={groupOptions}
              value={groups}
              onChange={setGroups}
              placeholder={t('scopeGroupsPlaceholder')}
              searchPlaceholder={t('scopeGroupsSearchPlaceholder')}
              emptyMessage={t('scopeGroupsEmpty')}
            />
          </FormField>
          <p className={scopeTooLong ? 'text-sm text-destructive' : 'text-sm text-muted-foreground'}>
            {scopeTooLong ? t('scopeTooLong', { max: MAX_SCOPE_ENTRIES }) : t('scopeLimitedHint', { max: MAX_SCOPE_ENTRIES })}
          </p>
        </>
      )}
      <Button onClick={submit} loading={busy} disabled={busy || scopeIncomplete || scopeTooLong || (!account && !token.trim())}>
        {account ? t('common:saveChanges') : t('saCreateButton')}
      </Button>
    </div>
  );
}

function RotateForm({ account, onIssued }: { account: ServiceAccount; onIssued: (c: ServiceAccountCredentials) => void }) {
  const { t } = useTranslation('members');
  const [overlap, setOverlap] = useState(String(DEFAULT_OVERLAP_HOURS));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const hours = Number(overlap);
  const valid = overlap.trim() !== '' && Number.isInteger(hours) && hours >= 0 && hours <= MAX_OVERLAP_HOURS;
  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      onIssued(await rotateServiceAccountSecret(account.token, hours));
    } catch (err) {
      setFormError(memberErrorMessage(err, t));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('saOverlapLabel')}
        htmlFor="sa-overlap"
        description={t('saOverlapDescription', { max: MAX_OVERLAP_HOURS })}
      >
        <Input id="sa-overlap" type="number" min={0} max={MAX_OVERLAP_HOURS} value={overlap} onChange={(e) => setOverlap(e.target.value)} />
      </FormField>
      <Button onClick={submit} loading={busy} disabled={busy || !valid}>
        {t('saRotate')}
      </Button>
    </div>
  );
}

// SecretIssued shows a newly minted client secret. It is the ONLY time it can be seen: the
// server keeps a digest, so there is nothing to read back, and losing it means rotating.
export function SecretIssued({ credentials, onDone }: { credentials: ServiceAccountCredentials; onDone: () => void }) {
  const { t } = useTranslation('members');
  return (
    <div className="space-y-4" data-shot="service-account-secret">
      <FormField label={t('saClientIdLabel')} htmlFor="sa-client-id">
        <div className="flex items-center gap-2">
          <Input id="sa-client-id" readOnly value={credentials.clientId} className="font-mono text-xs" />
          <CopyToken value={credentials.clientId} display="" />
        </div>
      </FormField>
      <FormField label={t('saClientSecretLabel')} htmlFor="sa-client-secret">
        <div className="flex items-center gap-2">
          <Input
            id="sa-client-secret"
            readOnly
            value={credentials.clientSecret}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <CopyToken value={credentials.clientSecret} display="" />
        </div>
      </FormField>
      {credentials.tokenEndpoint && (
        <FormField label={t('saTokenEndpointLabel')} htmlFor="sa-endpoint">
          <Input id="sa-endpoint" readOnly value={credentials.tokenEndpoint} className="font-mono text-xs" />
        </FormField>
      )}
      <p className="flex items-start gap-2 text-sm text-muted-foreground">
        <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warning" />
        <span>{t('saSecretShownOnce')}</span>
      </p>
      <Button onClick={onDone}>{t('done')}</Button>
    </div>
  );
}

function TokenEndpointPanel({ endpoint }: { endpoint: string | null }) {
  const { t } = useTranslation('members');
  return (
    <SectionPanel title={t('saUsingTitle')} description={t('saUsingDescription')}>
      {endpoint ? (
        <pre className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">
          {`curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \\\n  -d grant_type=client_credentials \\\n  ${endpoint}`}
        </pre>
      ) : (
        <p className="text-sm text-muted-foreground">{t('saNoTokenEndpoint')}</p>
      )}
    </SectionPanel>
  );
}
