// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FormDrawer } from '@/components/registry';
import { Button } from '@/components/ui/button';
import { FormField } from '@/components/ui/form-field';
import { MultiSelect } from '@/components/ui/multi-select';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import type { ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { errorCode } from '@devicechain/client';
import { listCustomers } from '@/lib/api/customers';
import { listAccessScopeGroups } from '@/lib/api/device-management';
import { getDashboardSharing, shareDashboard, type DashboardShareTargets } from '@/lib/api/dashboards';
import { errMessage } from '@/routes/common';
import '@/i18n/lazy/dashboards';

const CUSTOMER_PAGE = { pageNumber: 1, pageSize: 1000 };

// ShareDashboardDrawer picks the customers and groups of devices a dashboard is shared with.
// The members limited to those customers or groups can then list it and open its PUBLISHED
// version — never the draft — in the console and in the dashboard app. Sharing an
// unpublished dashboard is allowed and shows them nothing until it is published, which the
// drawer says. Only groups that are access scopes are offered: the server refuses any other,
// because no member can be limited to it.
export function ShareDashboardDrawer({
  token,
  name,
  published,
  onClose,
  onShared,
}: {
  token: string;
  name: string;
  /** Whether the dashboard has a published version; a shared but unpublished one shows its viewers nothing. */
  published: boolean;
  onClose: () => void;
  onShared: (count: number) => void;
}) {
  const { t } = useTranslation('dashboards');
  return (
    <FormDrawer
      shot="dashboard-share"
      open
      onOpenChange={(open) => !open && onClose()}
      title={t('shareTitle', { name })}
      description={t('shareDescription')}
    >
      <ShareForm token={token} published={published} onShared={onShared} />
    </FormDrawer>
  );
}

function ShareForm({ token, published, onShared }: { token: string; published: boolean; onShared: (count: number) => void }) {
  const { t } = useTranslation('dashboards');
  const { data: current, loading, error } = useQuery(() => getDashboardSharing(token), [token]);
  if (loading) return <LoadingState description={t('shareLoading')} />;
  if (error) return <ErrorState description={error} />;
  if (current === null) return <ErrorState description={t('detailNotFound', { token })} />;
  return (
    <ShareEditor
      token={token}
      initial={current ?? { customers: [], groups: [] }}
      published={published}
      onShared={onShared}
    />
  );
}

function ShareEditor({
  token,
  initial,
  published,
  onShared,
}: {
  token: string;
  initial: DashboardShareTargets;
  published: boolean;
  onShared: (count: number) => void;
}) {
  const { t } = useTranslation('dashboards');
  const [customers, setCustomers] = useState<string[]>(initial.customers);
  const [groups, setGroups] = useState<string[]>(initial.groups);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { data: customerPage } = useQuery(() => listCustomers(CUSTOMER_PAGE), []);
  const { data: accessGroups } = useQuery(listAccessScopeGroups, []);
  const customerOptions = useMemo<ComboboxOption[]>(() => {
    const known = (customerPage?.results ?? []).map((c) => ({ value: c.token, label: c.name || c.token }));
    const extra = customers.filter((s) => !known.some((k) => k.value === s)).map((s) => ({ value: s }));
    return [...known, ...extra];
  }, [customerPage, customers]);
  const groupOptions = useMemo<ComboboxOption[]>(() => {
    const known = (accessGroups ?? []).map((g) => ({ value: g.token, label: g.name || g.token }));
    // A group already shared with stays shown, so it can be removed; saving it while it is
    // no longer an access scope is refused, and the message says why.
    const extra = groups.filter((s) => !known.some((k) => k.value === s)).map((s) => ({ value: s }));
    return [...known, ...extra];
  }, [accessGroups, groups]);

  const save = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const shared = await shareDashboard(token, { customers, groups });
      onShared(shared.customers.length + shared.groups.length);
    } catch (err) {
      setFormError(errorCode(err) === 'NOT_FOUND' ? t('shareTargetMissing') : errMessage(err));
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {!published && <p className="text-sm text-muted-foreground">{t('shareNotPublished')}</p>}
      <FormField label={t('shareCustomersLabel')} htmlFor="share-customers" description={t('shareCustomersDescription')}>
        <MultiSelect
          id="share-customers"
          options={customerOptions}
          value={customers}
          onChange={setCustomers}
          placeholder={t('shareCustomersPlaceholder')}
          searchPlaceholder={t('shareCustomersSearchPlaceholder')}
          emptyMessage={t('shareCustomersEmpty')}
        />
      </FormField>
      <FormField label={t('shareGroupsLabel')} htmlFor="share-groups" description={t('shareGroupsDescription')}>
        <MultiSelect
          id="share-groups"
          options={groupOptions}
          value={groups}
          onChange={setGroups}
          placeholder={t('shareGroupsPlaceholder')}
          searchPlaceholder={t('shareGroupsSearchPlaceholder')}
          emptyMessage={t('shareGroupsEmpty')}
        />
      </FormField>
      <Button onClick={save} loading={busy} disabled={busy}>
        {t('shareSave')}
      </Button>
    </div>
  );
}
