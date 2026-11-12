// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { DataTable, DataTableBody, DataTableHead, DataTableHeaderCell } from '@/components/ui/data-table';

export const reviewPath = (token: string) => `/automation-reviews/${encodeURIComponent(token)}`;
const refusalCodes = new Set([
  'SELF_APPROVAL_REFUSED', 'NOT_APPROVABLE', 'DIGEST_MISMATCH', 'REVIEW_STALE_DRAFT_CHANGED',
  'REVIEW_STALE_BASELINE_CHANGED', 'SECOND_FACTOR_REQUIRED', 'CONCURRENT_APPROVAL', 'REVIEW_NOT_PENDING',
  'REVIEW_UNAVAILABLE', 'REVIEW_REFUSED', 'NO_BASELINE', 'RATE_LIMITED', 'CONFLICT', 'NOT_A_PERSON',
]);
export const reviewErrorKey = (code: string | undefined) => code && refusalCodes.has(code) ? code : undefined;

export function Outcome({ outcome }: { outcome: string }) {
  const { t } = useTranslation('automationReview');
  const variant = outcome === 'PASSED' ? 'success' : outcome === 'FAILED' ? 'destructive'
    : outcome === 'INCONCLUSIVE' ? 'warning' : 'secondary';
  return <Badge variant={variant}>{t(outcome)}</Badge>;
}

export function ReviewTable({ headings, children }: { headings: string[]; children: ReactNode }) {
  return <div className="overflow-x-auto [&_td]:p-3 [&_td]:align-top [&_tr]:border-b"><DataTable>
    <DataTableHead>{headings.map(h => <DataTableHeaderCell key={h}>{h}</DataTableHeaderCell>)}</DataTableHead>
    <DataTableBody>{children}</DataTableBody>
  </DataTable></div>;
}
