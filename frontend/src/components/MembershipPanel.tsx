// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reusable group-membership surface, plugged into a group's detail page via
// the registry kit's `renderDetailExtra`. Membership is a group -> member edge
// (ADR-013), so one panel serves every group family by parameterizing the group
// and member entity types. Member display names are resolved from the candidate
// list (the Entity edge target only carries id + token).

import { useMemo, useState } from 'react';
import { Trash2 } from 'lucide-react';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { MultiSelect } from '@/components/ui/multi-select';
import { LoadingState } from '@/components/ui/loading-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useQuery } from '@/lib/hooks/use-query';
import { useToast } from '@/components/ui/toast';
import { useReload, errMessage } from '@/routes/common';
import { listGroupMembers, addGroupMembers, removeGroupMembers } from '@/lib/api/relationships';

interface Candidate {
  token: string;
  name?: string | null;
}

export function MembershipPanel({
  groupType,
  groupToken,
  memberType,
  memberSingular,
  loadCandidates,
}: {
  /** Backend entity-type token for the group, e.g. "assetgroup". */
  groupType: string;
  groupToken: string;
  /** Backend entity-type token for members, e.g. "asset". */
  memberType: string;
  /** Lowercase member noun for labels, e.g. "asset". */
  memberSingular: string;
  /** Loads all member-type instances (add candidates + name resolution). */
  loadCandidates: () => Promise<Candidate[]>;
}) {
  const { toast } = useToast();
  const [version, reload] = useReload();
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const { data: members, loading } = useQuery(
    () => listGroupMembers(groupType, groupToken, { pageNumber: 1, pageSize: 1000 }),
    [groupType, groupToken, version],
  );
  const { data: candidates } = useQuery(loadCandidates, [memberType]);

  const memberEdges = members?.results ?? [];

  const nameOf = useMemo(() => {
    const m = new Map((candidates ?? []).map((c) => [c.token, c.name || c.token]));
    return (token: string) => m.get(token) ?? token;
  }, [candidates]);

  const memberTokens = useMemo(() => new Set(memberEdges.map((e) => e.target.token)), [memberEdges]);

  // Add-picker options: member-type instances that aren't already in the group.
  const options = useMemo(
    () =>
      (candidates ?? [])
        .filter((c) => !memberTokens.has(c.token))
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
      toast(`Added ${n} ${memberSingular}${n === 1 ? '' : 's'}`);
      setSelected([]);
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
      toast(`Removed ${label}`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    // Untitled: this renders inside the detail page's "Members" tab, which labels it.
    <SectionPanel>
      <div className="space-y-4">
        <div className="flex items-end gap-2">
          <div className="min-w-0 flex-1">
            <MultiSelect
              options={options}
              value={selected}
              onChange={setSelected}
              placeholder={`Add ${memberSingular}s…`}
            />
          </div>
          <Button onClick={add} loading={busy} disabled={busy || selected.length === 0}>
            Add
          </Button>
        </div>

        {loading ? (
          <LoadingState description={`Loading ${memberSingular}s…`} />
        ) : memberEdges.length === 0 ? (
          <EmptyState description={`No ${memberSingular}s in this group yet.`} />
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
                  aria-label={`Remove ${nameOf(edge.target.token)}`}
                >
                  <Trash2 size={14} />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </SectionPanel>
  );
}
