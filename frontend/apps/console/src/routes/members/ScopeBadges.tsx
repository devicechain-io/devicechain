// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Building2 } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { useQuery } from '@/lib/hooks/use-query';
import { getCustomerNames } from '@/lib/api/customers';

/**
 * Display names for customer tokens, fetched once per distinct set. While the names load,
 * or for a token that names no customer, the token itself stands in.
 */
export function useCustomerNames(tokens: string[]): Record<string, string> {
  const key = [...new Set(tokens)].sort().join('\n');
  const { data } = useQuery(() => getCustomerNames(key ? key.split('\n') : []), [key]);
  return data ?? {};
}

// ScopeBadges shows what a member can see: "Whole tenant", or one badge per customer they
// are limited to. An empty scope is the WHOLE tenant, never "nothing" — which is the
// misreading this component exists to rule out.
export function ScopeBadges({ tokens, names }: { tokens: string[]; names: Record<string, string> }) {
  const { t } = useTranslation('members');
  if (tokens.length === 0) {
    return <span className="text-sm text-muted-foreground">{t('scopeWholeTenant')}</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {tokens.map((tok) => (
        <Badge key={tok} variant="secondary" className="gap-1">
          <Building2 size={12} /> {names[tok] ?? tok}
        </Badge>
      ))}
    </div>
  );
}
