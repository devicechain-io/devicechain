// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Generic registry forms. Every registry "type" entity is a token/name/description
// record, and every "instance" entity is that plus a required reference to its
// type — so a single RegistryTypeForm / RegistryInstanceForm serves all the
// device/asset/customer/area families. A resource adapts the normalized form
// values to its own typed create/update request in its config (see resource.tsx).
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
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { Textarea, errMessage } from '@/routes/common';

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

export interface TypeRequest {
  token: string;
  name?: string;
  description?: string;
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
  entityType: string;
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
      const fields = { name: name.trim() || undefined, description: description.trim() || undefined };
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
  entityType: string;
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
        name: name.trim() || undefined,
        description: description.trim() || undefined,
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
