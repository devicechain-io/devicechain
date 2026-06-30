// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { BackLink, errMessage, useReload } from '@/routes/common';
import type { RegistryResource } from '@/components/registry/types';

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// Generic detail page: loads the entity by token, frames it with a header toolbar
// (Back + Delete), renders the resource's form, and appends any extra panels
// (e.g. device state + events). Delete surfaces the backend message (including a
// referential-integrity refusal) and only navigates away on success.
export function ResourceDetailPage<T>({ resource }: { resource: RegistryResource<T> }) {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();

  const [version, reload] = useReload();
  const { data: item, loading, error } = useQuery(
    () => resource.load(token),
    [version, resource.basePath, token],
  );

  const back = <BackLink to={resource.basePath}>{resource.backLabel}</BackLink>;

  if (loading) {
    return (
      <PageShell title={token} action={back}>
        <LoadingState description={`Loading ${resource.singular}…`} />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!item) {
    return (
      <PageShell title={token} action={back}>
        <ErrorState description={`${cap(resource.singular)} “${token}” not found.`} />
      </PageShell>
    );
  }

  const remove = async () => {
    const prompt =
      resource.removeConfirm?.(item) ??
      `Delete ${resource.singular} “${token}”? This cannot be undone.`;
    if (!window.confirm(prompt)) return;
    try {
      await resource.remove(token);
      toast(`${cap(resource.singular)} “${token}” deleted`);
      navigate(resource.basePath);
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={token}
      description={resource.descriptionOf?.(item)}
      action={
        <div className="flex items-center gap-2">
          {back}
          <Button variant="destructive" size="sm" onClick={remove}>
            <Trash2 size={14} /> Delete
          </Button>
        </div>
      }
    >
      <div className="space-y-6">
        <SectionPanel>
          {resource.renderForm(item, (m) => {
            toast(m);
            reload();
          })}
        </SectionPanel>
        {resource.renderDetailExtra?.(item)}
      </div>
    </PageShell>
  );
}
