// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { BackLink } from '@/routes/common';
import { DeviceForm } from '@/routes/devices/DeviceForm';

export default function NewDevicePage() {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title="New device"
      description="Devices registered in this tenant."
      action={<BackLink to="/devices">Devices</BackLink>}
    >
      <SectionPanel>
        <DeviceForm
          onDone={(m) => {
            toast(m);
            navigate('/devices');
          }}
        />
      </SectionPanel>
    </PageShell>
  );
}
