// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { Trash2 } from 'lucide-react';
import { PageShell } from '@/components/ui/page-shell';
import { CopyToken } from '@/components/ui/copy-token';
import { SectionPanel } from '@/components/ui/section-panel';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { TypeCapsule } from '@/components/TypeCapsule';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import type { RegistryResource } from '@/components/registry/types';

// Generic detail page: loads the entity by token, frames it with a header toolbar
// (Back + Delete), renders the resource's form, and appends any extra panels
// (e.g. device state + events). Delete surfaces the backend message (including a
// referential-integrity refusal) and only navigates away on success.
export function ResourceDetailPage<T>({ resource }: { resource: RegistryResource<T> }) {
  const { t } = useTranslation(['entities', 'common']);
  // Resolve one of this family's noun-bearing strings by its fixed suffix.
  const e = (suffix: string, opts?: Record<string, unknown>) =>
    t(`entities:${resource.i18nKey}${suffix}`, opts);
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
        <LoadingState description={t('common:loading')} />
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
        <ErrorState description={e('NotFound', { token })} />
      </PageShell>
    );
  }

  const remove = async () => {
    if (
      !(await confirm({
        title: e('DeleteTitle'),
        description: e('RemoveConfirm', { token }),
        confirmLabel: t('common:delete'),
      }))
    )
      return;
    try {
      await resource.remove(token);
      toast(e('DeletedToast', { token }));
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
    ? resource.detailTabs.map((tab) => ({
        value: tab.value,
        label: t(tab.label),
        content: tab.render(item, reload),
      }))
    : (() => {
        const extra = resource.renderDetailExtra?.(item, reload);
        return extra
          ? [
              {
                value: 'extra',
                label: resource.detailExtraLabel ? t(resource.detailExtraLabel) : t('common:moreTab'),
                content: extra,
              },
            ]
          : [];
      })();
  const name = resource.nameOf?.(item);
  const heading = name || token;
  const type = resource.typeOf?.(item);

  return (
    <PageShell
      title={heading}
      titleAdornment={name ? <CopyToken value={token} /> : undefined}
      banner={resource.banner}
      // The token is the title-line chip now; the description row carries only the type
      // (when the entity has one), so it is omitted entirely when there is none.
      description={
        type ? (
          <div className="flex flex-wrap items-center gap-2">
            <TypeCapsule appearance={type} />
          </div>
        ) : undefined
      }
      action={
        <Button variant="destructive" size="sm" onClick={remove}>
          <Trash2 size={14} /> {t('common:delete')}
        </Button>
      }
    >
      {/* The form always lives under a "Basic" tab; any labelled extras (a group's
          Members, a profile's Metrics/Commands/Alarm Rules/Versions) become tabs
          beside it. */}
      <Tabs defaultValue="basic">
        <TabsList>
          <TabsTrigger value="basic">{t('common:basicTab')}</TabsTrigger>
          {extraTabs.map((tab) => (
            <TabsTrigger key={tab.value} value={tab.value}>
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="basic">{form}</TabsContent>
        {extraTabs.map((tab) => (
          <TabsContent key={tab.value} value={tab.value}>
            {tab.content}
          </TabsContent>
        ))}
      </Tabs>
    </PageShell>
  );
}
