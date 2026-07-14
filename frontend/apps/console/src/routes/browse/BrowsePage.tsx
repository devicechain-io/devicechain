// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The faceted browse + dynamic-group consumer (ADR-061 G4) — the end-to-end proof of
// the selector engine. A user picks a member family, composes a filter from its
// declared facet axes (G2), sees a LIVE "matches N" count as they edit (previewSelector,
// evaluated by lowering the composed CEL to an indexed query — never per-row CEL), then
// saves the composed selector as a dynamic EntityGroup whose members resolve eval-on-read.
// The composed CEL is shown verbatim: what you preview is exactly what the group stores.

import { useMemo, useState } from 'react';
import { Plus, Filter, Users } from 'lucide-react';
import { useQuery } from '@/lib/hooks/use-query';
import { useDebouncedValue } from '@/lib/hooks/use-debounced-value';
import { listFacetKeys, FACET_MEMBER_TYPES, type FacetKey } from '@/lib/api/facet-keys';
import {
  previewSelector,
  createDynamicGroup,
  listDynamicGroups,
  resolveGroupMembers,
  type DynamicGroup,
  type GroupMember,
} from '@/lib/api/browse';
import { buildSelector, type FacetCondition, type FacetOperator } from '@/lib/selector';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { FormField } from '@/components/ui/form-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { EmptyState } from '@/components/ui/empty-state';
import { useToast } from '@/components/ui/toast';
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

const PREVIEW_PAGE_SIZE = 25;

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// The per-facet condition the UI holds. `operator === 'off'` means the facet is not
// contributing; every other operator maps to a lowerable CEL leaf (see lib/selector).
type ConditionState = { operator: FacetOperator | 'off'; values: string[] };

// The operators offered for a facet's value type. `present` ("has any value") works
// for every type; equality for all scalars; inequality + ordering only where they lower.
function operatorOptions(valueType: string): ComboboxOption[] {
  const base: ComboboxOption[] = [{ value: 'off', label: '— (ignore)' }];
  if (valueType === 'JSON') {
    return [...base, { value: 'present', label: 'has any value' }];
  }
  const opts: ComboboxOption[] = [
    ...base,
    { value: 'eq', label: 'is' },
    { value: 'neq', label: 'is not' },
  ];
  if (valueType === 'LONG' || valueType === 'DOUBLE') {
    opts.push(
      { value: 'lt', label: '<' },
      { value: 'lte', label: '≤' },
      { value: 'gt', label: '>' },
      { value: 'gte', label: '≥' },
    );
  }
  opts.push({ value: 'present', label: 'has any value' });
  return opts;
}

