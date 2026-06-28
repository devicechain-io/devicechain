// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { BackLink } from '@/routes/common';
import { DeviceTypeForm } from '@/routes/device-types/DeviceTypeForm';

export default function NewDeviceTypePage() {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title="New device type"
      description="Templates that classify devices."
      action={<BackLink to="/device-types">Device types</BackLink>}
    >
      <SectionPanel>
        <DeviceTypeForm
          onDone={(m) => {
            toast(m);
            navigate('/device-types');
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
