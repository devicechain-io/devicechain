// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { BackLink } from '@/routes/common';
import { TenantForm } from '@/routes/admin/tenants/TenantForm';

export default function NewTenantPage() {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title="New tenant"
      description="The instance's tenant registry. A tenant is a control-plane record, not a provisioned resource."
      action={<BackLink to="/admin/tenants">Tenants</BackLink>}
    >
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