export default function BrowsePage() {
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const [family, setFamily] = useState<string>(FACET_MEMBER_TYPES[0]);
  // Conditions are keyed by facet key and reset when the family changes.
  const [conditions, setConditions] = useState<Record<string, ConditionState>>({});
  const [saving, setSaving] = useState(false);
  const [groupsVersion, reloadGroups] = useReload();

  const facetsQ = useQuery(() => listFacetKeys(family), [family]);
  const facets = facetsQ.data ?? [];

  const setFamilyAndReset = (f: string) => {
    setFamily(f);
    setConditions({});
  };

  const setCondition = (key: string, next: ConditionState) => {
    setConditions((prev) => ({ ...prev, [key]: next }));
  };

  // Compose the picked facets into a CEL selector. Only facets with a live operator
  // contribute; the composer drops (and explains) an incomplete or ill-typed condition.
  const built = useMemo(() => {
    const list: FacetCondition[] = [];
    for (const f of facets) {
      const c = conditions[f.key];
      if (!c || c.operator === 'off') continue;
      list.push({
        key: f.key,
        valueType: f.valueType as FacetCondition['valueType'],
        operator: c.operator,
        values: c.values,
        label: f.label || f.key,
      });
    }
    return buildSelector(list);
  }, [facets, conditions]);

  return (
    <PageShell
      title="Browse"
      description="Filter a family by its facets, preview the matches live, and save the filter as a dynamic group"
      banner="dashboard"
    >
      {/* Member-family picker. Facets, the composed selector, and saved dynamic groups
          all scope to one family at a time. */}
      <div className="mb-6 flex flex-wrap gap-2">
        {FACET_MEMBER_TYPES.map((t) => (
          <FilterChip
            key={t}
            label={titleCase(t)}
            active={family === t}
            onClick={() => setFamilyAndReset(t)}
          />
        ))}
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Axes */}
        <section>
          <h2 className="mb-3 flex items-center gap-2 text-sm font-medium text-foreground">
            <Filter size={15} /> Facet axes
          </h2>
          {facetsQ.loading ? (
            <LoadingState description="Loading facets…" />
          ) : facetsQ.error ? (
            <ErrorState description={facetsQ.error} />
          ) : facets.length === 0 ? (
            <EmptyState description="No facets declared for this family. Declare axes on the Facets screen first." />
          ) : (
            <div className="space-y-3">
              {facets.map((f) => (
                <FacetAxis
                  key={f.id}
                  facet={f}
                  state={conditions[f.key] ?? { operator: 'off', values: [] }}
                  onChange={(next) => setCondition(f.key, next)}
                />
              ))}
            </div>
          )}
        </section>

        {/* Preview */}
        <section>
          <h2 className="mb-3 flex items-center gap-2 text-sm font-medium text-foreground">
            <Users size={15} /> Matches
          </h2>
          {/* Key by family so a family switch remounts the panel, resetting its
              debounce state — otherwise the old family's selector lingers for one
              debounce window and fires a spurious cross-family preview. */}
          <PreviewPanel
            key={family}
            family={family}
            built={built}
            canSave={canWrite}
            onSave={() => setSaving(true)}
          />
        </section>
      </div>

      {/* Saved dynamic groups for this family */}
      <SavedGroups family={family} version={groupsVersion} />

      <FormDrawer open={saving} onOpenChange={setSaving} title="Save as dynamic group">
        {built.selector && (
          <SaveGroupForm
            family={family}
            selector={built.selector}
            onDone={() => {
              setSaving(false);
              reloadGroups();
            }}
          />
        )}
      </FormDrawer>
    </PageShell>
  );
}

// One facet's condition row: an operator picker plus a value control that fits the
// operator + the facet's value type. A vocabulary-backed STRING facet offers a
// multi-select for equality (an OR of values); everything else is a single input.
function FacetAxis({
  facet,
  state,
  onChange,
}: {
  facet: FacetKey;
  state: ConditionState;
  onChange: (next: ConditionState) => void;
}) {
  const opts = useMemo(() => operatorOptions(facet.valueType), [facet.valueType]);
  const vocab = facet.values ?? [];
  const needsValue = state.operator !== 'off' && state.operator !== 'present';
  const isMulti = state.operator === 'eq' && facet.valueType === 'STRING' && vocab.length > 0;

  return (
    <div className="rounded-lg border border-border p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-sm font-medium text-foreground">
          {facet.label || facet.key}
          <span className="font-mono text-xs text-muted-foreground">{facet.valueType}</span>
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-40">
          <Combobox
            value={state.operator}
            onChange={(v) => onChange({ operator: v as ConditionState['operator'], values: [] })}
            options={opts}
            allowClear={false}
          />
        </div>
        {needsValue &&
          (isMulti ? (
            <div className="min-w-52 flex-1">
              <MultiSelect
                options={vocab.map((v) => ({ value: v, label: v }))}
                value={state.values}
                onChange={(values) => onChange({ ...state, values })}
                placeholder="any of…"
              />
            </div>
          ) : facet.valueType === 'BOOLEAN' ? (
            <div className="w-32">
              <Combobox
                value={state.values[0] ?? ''}
                onChange={(v) => onChange({ ...state, values: v ? [v] : [] })}
                options={[
                  { value: 'true', label: 'true' },
                  { value: 'false', label: 'false' },
                ]}
                allowClear={false}
              />
            </div>
          ) : vocab.length > 0 ? (
            <div className="w-52">
              <Combobox
                value={state.values[0] ?? ''}
                onChange={(v) => onChange({ ...state, values: v ? [v] : [] })}
                options={vocab.map((v) => ({ value: v, label: v }))}
              />
            </div>
          ) : (
            <Input
              className="w-52"
              value={state.values[0] ?? ''}
              onChange={(e) => onChange({ ...state, values: e.target.value ? [e.target.value] : [] })}
              placeholder={facet.valueType === 'STRING' ? 'value' : 'number'}
              inputMode={
                facet.valueType === 'LONG' || facet.valueType === 'DOUBLE' ? 'decimal' : 'text'
              }
            />
          ))}
      </div>
    </div>
  );
}

