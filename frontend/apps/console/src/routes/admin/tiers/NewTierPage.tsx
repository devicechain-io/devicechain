// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { TierForm } from '@/routes/admin/tiers/TierForm';

export default function NewTierPage() {
  const { t } = useTranslation('tiers');
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell title={t('newTier')} description={t('newTierDescription')}>
      <SectionPanel>
        <TierForm
          onDone={(m) => {
            toast(m);
            navigate('/admin/tiers');
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
