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
import { listCustomers } from '@/lib/api/customers';
import { getDashboardSharing, shareDashboard } from '@/lib/api/dashboards';
import { errMessage } from '@/routes/common';

const CUSTOMER_PAGE = { pageNumber: 1, pageSize: 1000 };

// ShareDashboardDrawer picks the customers a dashboard is shared with. The users of those
// customers can then list it and open its PUBLISHED version — never the draft — in the
// console and in the dashboard app. Sharing an unpublished dashboard is allowed and shows
// them nothing until it is published, which the drawer says.
export function ShareDashboardDrawer({
  token,
  name,
  published,
  onClose,
  onShared,
}: {
  token: string;
  name: string;
  /** Whether the dashboard has a published version; a shared but unpublished one shows customers nothing. */
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
  return <ShareEditor token={token} initial={current ?? []} published={published} onShared={onShared} />;
}

function ShareEditor({
  token,
  initial,
  published,
  onShared,
}: {
  token: string;
  initial: string[];
  published: boolean;
  onShared: (count: number) => void;
}) {
  const { t } = useTranslation('dashboards');
  const [selected, setSelected] = useState<string[]>(initial);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { data: customers } = useQuery(() => listCustomers(CUSTOMER_PAGE), []);
  const options = useMemo<ComboboxOption[]>(() => {
    const known = (customers?.results ?? []).map((c) => ({ value: c.token, label: c.name || c.token }));
    const extra = selected.filter((s) => !known.some((k) => k.value === s)).map((s) => ({ value: s }));
    return [...known, ...extra];
  }, [customers, selected]);

  const save = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const shared = await shareDashboard(token, selected);
      onShared(shared.length);
    } catch (err) {
      setFormError(errMessage(err));
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
          options={options}
          value={selected}
          onChange={setSelected}
          placeholder={t('shareCustomersPlaceholder')}
          searchPlaceholder={t('shareCustomersSearchPlaceholder')}
          emptyMessage={t('shareCustomersEmpty')}
        />
      </FormField>
      <Button onClick={save} loading={busy} disabled={busy}>
        {t('shareSave')}
      </Button>
    </div>
  );
}
