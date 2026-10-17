// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Boxes, Building2 } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { useQuery } from '@/lib/hooks/use-query';
import { getCustomerNames } from '@/lib/api/customers';
import { getGroupNames } from '@/lib/api/device-management';

/** One thing a member's access is limited to: a customer, or a group of devices. */
export interface ScopeRef {
  kind: 'customer' | 'group';
  token: string;
}

/**
 * The scope entries of a member or of the signed-in user's claims, as ScopeRefs. Kinds
 * arrive as the API's enum (CUSTOMER) or as the token's lower-case prefix (customer); an
 * entry of any other kind is dropped rather than guessed at.
 */
export function scopeRefs(anchors: readonly { kind: string; token: string }[]): ScopeRef[] {
  return anchors.flatMap((a) => {
    const kind = a.kind.toLowerCase();
    return kind === 'customer' || kind === 'group' ? [{ kind, token: a.token }] : [];
  });
}

/** The key a scope entry's display name is held under. */
export function scopeKey(ref: ScopeRef): string {
  return `${ref.kind}:${ref.token}`;
}

function distinctTokens(refs: ScopeRef[], kind: ScopeRef['kind']): string {
  return [...new Set(refs.filter((r) => r.kind === kind).map((r) => r.token))].sort().join('\n');
}

/**
 * Display names for scope entries, keyed by scopeKey, fetched once per distinct set of each
 * kind. While the names load, or for an entry that names nothing (or that the caller cannot
 * read), the token itself stands in.
 */
export function useScopeNames(refs: ScopeRef[]): Record<string, string> {
  const customers = distinctTokens(refs, 'customer');
  const groups = distinctTokens(refs, 'group');
  const { data: customerNames } = useQuery(() => getCustomerNames(customers ? customers.split('\n') : []), [customers]);
  const { data: groupNames } = useQuery(() => getGroupNames(groups ? groups.split('\n') : []), [groups]);
  const out: Record<string, string> = {};
  for (const [token, name] of Object.entries(customerNames ?? {})) out[`customer:${token}`] = name;
  for (const [token, name] of Object.entries(groupNames ?? {})) out[`group:${token}`] = name;
  return out;
}

/** The display name of a scope entry, or its token while none is known. */
export function scopeName(ref: ScopeRef, names: Record<string, string>): string {
  return names[scopeKey(ref)] ?? ref.token;
}

// ScopeBadges shows what a member can see: "Whole tenant", or one badge per customer and per
// group of devices they are limited to. An empty scope is the WHOLE tenant, never "nothing" —
// which is the misreading this component exists to rule out.
export function ScopeBadges({ refs, names }: { refs: ScopeRef[]; names: Record<string, string> }) {
  const { t } = useTranslation('members');
  if (refs.length === 0) {
    return <span className="text-sm text-muted-foreground">{t('scopeWholeTenant')}</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {refs.map((ref) => (
        <Badge
          key={scopeKey(ref)}
          variant="secondary"
          className="gap-1"
          title={ref.kind === 'group' ? t('scopeBadgeGroup') : t('scopeBadgeCustomer')}
        >
          {ref.kind === 'group' ? <Boxes size={12} /> : <Building2 size={12} />} {scopeName(ref, names)}
        </Badge>
      ))}
    </div>
  );
}
