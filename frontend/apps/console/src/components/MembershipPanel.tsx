// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reusable group-membership surface, plugged into a group's detail page via
// the registry kit's `renderDetailExtra`. Membership is a group -> member edge
// (ADR-013), so one panel serves every group family by parameterizing the group
// and member entity types. Member display names are resolved from the candidate
// list (the Entity edge target only carries id + token).

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Trash2 } from 'lucide-react';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { MultiSelect } from '@/components/ui/multi-select';
import { LoadingState } from '@/components/ui/loading-state';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorState } from '@/components/ui/error-state';
import { Pagination } from '@/components/ui/pagination';
import { useQuery } from '@/lib/hooks/use-query';
import { listAll } from '@/lib/api/list-all';
import { useToast } from '@/components/ui/toast';
import { useReload, errMessage } from '@/routes/common';
import { listGroupMembers, addGroupMembers, removeGroupMembers } from '@/lib/api/relationships';

interface Candidate {
  token: string;
  name?: string | null;
}

export function MembershipPanel(props: Parameters<typeof MembershipContent>[0]) {
  return <MembershipContent key={JSON.stringify([props.groupType, props.groupToken, props.memberType])} {...props} />;
}

function MembershipContent({
  groupType,
  groupToken,
  memberType,
  memberI18nKey,
  loadCandidates,
}: {
  /** Backend entity-type token for the group — always "group" since ADR-061. */
  groupType: string;
  groupToken: string;
  /** Backend entity-type token for members, e.g. "asset". */
  memberType: string;
  /** Member family prefix in the `entities` catalog, e.g. "asset" — resolves the
   *  member-noun prose (`${memberI18nKey}AddPlaceholder`, `${memberI18nKey}EmptyInGroup`,
   *  `${memberI18nKey}AddedCount`). */
  memberI18nKey: string;
  /** Loads all member-type instances (add candidates + name resolution). */
  loadCandidates: () => Promise<Candidate[]>;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const e = (suffix: string, opts: Record<string, unknown> = {}) =>
    t(`entities:${memberI18nKey}${suffix}`, opts);
  const { toast } = useToast();
  const [version, reload] = useReload();
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [pageNumber, setPageNumber] = useState(1);
  const pageSize = 50;

  const { current: members, loading, error } = useQuery(
    () => listGroupMembers(groupType, groupToken, { pageNumber, pageSize }),
    [groupType, groupToken, pageNumber, version],
  );
  // Exclusion must cover every member, including those on another displayed page.
  // A failed complete read disables adding, while the paged member list still works.
  const { current: candidateData, loading: candidatesLoading, error: candidatesError } = useQuery(async () => {
    const [candidates, allMembers] = await Promise.all([
      loadCandidates(),
      listAll(page => listGroupMembers(groupType, groupToken, page)),
    ]);
    return { candidates, memberTokens: new Set(allMembers.results.map(edge => edge.target.token)) };
  }, [groupType, groupToken, memberType, version]);
  const candidates = candidateData?.candidates;

  const memberEdges = members?.results ?? [];

  const nameOf = useMemo(() => {
    const m = new Map((candidates ?? []).map((c) => [c.token, c.name || c.token]));
    return (token: string) => m.get(token) ?? token;
  }, [candidates]);

  const memberTokens = candidateData?.memberTokens;

  // Add-picker options: member-type instances that aren't already in the group.
  const options = useMemo(
    () =>
      (candidates ?? [])
        .filter((c) => !memberTokens?.has(c.token))
        .map((c) => ({
          value: c.token,
          label: c.name || c.token,
          description: c.name ? c.token : undefined,
        })),
    [candidates, memberTokens],
  );

  const add = async () => {
    if (selected.length === 0) return;
    setBusy(true);
    try {
      const n = await addGroupMembers(groupType, groupToken, memberType, selected);
      toast(e('AddedCount', { count: n }));
      setSelected([]);
      setPageNumber(1);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const remove = async (edgeToken: string, label: string) => {
    try {
      await removeGroupMembers([edgeToken]);
      toast(t('common:removedItem', { label }));
      if (memberEdges.length === 1 && pageNumber > 1) setPageNumber(pageNumber - 1);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    // Untitled: this renders inside the detail page's "Members" tab, which labels it.
    <SectionPanel>
      <div className="space-y-4">
        {candidatesError && <ErrorState description={candidatesError} />}
        <div className="flex items-end gap-2">
          <div className="min-w-0 flex-1">
            <MultiSelect
              options={options}
              value={selected}
              onChange={setSelected}
              disabled={candidatesLoading || !!candidatesError || !members}
              placeholder={e('AddPlaceholder')}
            />
          </div>
          <Button onClick={add} loading={busy} disabled={busy || selected.length === 0 || !candidates || !members}>
            {t('common:add')}
          </Button>
        </div>

        {loading ? (
          <LoadingState description={t('common:loading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : memberEdges.length === 0 ? (
          <EmptyState description={e('EmptyInGroup')} />
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {memberEdges.map((edge) => (
              <li key={edge.token} className="flex items-center justify-between gap-2 px-3 py-2">
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium text-foreground">
                    {nameOf(edge.target.token)}
                  </span>
                  <span className="block truncate font-mono text-xs text-muted-foreground">
                    {edge.target.token}
                  </span>
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => remove(edge.token, nameOf(edge.target.token))}
                  aria-label={t('common:removeItem', { label: nameOf(edge.target.token) })}
                >
                  <Trash2 size={14} />
                </Button>
              </li>
            ))}
          </ul>
        )}
        {members && <Pagination pageNumber={pageNumber} pageSize={pageSize} pagination={members.pagination} onPageChange={setPageNumber} />}
      </div>
    </SectionPanel>
  );
}
