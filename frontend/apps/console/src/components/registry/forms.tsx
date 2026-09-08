// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Generic registry forms. Every registry "type" entity is a token/name/description
// record, and every "instance" entity is that plus a required reference to its
// type — so a single RegistryTypeForm / RegistryInstanceForm serves all the
// device/asset/customer/area families. A resource adapts the normalized form
// values to its own typed create/update request in its config (see resource.tsx).
//
// 🔴 THESE FORMS EDIT A SUBSET, AND WHAT THE SERVER DOES WITH THE REST DEPENDS ON
// WHICH UPDATE CONTRACT THE FAMILY IS ON. Both are in service, and they want
// OPPOSITE things from the same adapter.
//
// A FULL-REPLACE family rebuilds the stored record from the request, so a field the
// request leaves out is not "unchanged" — it is DELETED. These forms only ever
// collect a name and a description, so an adapter that maps them straight onto a
// create request quietly erases everything else the entity had: its metadata, a
// device's externalId, a type's icon and colours. Nothing about it looks wrong — the
// save succeeds, the toast is cheerful, and the audit trail says the operator edited
// the thing. Such an adapter must start from its family's `…Preserved(entity)`
// projection (see lib/api/*.ts) and override only the fields collected here.
//
// A PARTIAL-UPDATE family leaves an absent field alone, and there the carry-forward
// INVERTS from the fix into the bug: an adapter re-sending fields the form never
// showed is writing them back from a snapshot it read when the page opened, so two
// operators on two tabs each silently overwrite the other. Such an adapter sends
// ONLY what this form collects, and its family's `…Preserved` projection is deleted
// rather than left lying around for someone to reach for.
//
// 🔑 Which contract a family is on is read off its mutation — `$request:
// …UpdateRequest` is partial, `…CreateRequest` is a full replace — never off a list
// kept here by hand.
//
// Two gates hold this in place, and they catch different mistakes:
//
//   * a full-replace `update*` in the API layer takes `Required<…CreateRequest>`, so
//     an adapter that OMITS a field no longer compiles, and each `…Preserved` helper
//     RETURNS `Required<…>`, so a field added to the schema breaks the helper until
//     someone decides what an edit should do with it. A partial `update*` takes the
//     plain `…UpdateRequest`, where omission is the whole point and there is nothing
//     for a type to check;
//   * `routes/resources.test.tsx` walks every registry resource, renames the entity,
//     saves, and then asserts that the ONLY fields that changed are the ones this
//     form edits — reading each family's contract off its document and flipping its
//     assertion accordingly. That is what catches the mistakes a type cannot see: an
//     adapter that names a field and sends the wrong value, a projection with two
//     fields crossed, a spread written the other way round so the operator's edit is
//     the thing that gets discarded, or a carry-forward regrown on a family that has
//     since converted.
//
// Noun-bearing prose is resolved from the `entities` catalog by the family's
// `i18nKey` prefix (`${i18nKey}CreateAction`, `${i18nKey}CreatedToast`, …), so the
// engine never builds a sentence by interpolating a noun — each locale writes
// grammatical text. `entityType` is the technical mask key (ADR-042 P3), separate
// from any display noun.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import type { EntityType } from '@/lib/entity-types';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';

// Capitalize the first letter. Still used by device-profile definition toasts
// (DefinitionsPanel) pending that area's sweep; the registry forms below now take
// their prose from the catalog and no longer call it.
export const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// A minimal registry entity: every family exposes at least these.
interface NamedEntity {
  token: string;
  name?: string | null;
  description?: string | null;
}

// 🔴 `null`, not `undefined`, for a cleared field. Against a full-replace endpoint
// the two are not synonyms in intent even though the server maps both to a nil
// column: `undefined` is what an omission looks like, and an omission is the bug
// this file's header describes. Saying null is the form stating, in the request,
// that it means for the field to be empty.
export interface TypeRequest {
  token: string;
  name: string | null;
  description: string | null;
}

export interface InstanceRequest extends TypeRequest {
  typeToken: string;
}

// ── Type form (device type, asset type, …) ───────────────────────────────