// The live preview: the composed CEL, the eval-on-read match count + a sample of members,
// and the Save action. previewSelector is debounced so it fires when the user pauses, and
// a publish-gate rejection comes back as an inline error rather than a thrown fault.
function PreviewPanel({
  family,
  built,
  canSave,
  onSave,
}: {
  family: string;
  built: { selector: string | null; issues: string[] };
  canSave: boolean;
  onSave: () => void;
}) {
  const debounced = useDebouncedValue(built.selector, 400);
  const preview = useQuery(
    () => (debounced ? previewSelector(family, debounced, PREVIEW_PAGE_SIZE) : Promise.resolve(null)),
    [family, debounced],
  );

  const result = preview.data;
  const members: GroupMember[] = result?.members?.results ?? [];
  const total = result?.members?.pagination.totalRecords ?? 0;
  // The user has edited faster than the debounce: the shown count/validity is for the
  // previous selector, not the one in the Selector box. Dim it and hold Save until the
  // preview catches up, so a click never saves an unvalidated, uncounted selector.
  const pending = built.selector !== debounced;

  return (
    <div className="space-y-3">
      {/* The composed selector, verbatim — the authored contract the backend lowers. */}
      <div className="rounded-lg border border-border bg-muted/40 p-3">
        <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Selector
        </div>
        {built.selector ? (
          <code className="block break-words font-mono text-xs text-foreground">
            {built.selector}
          </code>
        ) : (
          <span className="text-sm text-muted-foreground">
            Pick one or more facet axes to compose a filter.
          </span>
        )}
      </div>

      {built.issues.length > 0 && (
        <div className="space-y-1">
          {built.issues.map((issue, i) => (
            <p key={i} className="text-xs text-amber-600 dark:text-amber-500">
              {issue}
            </p>
          ))}
        </div>
      )}

      {built.selector && (
        <>
          {preview.loading ? (
            <LoadingState description="Evaluating…" />
          ) : preview.error ? (
            // A genuine fault (auth, network) — distinct from a publish-gate rejection,
            // which comes back as result.valid === false below.
            <ErrorState description={preview.error} />
          ) : result && !result.valid ? (
            <ErrorBanner message={result.error ?? 'This filter is not expressible yet.'} />
          ) : result ? (
            <>
              <div className="flex items-center justify-between">
                <p className={cn('text-sm text-foreground', pending && 'opacity-40')}>
                  <span className="font-semibold">{total}</span>{' '}
                  {total === 1 ? family : `${family}s`} match
                </p>
                {canSave && (
                  <Button size="sm" onClick={onSave} disabled={pending}>
                    <Plus size={15} /> Save as group
                  </Button>
                )}
              </div>
              {members.length === 0 ? (
                <EmptyState description="No members match this filter." />
              ) : (
                <MembersTable members={members} truncated={total > members.length} total={total} />
              )}
            </>
          ) : null}
        </>
      )}
    </div>
  );
}

function MembersTable({
  members,
  truncated,
  total,
}: {
  members: GroupMember[];
  truncated: boolean;
  total: number;
}) {
  return (
    <div>
      <DataTable>
        <DataTableHead>
          <DataTableHeaderCell>Token</DataTableHeaderCell>
          <DataTableHeaderCell>Id</DataTableHeaderCell>
        </DataTableHead>
        <DataTableBody>
          {members.map((m) => (
            <DataTableRow key={m.id}>
              <DataTableCell className="font-medium text-foreground">{m.token}</DataTableCell>
              <DataTableCell className="font-mono text-xs text-muted-foreground">
                {m.id}
              </DataTableCell>
            </DataTableRow>
          ))}
        </DataTableBody>
      </DataTable>
      {truncated && (
        <p className="mt-2 text-xs text-muted-foreground">
          Showing the first {members.length} of {total}.
        </p>
      )}
    </div>
  );
}

