// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';

export default function NewTenantPage() {
  const { t } = useTranslation('tenants');
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell title={t('newTenant')} description={t('description')}>
      <SectionPanel>
        <TenantForm
          onDone={(m) => {
            toast(m);
            navigate('/admin/tenants');
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
