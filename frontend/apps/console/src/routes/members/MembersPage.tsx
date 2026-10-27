// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { MoreHorizontal, Plus, ShieldCheck } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { IconButton } from '@/components/ui/icon-button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { SectionPanel } from '@/components/ui/section-panel';
import { Pagination } from '@/components/ui/pagination';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
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
import {
  listInvitations,
  listMembers,
  reissueInvitation,
  removeMember,
  revokeInvitation,
  setMemberEnabled,
  type Member,
} from '@/lib/api/members';
import { MemberAccessSheet, InvitationIssued, editMode, inviteMode, type MemberAccessMode } from './MemberAccessSheet';
import { ScopeBadges, scopeRefs, useScopeNames } from './ScopeBadges';
import { memberErrorMessage } from './memberErrors';

const PAGE_SIZE = 50;

export default function MembersPage() {
  const { t } = useTranslation('members');
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'member:write');
  const [pageNumber, setPageNumber] = useState(1);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(
    () => listMembers({ pageNumber, pageSize: PAGE_SIZE }),
    [pageNumber, version],
  );
  const members = data?.results ?? [];
  const names = useScopeNames(members.flatMap((m) => scopeRefs(m.scope)));
  const [sheet, setSheet] = useState<MemberAccessMode | null>(null);
  const { toast } = useToast();

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      action={
        canWrite && (
          <Button onClick={() => setSheet(inviteMode())} data-shot="new-button">
            <Plus size={16} /> {t('inviteButton')}
          </Button>
        )
      }
    >
      {sheet && (
        <MemberAccessSheet
          open
          onOpenChange={(open) => !open && setSheet(null)}
          mode={sheet}
          onDone={(message) => {
            toast(message);
            setSheet(null);
            reload();
          }}
        />
      )}
      <div className="space-y-6">
        {loading && !data ? (
          <LoadingState description={t('loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : members.length === 0 ? (
          <EmptyState description={t('empty')} />
        ) : (
          <>
            <MemberTable
              members={members}
              names={names}
              canWrite={canWrite}
              self={claims?.username ?? ''}
              onEdit={(m) => setSheet(editMode(m))}
              onChanged={reload}
            />
            {data && (
              <Pagination
                pageNumber={pageNumber}
                pageSize={PAGE_SIZE}
                pagination={data.pagination}
                onPageChange={setPageNumber}
              />
            )}
          </>
        )}
        <InvitationsPanel version={version} />
      </div>
    </PageShell>
  );
}

/** A member's status, in the order that decides what can be done with them. */
export function MemberStatus({ member }: { member: Member }) {
  const { t } = useTranslation('members');
  return (
    <div className="flex flex-wrap items-center gap-1">
      {member.invitationPending ? (
        <Badge variant="warning">{t('statusInvited')}</Badge>
      ) : member.enabled ? (
        <Badge variant="success">{t('common:enabled')}</Badge>
      ) : (
        <Badge variant="outline" className="text-muted-foreground">
          {t('common:disabled')}
        </Badge>
      )}
      {member.managedByIdentityProvider && (
        <Badge variant="secondary" className="gap-1" title={t('managedByIdpHint')}>
          <ShieldCheck size={12} /> {t('managedByIdp')}
        </Badge>
      )}
    </div>
  );
}

export function memberName(m: Member): string {
  return [m.firstName, m.lastName].filter(Boolean).join(' ');
}

