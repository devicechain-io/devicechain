// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The attributes panel for a facetable entity — devices, assets, areas, customers
// (ADR-061 follow-on: facet-value authoring). One component, four mount points.
//
// 🔴 WHAT THIS FIXES IS NOT A MISSING FORM, IT IS AN INERT FEATURE. The Facets
// registry declares classification AXES; it stores no values. Browse composes those
// axes into a CEL selector that lowers to a semi-join over EntityAttribute rows. Both
// screens worked, and nothing anywhere could write the rows in between — so a tenant
// could declare `climate`, pick `arid` from the typeahead the registry supplied, and
// get "matches 0" forever with every affordance on screen reporting success.
//
// 🔴 TWO WAYS TO REPRODUCE THAT SYMPTOM FROM INSIDE THIS PANEL, both silent:
//
//  1. THE WRONG SCOPE. The lowering reads exactly one EntityAttribute scope (SHARED).
//     A CLIENT- or SERVER-scoped row with the right key and the right value is a
//     perfectly valid row that no selector can see. Every write here goes through
//     lib/api/entity-attributes.ts, which owns that constant; this file never names a
//     scope for a write.
//  2. THE WRONG VALUE TYPE. A scalar leaf pins value_type (`ea.value_type = 'STRING'`,
//     or `IN ('LONG','DOUBLE')` for a numeric comparison), so "3" stored as STRING is
//     invisible to `attr["floors"] == 3`. The value type therefore comes from the
//     FACET KEY'S DECLARATION, never from inspecting the text the user typed.
//
//     There was a third door into the same room, and it is now shut at the SERVER, which is
//     the only place it could be shut properly: a numeric or boolean write the declared type
//     cannot hold used to be coerced to unset rather than refused, so "hot" on a LONG facet
//     stored a row with a NULL value that read back without error and matched nothing. The
//     check below is the FRIENDLY MESSAGE, not the gate — it validates by pattern while the
//     server validates by parser, and the two disagree exactly where it matters (`1e400`
//     matches any decimal pattern and overflows float64 to +Inf).
//
// Reads every scope and shows the non-SHARED rows read-only: "I set climate and Browse
// still says 0" has a visible cause when the device reported its own `climate`.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import {
  listFacetKeys,
  FACET_VALUE_TYPES,
  type FacetKey,
} from '@/lib/api/facet-keys';
import {
  listEntityAttributes,
  setFacetValue,
  clearFacetValue,
  FACET_VALUE_SCOPE,
  type EntityAttribute,
} from '@/lib/api/entity-attributes';
import { listDynamicGroups, type DynamicGroup } from '@/lib/api/browse';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { EmptyState } from '@/components/ui/empty-state';
import { ErrorBanner } from '@/components/ui/error-banner';
import { ErrorState } from '@/components/ui/error-state';
import { FormField } from '@/components/ui/form-field';
import { Input } from '@/components/ui/input';
import { LoadingState } from '@/components/ui/loading-state';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useToast } from '@/components/ui/toast';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';

// Value types are technical enum tokens (STRING/LONG/…), never localized.
const VALUE_TYPE_OPTIONS: ComboboxOption[] = FACET_VALUE_TYPES.map((vt) => ({ value: vt }));

// A BOOLEAN facet's two storable values — the raw stored text, never localized (it is
// what `attr[k] == true` lowers to comparing against, not a translated concept).
const BOOLEAN_OPTIONS: ComboboxOption[] = [{ value: 'true' }, { value: 'false' }];

/**
 * facetValueIssue reports why `raw` cannot be stored under `valueType`, as an i18n key
 * in the `facets` namespace — or null when it is storable.
 *
 * 🔴 THIS IS THE MESSAGE, NOT THE GATE. The server now REFUSES a present value its declared
 * type cannot hold (`normalizeAttributeValue`), so a wrong value fails whether or not this
 * function catches it. What this buys is a specific, translated sentence beside the field
 * instead of a server error in a banner — and it deliberately does NOT try to reproduce the
 * parser: a pattern cannot see that `1e400` overflows float64 to +Inf, or that a 20-digit
 * LONG overflows int64, which is exactly why the refusal has to live on the server.
 *
 * The forms accepted are the ones the CONSOLE'S SELECTOR COMPOSER can write as a CEL literal
 * (lib/selector.ts `literalFor`), which is narrower than what storage would take: a value you
 * can store but cannot compose a filter against is a value Browse can never match.
 *
 * ⚠️ JSON IS THE ACKNOWLEDGED EXCEPTION TO THAT SENTENCE, not an oversight in it.
 * `literalFor('JSON')` returns null, so a JSON facet's value is perfectly storable and has no
 * scalar literal form to compose against — the only operator Browse offers for a JSON axis is
 * presence. This function therefore checks a JSON value is well-formed and nothing more;
 * whether a JSON facet should be value-authorable at all is a separate question, filed.
 */
