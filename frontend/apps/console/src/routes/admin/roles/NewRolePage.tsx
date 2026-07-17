// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { RoleForm } from '@/routes/admin/roles/RoleForm';

export default function NewRolePage() {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title="New role"
      description="System roles gate the admin API; tenant roles gate the data plane."
    >
      <SectionPanel>
        <RoleForm
          onDone={(m) => {
            toast(m);
            navigate('/admin/roles');
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
