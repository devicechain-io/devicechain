// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { mayWrite } from '@/lib/scope';
import { useReload } from '@/routes/common';
import { listAllMembers } from '@/lib/api/members';
import { MemberAccessSheet, editMode, inviteMode, type MemberAccessMode } from '@/routes/members/MemberAccessSheet';
import { MemberTable } from '@/routes/members/MembersPage';
import { scopeRefs, useScopeNames } from '@/routes/members/ScopeBadges';

// CustomerUsersPanel shows the people whose access is limited to this customer — the
// customer's own users — and invites a new one with the scope already set to it.
//
// The members query filters by email and status only, so this reads every member and keeps
// those whose scope names this customer. A member limited to several customers appears on
// each of their pages.
export function CustomerUsersPanel({ customerToken }: { customerToken: string }) {
  const { t } = useTranslation('members');
  const { claims } = useAuth();
  const canWrite = mayWrite(claims, 'member:write');
  const { toast } = useToast();
  const [version, reload] = useReload();
  const [sheet, setSheet] = useState<MemberAccessMode | null>(null);
  const { data, loading, error } = useQuery(listAllMembers, [version]);
  const members = (data ?? []).filter((m) =>
    m.scope.some((a) => a.kind === 'CUSTOMER' && a.token === customerToken),
  );
  const names = useScopeNames(members.flatMap((m) => scopeRefs(m.scope)));

  return (
    <div className="space-y-4">
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
      <div className="flex items-start justify-between gap-4">
        <p className="text-sm text-muted-foreground">{t('customerUsersDescription')}</p>
        {canWrite && (
          <Button size="sm" onClick={() => setSheet(inviteMode([customerToken]))}>
            <Plus size={14} /> {t('inviteCustomerUser')}
          </Button>
        )}
      </div>
      {loading && !data ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : members.length === 0 ? (
        <EmptyState description={t('customerUsersEmpty')} />
      ) : (
        <MemberTable
          members={members}
          names={names}
          canWrite={canWrite}
          self={claims?.username ?? ''}
          onEdit={(m) => setSheet(editMode(m))}
          onChanged={reload}
        />
      )}
    </div>
  );
}