export function facetValueIssue(valueType: string, raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed.length === 0) return 'valueRequired';
  switch (valueType) {
    case 'LONG':
      return /^-?[0-9]+$/.test(trimmed) ? null : 'invalidLong';
    case 'DOUBLE':
      return /^-?([0-9]+(\.[0-9]+)?|\.[0-9]+)([eE][+-]?[0-9]+)?$/.test(trimmed)
        ? null
        : 'invalidDouble';
    case 'BOOLEAN':
      return trimmed === 'true' || trimmed === 'false' ? null : 'invalidBoolean';
    case 'JSON':
      try {
        JSON.parse(trimmed);
        return null;
      } catch {
        return 'invalidJson';
      }
    default:
      return null; // STRING, and any value type the server grows that we do not model
  }
}

/**
 * selectorReferencesKey reports whether a saved dynamic group's selector mentions this
 * facet key, so the panel can warn that editing the value moves the entity in or out of
 * a group SYNCHRONOUSLY (the membership read-model is recomputed inside the write's
 * transaction, ADR-062 S2).
 *
 * A textual match against the two shapes the composer emits — `attr["k"]` and
 * `"k" in attr` — NOT a CEL parse. It is a display hint and is allowed to miss a
 * hand-written selector; the authority on membership is the server, which recomputes
 * whatever this says.
 */
export function selectorReferencesKey(selector: string | null | undefined, key: string): boolean {
  if (!selector) return false;
  const quoted = JSON.stringify(key);
  return selector.includes(`attr[${quoted}]`) || selector.includes(`${quoted} in attr`);
}

/**
 * facetRowKey identifies the stored state a facet row was built from, so React remounts the
 * row — and re-seeds its draft — whenever the server's answer changes. Exported for the
 * test that pins it: the whole point is that it moves when the stored row does, and a key
 * that silently stops moving leaves the previous draft on screen looking saved.
 */
export function facetRowKey(facet: FacetKey, current: EntityAttribute | undefined): string {
  return [facet.id, current?.id ?? '', current?.valueType ?? '', current?.value ?? ''].join(':');
}

