// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connector picker for the `publish` REACT action, shared by the rule form and the canvas
// inspector so the two surfaces offer — and word — exactly the same thing.
//
// It lists the CALLER's connectors: the query runs under the caller's own token, so what is offered
// is what the caller's tenant and authorities allow, and a connector the caller cannot see is never
// presented as a choice. Only a published connector actually dispatches, but the list is all
// connectors (the publish-time existence check is not the compiler's); an unknown or stale current
// value is shown rather than silently dropped, labelled as not found.

import { Link } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { Select, SelectItem } from '@/components/ui/select';
import { useQuery } from '@/lib/hooks/use-query';
import { listConnectors } from '@/lib/api/connectors';
import '@/i18n/lazy/deviceProfiles';

export function ConnectorPicker({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const { data, loading, error } = useQuery(() => listConnectors({ pageNumber: 1, pageSize: 100 }), []);
  const connectors = data?.results ?? [];
  const known = connectors.some((c) => c.token === value);
  // Only show the "no connectors" hint when there's also nothing selected — a stale/deleted
  // ref (value set, list empty) must still render the Select so the author can see and clear it.
  if (!loading && !error && connectors.length === 0 && !value) {
    return (
      <p className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        <Trans t={t} i18nKey="inspectorNoConnectorsHint" components={{ link: <Link to="/connectors" className="text-primary hover:underline" /> }} />
      </p>
    );
  }
  return (
    <div className="space-y-1.5">
      <Select id={id} value={value} onChange={onChange}>
        <SelectItem value="">{loading ? t('common:loading') : t('inspectorSelectConnectorPlaceholder')}</SelectItem>
        {/* Show a "(not found)" row for a value not in the list — but only once loaded without
            error, so a valid ref doesn't flash as not-found while the query is in flight or when
            the list could not be read at all (then it is shown as the plain token). */}
        {!loading && value && !known && (
          <SelectItem value={value}>{error ? value : t('inspectorConnectorNotFound', { token: value })}</SelectItem>
        )}
        {connectors.map((c) => (
          <SelectItem key={c.token} value={c.token}>
            {c.name ? `${c.name} (${c.token})` : c.token}
          </SelectItem>
        ))}
      </Select>
      {error && (
        <p role="alert" className="text-xs text-red-600 dark:text-red-500">
          {t('connectorsLoadError', { message: error })}
        </p>
      )}
    </div>
  );
}
