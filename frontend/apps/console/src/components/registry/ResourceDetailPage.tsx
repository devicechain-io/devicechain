// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { TypeCapsule, TokenCapsule } from '@/components/TypeCapsule';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { cap } from '@/components/registry/forms';
import { errMessage, useReload } from '@/routes/common';
import type { RegistryResource } from '@/components/registry/types';

// Generic detail page: loads the entity by token, frames it with a header toolbar
// (Back + Delete), renders the resource's form, and appends any extra panels
// (e.g. device state + events). Delete surfaces the backend message (including a
// referential-integrity refusal) and only navigates away on success.
export function ResourceDetailPage<T>({ resource }: { resource: RegistryResource<T> }) {
  const { token: rawToken } = useParams<{ token: string }>();
  const token = decodeURIComponent(rawToken ?? '');
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();

  const [version, reload] = useReload();
  const { data: item, loading, error } = useQuery(
    () => resource.load(token),
    [version, resource.basePath, token],
  );

  // Only take over the whole page on the FIRST load / hard error. useQuery keeps the
  // prior data and flips loading during a background refetch (e.g. a tab calling
  // reload after publish); blanking the page then would unmount the tab bar and
  // bounce the user back to "Basic". While data is present, refetches update in place.
  if (loading && !item) {
    return (
      <PageShell title={token} banner={resource.banner}>
        <LoadingState description={`Loading ${resource.singular}…`} />
      </PageShell>
    );
  }
  if (error && !item) {
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
    if (
      !(await confirm({
        title: `Delete ${resource.singular}`,
        description: prompt,
        confirmLabel: 'Delete',
      }))
    )
      return;
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
  // Detail tabs beside "Basic": the N-tab list wins (device profiles), else the
  // legacy single labelled extra (a group's Members, a type's Appearance).
  const extraTabs: { value: string; label: string; content: ReactNode }[] = resource.detailTabs
    ? resource.detailTabs.map((t) => ({ value: t.value, label: t.label, content: t.render(item, reload) }))
    : (() => {
        const extra = resource.renderDetailExtra?.(item, reload);
        return extra ? [{ value: 'extra', label: resource.detailExtraLabel ?? 'More', content: extra }] : [];
      })();
  const heading = resource.nameOf?.(item) || token;
  const type = resource.typeOf?.(item);

  return (
    <PageShell
      title={heading}
      banner={resource.banner}
      description={
        <div className="mt-1 flex flex-wrap items-center gap-2">
          {type && <TypeCapsule appearance={type} />}
          <TokenCapsule token={token} />
        </div>
      }
      action={
        <Button variant="destructive" size="sm" onClick={remove}>
          <Trash2 size={14} /> Delete
        </Button>
      }
    >
      {/* The form always lives under a "Basic" tab; any labelled extras (a group's
          Members, a profile's Metrics/Commands/Alarm Rules/Versions) become tabs
          beside it. */}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">Basic</TabsTrigger>
          {extraTabs.map((t) => (
            <TabsTrigger key={t.value} value={t.value}>
              {t.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="basic">{form}</TabsContent>
        {extraTabs.map((t) => (
          <TabsContent key={t.value} value={t.value}>
            {t.content}
          </TabsContent>
        ))}
      </Tabs>
    </PageShell>
  );
}