export function EntityAttributesPanel({
  entityType,
  entityToken,
}: {
  /** The member family: 'device' | 'asset' | 'area' | 'customer'. */
  entityType: string;
  /** The entity's token — setEntityAttribute resolves the owner by (type, token). */
  entityToken: string;
}) {
  const { t } = useTranslation('facets');
  const { claims } = useAuth();
  // 🔴 device:write gates setEntityAttribute and deleteEntityAttribute for EVERY entity
  // family, not just devices — device-management owns the attribute table and its
  // authorities are per-service, not per-entity. So editing an AREA's attributes needs
  // device:write. That is the backend's contract today; the panel mirrors it rather than
  // inventing a second answer that would show affordances the server then refuses.
  const canWrite = hasAuthority(claims, 'device:write');
  const [version, reload] = useReload();
  const [adding, setAdding] = useState(false);

  const facetsQ = useQuery(() => listFacetKeys(entityType), [entityType]);
  const attrsQ = useQuery(
    () => listEntityAttributes(entityType, entityToken),
    [entityType, entityToken, version],
  );
  // The group hint is advisory: a failure to load it must not take the panel down, so
  // its error is folded to an empty list rather than surfaced.
  const groupsQ = useQuery(
    () => listDynamicGroups(entityType).catch((): DynamicGroup[] => []),
    [entityType],
  );

  const facets = facetsQ.data ?? [];
  const attributes = attrsQ.data ?? [];
  const groups = groupsQ.data ?? [];

  // The facet's current value: a SHARED row whose key matches the declaration. A row of
  // the same key in another scope is NOT this facet's value — that is the whole point.
  const facetValue = useMemo(() => {
    const byKey = new Map<string, EntityAttribute>();
    for (const a of attributes) {
      if (a.scope === FACET_VALUE_SCOPE) byKey.set(a.attrKey, a);
    }
    return byKey;
  }, [attributes]);

  // Everything the facet section does not already show: another scope's row, or a
  // SHARED row whose key no facet declares.
  const otherAttributes = useMemo(() => {
    const declared = new Set(facets.map((f) => f.key));
    return attributes.filter((a) => !(a.scope === FACET_VALUE_SCOPE && declared.has(a.attrKey)));
  }, [attributes, facets]);

  const loading = facetsQ.loading || attrsQ.loading;
  const error = facetsQ.error ?? attrsQ.error;

  // 🔴 THE FOLD IS OVER BOTH READS, AND IT HAS TO BE. The declarations and the values come
  // from two independent queries, and a panel that rendered on the declarations alone would
  // answer a FAILED attribute read with "every facet is unset" — every row showing Not set,
  // a Save button, no Clear, and the error never surfaced. That is a wrong ANSWER rather
  // than an error, which is the exact class of failure this whole panel exists to end: it
  // would invite the user to re-author values that already exist, and one save would then
  // overwrite whatever was really there.
  //
  // `data == null` rather than `loading`, because useQuery keeps the previous data across a
  // refetch — a background reload should update in place, not blank the tab.
  const nothingLoaded = facetsQ.data == null || attrsQ.data == null;
  if (loading && nothingLoaded) {
    return <LoadingState description={t('valuesLoading')} />;
  }
  if (error && nothingLoaded) {
    return <ErrorState description={error} />;
  }

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-sm font-medium text-foreground">{t('valuesTitle')}</h3>
        <p className="mt-1 text-sm text-muted-foreground">{t('valuesIntro')}</p>
      </div>

      {facets.length === 0 ? (
        <EmptyState description={t('noFacetsDeclared')} />
      ) : (
        <div className="space-y-3">
          {facets.map((facet) => (
            <FacetValueRow
              // Re-key on the stored ROW'S IDENTITY — its id, its value and its value type —
              // not on the value alone. The draft is state seeded from the server, so the row
              // has to remount whenever what the server holds changes; keying on the value
              // alone made a write that did not change the text (a re-type of a stranded
              // value, or a save the server refused) leave the box showing the text the user
              // typed as though it had been stored.
              key={facetRowKey(facet, facetValue.get(facet.key))}
              facet={facet}
              entityType={entityType}
              entityToken={entityToken}
              current={facetValue.get(facet.key)}
              canWrite={canWrite}
              groups={groups.filter((g) => selectorReferencesKey(g.selector, facet.key))}
              onChanged={reload}
            />
          ))}
        </div>
      )}

      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h3 className="text-sm font-medium text-foreground">{t('otherTitle')}</h3>
            <p className="mt-1 text-sm text-muted-foreground">{t('otherIntro')}</p>
          </div>
          {canWrite && !adding && (
            <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
              <Plus size={15} /> {t('addAttribute')}
            </Button>
          )}
        </div>

        {adding && (
          <FreeFormAttributeForm
            entityType={entityType}
            entityToken={entityToken}
            onDone={() => {
              setAdding(false);
              reload();
            }}
            onCancel={() => setAdding(false)}
          />
        )}

        {otherAttributes.length === 0 ? (
          <EmptyState description={t('otherEmpty')} />
        ) : (
          <OtherAttributesTable
            entityType={entityType}
            entityToken={entityToken}
            attributes={otherAttributes}
            canWrite={canWrite}
            onChanged={reload}
          />
        )}
      </div>
    </div>
  );
}