export function RegistryTypeForm<T extends NamedEntity>({
  entity,
  i18nKey,
  entityType,
  checkAvailability,
  create,
  update,
  onDone,
}: {
  entity?: T;
  /** Family prefix in the `entities` catalog, e.g. "deviceType". */
  i18nKey: string;
  /** Mask key for token generation (ADR-042 P3), e.g. "device-type". */
  entityType: EntityType;
  checkAvailability?: (token: string) => Promise<boolean>;
  create: (req: TypeRequest) => Promise<unknown>;
  update: (token: string, req: TypeRequest) => Promise<unknown>;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const e = (suffix: string, opts?: Record<string, unknown>) =>
    t(`entities:${i18nKey}${suffix}`, opts);
  const editing = entity != null;
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const fields = { name: name.trim() || null, description: description.trim() || null };
      if (editing) {
        await update(entity.token, { token: entity.token, ...fields });
        onDone(e('UpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await create({ token: trimmed, ...fields });
        onDone(e('CreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('common:colToken')}
        htmlFor="r-token"
        description={editing ? e('TokenFixed') : undefined}
      >
        {editing ? (
          <Input id="r-token" value={token} disabled />
        ) : (
          <TokenField
            id="r-token"
            entityType={entityType}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={e('TokenPlaceholder')}
            checkAvailability={checkAvailability}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="r-name">
        <Input id="r-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="r-description">
        <Textarea id="r-description" value={description} onChange={(ev) => setDescription(ev.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? t('common:saveChanges') : e('CreateAction')}
        </Button>
      </div>
    </div>
  );
}

// ── Instance form (device, asset, …) ─────────────────────────────────────

export function RegistryInstanceForm<T extends NamedEntity>({
  entity,
  i18nKey,
  entityType,
  defaultTypeToken,
  checkAvailability,
  loadTypes,
  create,
  update,
  onDone,
}: {
  entity?: T;
  /** Family prefix in the `entities` catalog, e.g. "asset". */
  i18nKey: string;
  /** Mask key for token generation (ADR-042 P3), e.g. "asset". */
  entityType: EntityType;
  defaultTypeToken?: string;
  checkAvailability?: (token: string) => Promise<boolean>;
  loadTypes: () => Promise<NamedEntity[]>;
  create: (req: InstanceRequest) => Promise<unknown>;
  update: (token: string, req: InstanceRequest) => Promise<unknown>;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const e = (suffix: string, opts?: Record<string, unknown>) =>
    t(`entities:${i18nKey}${suffix}`, opts);
  const editing = entity != null;
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [typeToken, setTypeToken] = useState(defaultTypeToken ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const { data: types } = useQuery(loadTypes, []);
  const options: ComboboxOption[] = (types ?? []).map((ty) => ({
    value: ty.token,
    label: ty.name || ty.token,
    description: ty.name ? ty.token : undefined,
  }));
  const noTypes = types != null && options.length === 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const fields = {
        name: name.trim() || null,
        description: description.trim() || null,
        typeToken,
      };
      if (editing) {
        await update(entity.token, { token: entity.token, ...fields });
        onDone(e('UpdatedToast', { token: entity.token }));
      } else {
        const trimmed = token.trim();
        await create({ token: trimmed, ...fields });
        onDone(e('CreatedToast', { token: trimmed }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {/* Type then token on one line: pick the classifying type first, then name
          the instance. (Live token-availability checking can hang off the token
          field later.) */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <FormField
          label={e('TypeLabel')}
          htmlFor="r-type"
          description={noTypes ? e('TypeEmpty') : undefined}
        >
          <Combobox
            id="r-type"
            value={typeToken}
            onChange={setTypeToken}
            options={options}
            placeholder={e('TypeSelect')}
            disabled={noTypes}
          />
        </FormField>
        <FormField
          label={t('common:colToken')}
          htmlFor="r-token"
          description={editing ? e('TokenFixed') : undefined}
        >
          {editing ? (
            <Input id="r-token" value={token} disabled />
          ) : (
            <TokenField
              id="r-token"
              entityType={entityType}
              value={token}
              onChange={setToken}
              seed={name}
              placeholder={e('TokenPlaceholder')}
              checkAvailability={checkAvailability}
            />
          )}
        </FormField>
      </div>
      <FormField label={t('common:colName')} htmlFor="r-name">
        <Input id="r-name" value={name} onChange={(ev) => setName(ev.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="r-description">
        <Textarea id="r-description" value={description} onChange={(ev) => setDescription(ev.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || noTypes || (!editing && !token.trim()) || !typeToken}
        >
          {editing ? t('common:saveChanges') : e('CreateAction')}
        </Button>
      </div>
    </div>
  );
}