// MemberTable is the members list with its row actions. Shared with a customer's Users tab,
// which shows the same rows filtered to the members limited to that customer.
export function MemberTable({
  members,
  names,
  canWrite,
  self,
  onEdit,
  onChanged,
}: {
  members: Member[];
  names: Record<string, string>;
  canWrite: boolean;
  /** The signed-in member's email: their own row offers no actions (the server refuses them). */
  self: string;
  onEdit: (m: Member) => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation('members');
  return (
    <DataTable>
      <DataTableHead>
        <DataTableHeaderCell>{t('colMember')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('colRoles')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('colAccess')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('colTwoFactor')}</DataTableHeaderCell>
        {canWrite && <DataTableHeaderCell> </DataTableHeaderCell>}
      </DataTableHead>
      <DataTableBody>
        {members.map((m) => (
          <DataTableRow key={m.email}>
            <DataTableCell>
              <div className="flex flex-col">
                <span className="font-medium text-foreground">{memberName(m) || m.email}</span>
                {memberName(m) && <span className="text-xs text-muted-foreground">{m.email}</span>}
              </div>
            </DataTableCell>
            <DataTableCell>
              <div className="flex flex-wrap gap-1">
                {m.roles.length === 0 ? (
                  <span className="text-sm text-muted-foreground">{t('noRoles')}</span>
                ) : (
                  m.roles.map((r) => (
                    <Badge key={r.token} variant="outline">
                      {r.name || r.token}
                    </Badge>
                  ))
                )}
              </div>
            </DataTableCell>
            <DataTableCell>
              <ScopeBadges refs={scopeRefs(m.scope)} names={names} />
            </DataTableCell>
            <DataTableCell>
              <MemberStatus member={m} />
            </DataTableCell>
            <DataTableCell>
              {/* A pending invitation has no account yet to hold a second factor. */}
              {m.invitationPending ? (
                <span className="text-sm text-muted-foreground">{t('twoFactorNone')}</span>
              ) : (
                <Badge variant={m.mfaEnabled ? 'success' : 'outline'} data-shot="member-mfa">
                  {m.mfaEnabled ? t('twoFactorOn') : t('twoFactorOff')}
                </Badge>
              )}
            </DataTableCell>
            {canWrite && (
              <DataTableCell className="text-right">
                {m.email.toLowerCase() !== self.toLowerCase() && (
                  <MemberActions member={m} onEdit={() => onEdit(m)} onChanged={onChanged} />
                )}
              </DataTableCell>
            )}
          </DataTableRow>
        ))}
      </DataTableBody>
    </DataTable>
  );
}

// The actions for one member. A membership the identity provider manages offers none:
// the server refuses every change to it (MANAGED_BY_IDP), and the provider's next sign-in
// would put it back anyway.
function MemberActions({ member, onEdit, onChanged }: { member: Member; onEdit: () => void; onChanged: () => void }) {
  const { t } = useTranslation('members');
  const { toast } = useToast();
  const confirm = useConfirm();
  const [reissued, setReissued] = useState<string | null>(null);

  if (member.managedByIdentityProvider) {
    return <span className="text-xs text-muted-foreground">{t('managedByIdpNoActions')}</span>;
  }

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
        title: t('removeTitle'),
        description: t('removeConfirm', { email: member.email }),
        confirmLabel: t('removeButton'),
        destructive: true,
      }))
    )
      return;
    await run(() => removeMember(member.email), t('removedToast', { email: member.email }));
  };

  const reissue = async () => {
    try {
      const res = await reissueInvitation(member.email);
      setReissued(res.invitationCode);
      onChanged();
    } catch (err) {
      toast(memberErrorMessage(err, t), 'error');
    }
  };

  return (
    <>
      {reissued && (
        <FormDrawer open onOpenChange={(open) => !open && setReissued(null)} title={t('reissueTitle')}>
          <InvitationIssued email={member.email} code={reissued} onDone={() => setReissued(null)} />
        </FormDrawer>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <IconButton label={t('actionsFor', { email: member.email })} size="sm">
            <MoreHorizontal size={16} />
          </IconButton>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={onEdit}>{t('editAccess')}</DropdownMenuItem>
          {member.invitationPending ? (
            <>
              <DropdownMenuItem onClick={() => void reissue()}>{t('reissueInvitation')}</DropdownMenuItem>
              <DropdownMenuItem
                onClick={() =>
                  void run(() => revokeInvitation(member.email), t('revokedToast', { email: member.email }))
                }
              >
                {t('revokeInvitation')}
              </DropdownMenuItem>
            </>
          ) : (
            <DropdownMenuItem
              onClick={() =>
                void run(
                  () => setMemberEnabled(member.email, !member.enabled),
                  t(member.enabled ? 'disabledToast' : 'enabledToast', { email: member.email }),
                )
              }
            >
              {member.enabled ? t('disableMember') : t('enableMember')}
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => void remove()} className="text-destructive">
            {t('removeButton')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}

// InvitationsPanel lists the tenant's invitations, newest first, so an admin can see which
// were accepted, which lapsed and which are still waiting.
function InvitationsPanel({ version }: { version: number }) {
  const { t } = useTranslation('members');
  const [pageNumber, setPageNumber] = useState(1);
  const { data, loading, error } = useQuery(
    () => listInvitations({ pageNumber, pageSize: PAGE_SIZE }),
    [pageNumber, version],
  );
  const rows = data?.results ?? [];
  const STATE_KEY: Record<string, string> = {
    PENDING: 'invitationPending',
    ACCEPTED: 'invitationAccepted',
    EXPIRED: 'invitationExpired',
    REVOKED: 'invitationRevoked',
  };
  return (
    <SectionPanel title={t('invitationsTitle')} description={t('invitationsDescription')}>
      {loading && !data ? (
        <LoadingState description={t('invitationsLoading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : rows.length === 0 ? (
        <EmptyState description={t('invitationsEmpty')} />
      ) : (
        <>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('colEmail')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('common:colStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colIssued')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('colExpires')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {rows.map((inv) => (
                <DataTableRow key={`${inv.email}-${inv.createdAt}`}>
                  <DataTableCell className="font-medium">{inv.email}</DataTableCell>
                  <DataTableCell>
                    <Badge variant={inv.state === 'PENDING' ? 'warning' : inv.state === 'ACCEPTED' ? 'success' : 'outline'}>
                      {t(STATE_KEY[inv.state] ?? 'invitationPending')}
                    </Badge>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatTime(inv.createdAt)}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatTime(inv.expiresAt)}</DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
          {data && (
            <Pagination
              pageNumber={pageNumber}
              pageSize={PAGE_SIZE}
              pagination={data.pagination}
              onPageChange={setPageNumber}
              className="mt-4"
            />
          )}
        </>
      )}
    </SectionPanel>
  );
}
