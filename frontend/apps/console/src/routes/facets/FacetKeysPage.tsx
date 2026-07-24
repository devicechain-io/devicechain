// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Facets registry (ADR-061 G2). A facet key declares that an EntityAttribute
// key, for a member family, is a classification facet — a browse/filter axis the
// console can offer with value typeahead. Declaring a facet does NOT store values:
// those stay as EntityAttribute rows on the member entities. This page lists the
// tenant's declared facets and hosts the declare drawer; a facet is upserted by its
// natural key (memberType, key). The dynamic-group browse consumer that uses these
// axes lands in G4.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import {
  listFacetKeys,
  setFacetKey,
  deleteFacetKey,
  FACET_MEMBER_TYPES,
  FACET_VALUE_TYPES,
  type FacetKey,
} from '@/lib/api/facet-keys';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import { errMessage, useReload } from '@/routes/common';
import { FormDrawer } from '@/components/registry';
import { cn } from '@/lib/utils';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

// Value types are technical enum tokens (STRING/LONG/…), never localized.
const VALUE_TYPE_OPTIONS: ComboboxOption[] = FACET_VALUE_TYPES.map((vt) => ({
  value: vt,
  label: vt,
}));

// Localized display label per member family (ADR-061). The family values
// ('device'/'asset'/'area'/'customer') are internal; these shared `common` keys
// give the user-facing name so no raw English family shows under a non-English
// locale.
const FAMILY_LABEL_KEY: Record<string, string> = {
  device: 'common:familyDevice',
  asset: 'common:familyAsset',
  area: 'common:familyArea',
  customer: 'common:familyCustomer',
};

export default function FacetKeysPage() {
  const { t } = useTranslation('facets');
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const [filter, setFilter] = useState<string | undefined>(undefined);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => listFacetKeys(filter), [filter, version]);

  const facets = data ?? [];

  // Resolve a member family to its localized label, falling back to the raw value
  // for an out-of-vocabulary family (backend-validated to the 4 today, but this
  // keeps a surprise value visible instead of blank). fk.memberType is typed as a
  // plain string by codegen, so the map lookup isn't compile-total.
  const familyLabel = (mt: string) => (FAMILY_LABEL_KEY[mt] ? t(FAMILY_LABEL_KEY[mt]) : mt);

  const remove = async (fk: FacetKey) => {
    if (
      !(await confirm({
        title: t('deleteTitle'),
        description: t('deleteConfirm', { memberType: familyLabel(fk.memberType), key: fk.key }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await deleteFacetKey(fk.memberType, fk.key);
      toast(t('deleted', { key: fk.key }));
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title={t('title')}
      description={t('description')}
      banner="dashboard"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} /> {t('newFacet')}
          </Button>
        ) : undefined
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title={t('newFacet')}>
        <FacetKeyForm
          onDone={() => {
            setCreating(false);
            reload();
          }}
        />
      </FormDrawer>

      {/* Member-family filter. Facets are declared per family, so the axis picker
          (and this list) scopes to one family at a time; "All" shows every family. */}
      <div className="mb-4 flex flex-wrap gap-2">
        <FilterChip label={t('all')} active={filter === undefined} onClick={() => setFilter(undefined)} />
        {FACET_MEMBER_TYPES.map((mt) => (
          <FilterChip
            key={mt}
            label={familyLabel(mt)}
            active={filter === mt}
            onClick={() => setFilter(mt)}
          />
        ))}
      </div>

      {loading ? (
        <LoadingState description={t('loading')} />
      ) : error ? (
        <ErrorState description={error} />
      ) : facets.length === 0 ? (
        <EmptyState description={t('empty')} />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('colMember')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colKey')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colValues')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colLabel')}</DataTableHeaderCell>
            <DataTableHeaderCell> </DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {facets.map((fk) => {
              const isSystem = fk.source === 'system';
              return (
                <DataTableRow key={fk.id}>
                  <DataTableCell className="text-muted-foreground">
                    {familyLabel(fk.memberType)}
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    <span className="flex items-center gap-2">
                      {fk.key}
                      {isSystem && <Badge variant="secondary">{t('system')}</Badge>}
                    </span>
                  </DataTableCell>
                  <DataTableCell className="font-mono text-xs text-muted-foreground">
                    {fk.valueType}
                  </DataTableCell>
                  <DataTableCell className="max-w-xs truncate text-muted-foreground">
                    {fk.values && fk.values.length > 0 ? fk.values.join(', ') : '—'}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground">{fk.label || '—'}</DataTableCell>
                  <DataTableCell className="text-right">
                    {canWrite && !isSystem && (
                      <Button variant="ghost" size="sm" onClick={() => void remove(fk)}>
                        <Trash2 size={14} /> {t('delete')}
                      </Button>
                    )}
                  </DataTableCell>
                </DataTableRow>
              );
            })}
          </DataTableBody>
        </DataTable>
      )}
    </PageShell>
  );
}

function FilterChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'rounded-full border px-3 py-1 text-sm transition-colors',
        active
          ? 'border-primary bg-primary/10 font-medium text-primary'
          : 'border-border text-muted-foreground hover:bg-muted',
      )}
    >
      {label}
    </button>
  );
}

