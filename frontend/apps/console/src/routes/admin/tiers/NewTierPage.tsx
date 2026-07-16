// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { BackLink } from '@/routes/common';
import { TierForm } from '@/routes/admin/tiers/TierForm';

export default function NewTierPage() {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title="New tier"
      description="A new packaging option. It takes effect once tenants are created at it or moved to it — defining one changes nothing on its own."
      action={<BackLink to="/admin/tiers">Tiers</BackLink>}
    >
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
