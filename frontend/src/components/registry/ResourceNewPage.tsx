// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate } from 'react-router-dom';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { useToast } from '@/components/ui/toast';
import { BackLink } from '@/routes/common';
import type { RegistryResource } from '@/components/registry/types';

// Generic create page: the resource's form in a panel; on success, toast and
// return to the list.
export function ResourceNewPage<T>({ resource }: { resource: RegistryResource<T> }) {
  const navigate = useNavigate();
  const { toast } = useToast();

  return (
    <PageShell
      title={`New ${resource.singular}`}
      action={<BackLink to={resource.basePath}>{resource.backLabel}</BackLink>}
    >
      <SectionPanel>
        {resource.renderForm(undefined, (m) => {
          toast(m);
          navigate(resource.basePath);
        })}
      </SectionPanel>
    </PageShell>
  );
}
