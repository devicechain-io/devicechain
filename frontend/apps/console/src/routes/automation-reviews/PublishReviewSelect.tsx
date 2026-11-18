// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import '@/i18n/lazy/automationReview';
import { useEffect } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { listApprovedReviewsForDraft } from '@/lib/api/automation-reviews';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { Select, SelectItem } from '@/components/ui/select';

export default function PublishReviewSelect({ profileToken, revision, selected, onSelect }: {
  profileToken: string; revision: number; selected: string; onSelect: (token: string) => void;
}) {
  const { t } = useTranslation('automationReview');
  const { current, error } = useQuery(() => listApprovedReviewsForDraft(profileToken), [profileToken, revision]);
  const rows = current?.results ?? [];
  useEffect(() => {
    if (!current || !current.results.some(r => r.token === selected)) onSelect('');
  }, [current, selected, onSelect]);
  return <div className="space-y-2 rounded-md border p-4">
    <p>{t('protectedPublish')}</p>
    <label htmlFor="publish-review">{t('approvedReview')}</label>
    <Select id="publish-review" value={selected} onChange={onSelect}>
      <SelectItem value="">{t('selectReview')}</SelectItem>
      {rows.map(r => <SelectItem key={r.token} value={r.token}>{r.token.slice(0, 12)} · {t(r.outcome)} · {formatTime(r.approvedAt)}</SelectItem>)}
    </Select>
    {error && <p role="alert">{error}</p>}
    {current && !rows.length && <p>{t('noApprovedReviews')} <Link className="text-primary underline" to={`/device-profiles/${encodeURIComponent(profileToken)}?tab=change-review`}>{t('tab')}</Link></p>}
  </div>;
}