// The declare form upserts a facet by its natural key (memberType, key). Editing an
// existing facet is the same operation — re-declaring the same (memberType, key)
// overwrites its type/values/label — so there is no separate edit surface in v1.
function FacetKeyForm({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation('facets');
  const memberTypeOptions: ComboboxOption[] = FACET_MEMBER_TYPES.map((mt) => ({ value: mt, label: t(FAMILY_LABEL_KEY[mt]) }));
  const [memberType, setMemberType] = useState<string>(FACET_MEMBER_TYPES[0]);
  const [key, setKey] = useState('');
  const [valueType, setValueType] = useState<string>('STRING');
  const [valuesText, setValuesText] = useState('');
  const [label, setLabel] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    const trimmedKey = key.trim();
    if (!trimmedKey) {
      setFormError(t('keyRequired'));
      return;
    }
    // Comma-separated vocabulary → a de-duplicated, trimmed, non-empty list.
    const values = Array.from(
      new Set(
        valuesText
          .split(',')
          .map((v) => v.trim())
          .filter((v) => v.length > 0),
      ),
    );
    setBusy(true);
    try {
      await setFacetKey({
        memberType,
        key: trimmedKey,
        valueType,
        values: values.length > 0 ? values : undefined,
        label: label.trim() || undefined,
      });
      onDone();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField label={t('memberFamily')} htmlFor="fk-member">
        <Combobox
          id="fk-member"
          value={memberType}
          onChange={(v) => setMemberType(v)}
          options={memberTypeOptions}
          allowClear={false}
        />
      </FormField>
      <FormField label={t('attributeKey')} htmlFor="fk-key">
        <Input
          id="fk-key"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder={t('keyPlaceholder')}
        />
      </FormField>
      <FormField label={t('valueType')} htmlFor="fk-type">
        <Combobox
          id="fk-type"
          value={valueType}
          onChange={(v) => setValueType(v)}
          options={VALUE_TYPE_OPTIONS}
          allowClear={false}
        />
      </FormField>
      <FormField label={t('colValues')} htmlFor="fk-values" description={t('valuesDescription')}>
        <Input
          id="fk-values"
          value={valuesText}
          onChange={(e) => setValuesText(e.target.value)}
          placeholder={t('valuesPlaceholder')}
        />
      </FormField>
      <FormField label={t('colLabel')} htmlFor="fk-label" description={t('labelDescription')}>
        <Input
          id="fk-label"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={t('labelPlaceholder')}
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !key.trim()}>
          {t('saveFacet')}
        </Button>
      </div>
    </div>
  );
}
