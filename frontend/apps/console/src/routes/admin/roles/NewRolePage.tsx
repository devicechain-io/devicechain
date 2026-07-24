// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { RoleForm } from '@/routes/admin/roles/RoleForm';

export default function NewRolePage() {
  const { t } = useTranslation('roles');
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell title={t('newRole')} description={t('newRoleDescription')}>
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