// One declared facet: its label, its declared value type, an editor for the value, and
// the two actions that are NOT symmetric — save writes, clear DELETES the row.
function FacetValueRow({
  facet,
  entityType,
  entityToken,
  current,
  canWrite,
  groups,
  onChanged,
}: {
  facet: FacetKey;
  entityType: string;
  entityToken: string;
  current: EntityAttribute | undefined;
  canWrite: boolean;
  groups: DynamicGroup[];
  onChanged: () => void;
}) {
  const { t } = useTranslation('facets');
  const { toast } = useToast();
  const [draft, setDraft] = useState(current?.value ?? '');
  const [issue, setIssue] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const vocab = facet.values ?? [];
  const fieldId = `facet-${facet.id}`;
  const listId = `facet-vocab-${facet.id}`;
  const name = facet.label || facet.key;
  // The axis name IS the control's label, so it is marked up as one — a row of value
  // boxes whose only identification is a heading beside them announces nothing. A
  // BOOLEAN facet's editor is a Combobox, whose trigger is a <button>: a <label for>
  // does not name a button, so that branch carries the name as `ariaLabel` instead.
  const isBoolean = facet.valueType === 'BOOLEAN';
  // A system facet's values come from modeled columns, not EntityAttribute rows, so
  // there is nothing here to author (the seam is declared but unbuilt).
  const editable = canWrite && facet.source !== 'system';

  // 🔴 A STRANDED VALUE MUST NOT BE PRESENTED AS A CORRECT ONE. Re-declaring a facet's value
  // type rewrites the DECLARATION and no rows, so every value authored under the old type
  // stays exactly where it was — stored, readable, and no longer matched by its own axis.
  // Printing `facet.valueType` beside a value whose row carries a different one turns that
  // into an active lie: three devices reading `arid · STRING` while Browse matches none of
  // them. The row says what is actually stored, and that saving re-types it — which is true,
  // because setFacetValue writes the declared type and the write is an upsert on the key.
  const stranded = current != null && current.valueType !== facet.valueType;

  const save = async () => {
    const problem = facetValueIssue(facet.valueType, draft);
    if (problem) {
      setIssue(t(problem, { value: draft.trim(), type: facet.valueType }));
      return;
    }
    setIssue(null);
    setBusy(true);
    try {
      await setFacetValue({
        entityType,
        entity: entityToken,
        attrKey: facet.key,
        // 🔴 The DECLARED type, not one inferred from the text. See the file header.
        valueType: facet.valueType,
        value: draft.trim(),
      });
      toast(t('valueSaved', { key: facet.key }));
      onChanged();
    } catch (err) {
      setIssue(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setBusy(true);
    try {
      await clearFacetValue({ entityType, entity: entityToken, attrKey: facet.key });
      toast(t('valueCleared', { key: facet.key }));
      onChanged();
    } catch (err) {
      setIssue(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-lg border border-border p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        {isBoolean ? (
          <span className="text-sm font-medium text-foreground">{name}</span>
        ) : (
          <label htmlFor={fieldId} className="text-sm font-medium text-foreground">
            {name}
          </label>
        )}
        <span className="font-mono text-xs text-muted-foreground">{facet.valueType}</span>
        {facet.source === 'system' && <Badge variant="secondary">{t('system')}</Badge>}
        {current == null && <Badge variant="outline">{t('notSet')}</Badge>}
        {stranded && (
          <Badge variant="warning">
            {t('strandedType', { stored: current.valueType })}
          </Badge>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {isBoolean ? (
          <div className="w-40">
            <Combobox
              id={fieldId}
              ariaLabel={name}
              value={draft}
              onChange={setDraft}
              options={BOOLEAN_OPTIONS}
              disabled={!editable || busy}
              allowClear={false}
            />
          </div>
        ) : (
          <>
            {/* A declared vocabulary SUGGESTS, it never constrains — exactly as the
                Browse axis does. A datalist gives native suggestion + filtering while
                leaving a brand-new value typeable. */}
            <Input
              id={fieldId}
              className="w-64"
              list={vocab.length > 0 ? listId : undefined}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder={facet.key}
              disabled={!editable || busy}
              inputMode={
                facet.valueType === 'LONG' || facet.valueType === 'DOUBLE' ? 'decimal' : 'text'
              }
            />
            {vocab.length > 0 && (
              <datalist id={listId}>
                {vocab.map((v) => (
                  <option key={v} value={v} />
                ))}
              </datalist>
            )}
          </>
        )}
        {editable && (
          <>
            <Button size="sm" onClick={save} loading={busy} disabled={busy}>
              {t('saveValue')}
            </Button>
            {current != null && (
              <Button size="sm" variant="ghost" onClick={clear} disabled={busy}>
                {t('clearValue')}
              </Button>
            )}
          </>
        )}
      </div>

      {stranded && (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('strandedHint', { stored: current.valueType, declared: facet.valueType })}
        </p>
      )}

      {issue && <p className="mt-2 text-xs text-destructive">{issue}</p>}

      {groups.length > 0 && (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('usedByGroups', { groups: groups.map((g) => g.name || g.token).join(', ') })}
        </p>
      )}
    </div>
  );
}

// Every row the facet section does not own. Non-SHARED rows are shown read-only and
// scope-labelled on purpose: a device-reported `climate` at CLIENT scope is the single
// most confusing way for Browse to keep saying 0, and hiding it would hide the answer.
function OtherAttributesTable({
  entityType,
  entityToken,
  attributes,
  canWrite,
  onChanged,
}: {
  entityType: string;
  entityToken: string;
  attributes: EntityAttribute[];
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation('facets');
  const { toast } = useToast();
  const confirm = useConfirm();

  const remove = async (attr: EntityAttribute) => {
    if (
      !(await confirm({
        title: t('deleteAttributeTitle'),
        description: t('deleteAttributeConfirm', { key: attr.attrKey, scope: attr.scope }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await clearFacetValue({
        entityType,
        entity: entityToken,
        attrKey: attr.attrKey,
        scope: attr.scope,
      });
      toast(t('valueCleared', { key: attr.attrKey }));
      onChanged();
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  return (
    <DataTable>
      <DataTableHead>
        <DataTableHeaderCell>{t('colKey')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('colScope')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('common:colType')}</DataTableHeaderCell>
        <DataTableHeaderCell>{t('colValue')}</DataTableHeaderCell>
        <DataTableHeaderCell> </DataTableHeaderCell>
      </DataTableHead>
      <DataTableBody>
        {attributes.map((attr) => (
          <DataTableRow key={attr.id}>
            <DataTableCell className="font-medium text-foreground">{attr.attrKey}</DataTableCell>
            <DataTableCell>
              <Badge variant={attr.scope === FACET_VALUE_SCOPE ? 'secondary' : 'outline'}>
                {attr.scope}
              </Badge>
            </DataTableCell>
            <DataTableCell className="font-mono text-xs text-muted-foreground">
              {attr.valueType}
            </DataTableCell>
            <DataTableCell className="max-w-xs truncate text-muted-foreground">
              {attr.value || '—'}
            </DataTableCell>
            <DataTableCell className="text-right">
              {canWrite && attr.scope === FACET_VALUE_SCOPE && (
                <Button variant="ghost" size="sm" onClick={() => void remove(attr)}>
                  <Trash2 size={14} /> {t('delete')}
                </Button>
              )}
            </DataTableCell>
          </DataTableRow>
        ))}
      </DataTableBody>
    </DataTable>
  );
}

// Writing an attribute whose key no facet declares. The registry is a LENS, not a gate:
// the selector engine never consults it, so an undeclared key stores and matches exactly
// like a declared one — it simply has no axis in Browse until somebody declares it.
function FreeFormAttributeForm({
  entityType,
  entityToken,
  onDone,
  onCancel,
}: {
  entityType: string;
  entityToken: string;
  onDone: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation('facets');
  const [key, setKey] = useState('');
  const [valueType, setValueType] = useState<string>('STRING');
  const [value, setValue] = useState('');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const trimmedKey = key.trim();
    if (!trimmedKey) {
      setFormError(t('keyRequired'));
      return;
    }
    const problem = facetValueIssue(valueType, value);
    if (problem) {
      setFormError(t(problem, { value: value.trim(), type: valueType }));
      return;
    }
    setFormError(null);
    setBusy(true);
    try {
      await setFacetValue({
        entityType,
        entity: entityToken,
        attrKey: trimmedKey,
        valueType,
        value: value.trim(),
      });
      onDone();
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3 rounded-lg border border-border p-3">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <p className="text-xs text-muted-foreground">{t('addHint')}</p>
      <div className="flex flex-wrap gap-3">
        <FormField label={t('attributeKey')} htmlFor="attr-key">
          <Input
            id="attr-key"
            className="w-52"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder={t('keyPlaceholder')}
          />
        </FormField>
        <FormField label={t('valueType')} htmlFor="attr-type">
          <div className="w-40">
            <Combobox
              id="attr-type"
              value={valueType}
              onChange={setValueType}
              options={VALUE_TYPE_OPTIONS}
              allowClear={false}
            />
          </div>
        </FormField>
        <FormField label={t('colValue')} htmlFor="attr-value">
          <Input
            id="attr-value"
            className="w-52"
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </FormField>
      </div>
      <div className="flex gap-2">
        <Button size="sm" onClick={submit} loading={busy} disabled={busy}>
          {t('saveValue')}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>
          {t('common:cancel')}
        </Button>
      </div>
    </div>
  );
}
