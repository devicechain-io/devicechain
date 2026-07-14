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

const MEMBER_TYPE_OPTIONS: ComboboxOption[] = FACET_MEMBER_TYPES.map((t) => ({
  value: t,
  label: t.charAt(0).toUpperCase() + t.slice(1),
}));

const VALUE_TYPE_OPTIONS: ComboboxOption[] = FACET_VALUE_TYPES.map((t) => ({
  value: t,
  label: t,
}));

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export default function FacetKeysPage() {
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const [filter, setFilter] = useState<string | undefined>(undefined);
  const [creating, setCreating] = useState(false);
  const [version, reload] = useReload();
  const { data, loading, error } = useQuery(() => listFacetKeys(filter), [filter, version]);

  const facets = data ?? [];

  const remove = async (fk: FacetKey) => {
    if (
      !(await confirm({
        title: 'Delete facet',
        description: `Delete the ${fk.memberType} facet “${fk.key}”? Its EntityAttribute values are untouched; only the axis declaration is removed. Any saved dynamic group that references it keeps working. This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteFacetKey(fk.memberType, fk.key);
      toast(`Facet “${fk.key}” deleted`);
      reload();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <PageShell
      title="Facets"
      description="Classification axes for browsing and filtering — declare which attribute keys are facets"
      banner="dashboard"
      action={
        canWrite ? (
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} /> New facet
          </Button>
        ) : undefined
      }
    >
      <FormDrawer open={creating} onOpenChange={setCreating} title="New facet">
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
        <FilterChip label="All" active={filter === undefined} onClick={() => setFilter(undefined)} />
        {FACET_MEMBER_TYPES.map((t) => (
          <FilterChip
            key={t}
            label={titleCase(t)}
            active={filter === t}
            onClick={() => setFilter(t)}
          />
        ))}
      </div>

      {loading ? (
        <LoadingState description="Loading facets…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : facets.length === 0 ? (
        <EmptyState description="No facets declared yet." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Member</DataTableHeaderCell>
            <DataTableHeaderCell>Key</DataTableHeaderCell>
            <DataTableHeaderCell>Type</DataTableHeaderCell>
            <DataTableHeaderCell>Values</DataTableHeaderCell>
            <DataTableHeaderCell>Label</DataTableHeaderCell>
            <DataTableHeaderCell> </DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {facets.map((fk) => {
              const isSystem = fk.source === 'system';
              return (
                <DataTableRow key={fk.id}>
                  <DataTableCell className="text-muted-foreground">
                    {titleCase(fk.memberType)}
                  </DataTableCell>
                  <DataTableCell className="font-medium text-foreground">
                    <span className="flex items-center gap-2">
                      {fk.key}
                      {isSystem && <Badge variant="secondary">system</Badge>}
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
                        <Trash2 size={14} /> Delete
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
      setFormError('A facet needs an attribute key.');
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
      <FormField label="Member family" htmlFor="fk-member">
        <Combobox
          id="fk-member"
          value={memberType}
          onChange={(v) => setMemberType(v)}
          options={MEMBER_TYPE_OPTIONS}
          allowClear={false}
        />
      </FormField>
      <FormField label="Attribute key" htmlFor="fk-key">
        <Input
          id="fk-key"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="climate"
        />
      </FormField>
      <FormField label="Value type" htmlFor="fk-type">
        <Combobox
          id="fk-type"
          value={valueType}
          onChange={(v) => setValueType(v)}
          options={VALUE_TYPE_OPTIONS}
          allowClear={false}
        />
      </FormField>
      <FormField label="Values" htmlFor="fk-values" description="Optional. Comma-separated vocabulary for typeahead; leave blank for free-form.">
        <Input
          id="fk-values"
          value={valuesText}
          onChange={(e) => setValuesText(e.target.value)}
          placeholder="arid, temperate, tropical"
        />
      </FormField>
      <FormField label="Label" htmlFor="fk-label" description="Optional. Display name for the axis; defaults to the key.">
        <Input
          id="fk-label"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Climate"
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !key.trim()}>
          Save facet
        </Button>
      </div>
    </div>
  );
}
