// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
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

  if (loading) {
    return (
      <PageShell title={token} banner={resource.banner}>
        <LoadingState description={`Loading ${resource.singular}…`} />
      </PageShell>
    );
  }
  if (error) {
    return (
      <PageShell title={token} banner={resource.banner}>
        <ErrorState description={error} />
      </PageShell>
    );
  }
  if (!item) {
    return (
      <PageShell title={token} banner={resource.banner}>
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

  const form = (
    <SectionPanel>
      {resource.renderForm(item, (m) => {
        toast(m);
        reload();
      })}
    </SectionPanel>
  );
  const extra = resource.renderDetailExtra?.(item);
  const labeledExtra = extra != null && resource.detailExtraLabel != null;

  return (
    <PageShell
      title={token}
      banner={resource.banner}
      description={resource.descriptionOf?.(item)}
      action={
        <Button variant="destructive" size="sm" onClick={remove}>
          <Trash2 size={14} /> Delete
        </Button>
      }
    >
      {/* The form always lives under a "Basic" tab — a single-tab bar is a
          deliberate, forward-looking frame for tabs added later. A labelled extra
          (e.g. a group's Members) becomes a second tab beside it. */}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">Basic</TabsTrigger>
          {labeledExtra && <TabsTrigger value="extra">{resource.detailExtraLabel}</TabsTrigger>}
        </TabsList>
        <TabsContent value="basic">
          <div className="space-y-6">
            {form}
            {/* An unlabelled extra (no tab) still renders under Basic. */}
            {extra && !resource.detailExtraLabel ? extra : null}
          </div>
        </TabsContent>
        {labeledExtra && <TabsContent value="extra">{extra}</TabsContent>}
      </Tabs>
    </PageShell>
  );
}