// The saved dynamic groups for the current family — token, name, its selector, and a
// members view that resolves eval-on-read off the stored selector.
function SavedGroups({ family, version }: { family: string; version: number }) {
  const { data, loading, error } = useQuery(() => listDynamicGroups(family), [family, version]);
  const [viewing, setViewing] = useState<DynamicGroup | null>(null);
  const groups = data ?? [];

  return (
    <section className="mt-8">
      <h2 className="mb-3 text-sm font-medium text-foreground">Saved dynamic groups</h2>
      {loading ? (
        <LoadingState description="Loading groups…" />
      ) : error ? (
        <ErrorState description={error} />
      ) : groups.length === 0 ? (
        <EmptyState description="No dynamic groups for this family yet. Compose a filter above and save it." />
      ) : (
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>Token</DataTableHeaderCell>
            <DataTableHeaderCell>Name</DataTableHeaderCell>
            <DataTableHeaderCell>Selector</DataTableHeaderCell>
            <DataTableHeaderCell> </DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {groups.map((g) => (
              <DataTableRow key={g.id}>
                <DataTableCell className="font-medium text-foreground">
                  <span className="flex items-center gap-2">
                    {g.token}
                    <Badge variant="secondary">dynamic</Badge>
                  </span>
                </DataTableCell>
                <DataTableCell className="text-muted-foreground">{g.name || '—'}</DataTableCell>
                <DataTableCell className="max-w-xs truncate font-mono text-xs text-muted-foreground">
                  {g.selector ?? '—'}
                </DataTableCell>
                <DataTableCell className="text-right">
                  <Button variant="ghost" size="sm" onClick={() => setViewing(g)}>
                    <Users size={14} /> Members
                  </Button>
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}

      <FormDrawer
        open={viewing !== null}
        onOpenChange={(open) => !open && setViewing(null)}
        title={viewing ? `Members of “${viewing.token}”` : ''}
      >
        {viewing && <GroupMembersView token={viewing.token} family={family} />}
      </FormDrawer>
    </section>
  );
}

function GroupMembersView({ token, family }: { token: string; family: string }) {
  const { data, loading, error } = useQuery(
    () => resolveGroupMembers(token, { pageNumber: 1, pageSize: 100 }),
    [token],
  );

  if (loading) return <LoadingState description="Resolving members…" />;
  if (error) return <ErrorState description={error} />;
  const page = data ?? { results: [], totalRecords: 0 };
  if (page.results.length === 0) return <EmptyState description="This group has no members." />;

  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        <span className="font-semibold text-foreground">{page.totalRecords}</span>{' '}
        {page.totalRecords === 1 ? family : `${family}s`} — resolved live from the selector.
      </p>
      <MembersTable
        members={page.results}
        truncated={page.totalRecords > page.results.length}
        total={page.totalRecords}
      />
    </div>
  );
}

// The save form: a token (+ optional name) for the composed selector. The backend
// re-compiles + cost-gates the selector at create, so an invalid one is refused here too.
function SaveGroupForm({
  family,
  selector,
  onDone,
}: {
  family: string;
  selector: string;
  onDone: () => void;
}) {
  const { toast } = useToast();
  const [token, setToken] = useState('');
  const [name, setName] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    if (!token.trim()) {
      setFormError('A group needs a token.');
      return;
    }
    setBusy(true);
    try {
      await createDynamicGroup({
        memberType: family,
        selector,
        token: token.trim(),
        name: name.trim() || undefined,
      });
      toast(`Dynamic group “${token.trim()}” saved`);
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
      <div className="rounded-lg border border-border bg-muted/40 p-3">
        <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Selector
        </div>
        <code className="block break-words font-mono text-xs text-foreground">{selector}</code>
      </div>
      <FormField label="Token" htmlFor="grp-token">
        <TokenField
          id="grp-token"
          entityType="group"
          value={token}
          onChange={setToken}
          seed={name}
          placeholder="arid-fleet"
        />
      </FormField>
      <FormField label="Name" htmlFor="grp-name" description="Optional display name.">
        <Input
          id="grp-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Arid-climate fleet"
        />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || !token.trim()}>
          Save group
        </Button>
      </div>
    </div>
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
