// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Generic registry forms. Every registry "type" entity is a token/name/description
// record, and every "instance" entity is that plus a required reference to its
// type — so a single RegistryTypeForm / RegistryInstanceForm serves all the
// device/asset/customer/area families. A resource adapts the normalized form
// values to its own typed create/update request in its config (see resource.tsx).

import { useState } from 'react';
import { normalizeToken } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { useQuery } from '@/lib/hooks/use-query';
import { Textarea, errMessage } from '@/routes/common';

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
  singular,
  entityType,
  tokenPlaceholder,
  checkAvailability,
  create,
  update,
  onDone,
}: {
  entity?: T;
  singular: string; // "device type"
  /** Mask key for token generation (ADR-042 P3); defaults to the kebab singular. */
  entityType?: string;
  tokenPlaceholder?: string;
  checkAvailability?: (token: string) => Promise<boolean>;
  create: (req: TypeRequest) => Promise<unknown>;
  update: (token: string, req: TypeRequest) => Promise<unknown>;
  onDone: (message: string) => void;
}) {
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
        onDone(`${cap(singular)} “${entity.token}” updated`);
      } else {
        const t = token.trim();
        await create({ token: t, ...fields });
        onDone(`${cap(singular)} “${t}” created`);
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
        label="Token"
        htmlFor="r-token"
        description={editing ? `The ${singular} id; it cannot change.` : undefined}
      >
        {editing ? (
          <Input id="r-token" value={token} disabled />
        ) : (
          <TokenField
            id="r-token"
            entityType={entityType ?? normalizeToken(singular)}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={tokenPlaceholder}
            checkAvailability={checkAvailability}
          />
        )}
      </FormField>
      <FormField label="Name" htmlFor="r-name">
        <Input id="r-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="r-description">
        <Textarea id="r-description" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : `Create ${singular}`}
        </Button>
      </div>
    </div>
  );
}

// ── Instance form (device, asset, …) ─────────────────────────────────────

export function RegistryInstanceForm<T extends NamedEntity>({
  entity,
  singular,
  entityType,
  typeLabel,
  typeSingular,
  defaultTypeToken,
  tokenPlaceholder,
  checkAvailability,
  loadTypes,
  create,
  update,
  onDone,
}: {
  entity?: T;
  singular: string; // "asset"
  /** Mask key for token generation (ADR-042 P3); defaults to the kebab singular. */
  entityType?: string;
  typeLabel: string; // "Asset type"
  typeSingular: string; // "asset type" — used in the "create one first" hint
  defaultTypeToken?: string;
  tokenPlaceholder?: string;
  checkAvailability?: (token: string) => Promise<boolean>;
  loadTypes: () => Promise<NamedEntity[]>;
  create: (req: InstanceRequest) => Promise<unknown>;
  update: (token: string, req: InstanceRequest) => Promise<unknown>;
  onDone: (message: string) => void;
}) {
  const editing = entity != null;
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [typeToken, setTypeToken] = useState(defaultTypeToken ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const { data: types } = useQuery(loadTypes, []);
  const options: ComboboxOption[] = (types ?? []).map((t) => ({
    value: t.token,
    label: t.name || t.token,
    description: t.name ? t.token : undefined,
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
        onDone(`${cap(singular)} “${entity.token}” updated`);
      } else {
        const t = token.trim();
        await create({ token: t, ...fields });
        onDone(`${cap(singular)} “${t}” created`);
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
          label={typeLabel}
          htmlFor="r-type"
          description={noTypes ? `Create a ${typeSingular} first.` : undefined}
        >
          <Combobox
            id="r-type"
            value={typeToken}
            onChange={setTypeToken}
            options={options}
            placeholder={`Select a ${typeSingular}…`}
            disabled={noTypes}
          />
        </FormField>
        <FormField
          label="Token"
          htmlFor="r-token"
          description={editing ? `The ${singular} id; it cannot change.` : undefined}
        >
          {editing ? (
            <Input id="r-token" value={token} disabled />
          ) : (
            <TokenField
              id="r-token"
              entityType={entityType ?? normalizeToken(singular)}
              value={token}
              onChange={setToken}
              seed={name}
              placeholder={tokenPlaceholder}
              checkAvailability={checkAvailability}
            />
          )}
        </FormField>
      </div>
      <FormField label="Name" htmlFor="r-name">
        <Input id="r-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="r-description">
        <Textarea id="r-description" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button
          onClick={submit}
          loading={busy}
          disabled={busy || noTypes || (!editing && !token.trim()) || !typeToken}
        >
          {editing ? 'Save changes' : `Create ${singular}`}
        </Button>
      </div>
    </div>
  );
}
