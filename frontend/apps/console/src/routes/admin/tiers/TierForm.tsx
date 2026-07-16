// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { useQuery } from '@/lib/hooks/use-query';
import {
  createTenantTier,
  updateTenantTier,
  listGovernanceDimensions,
  type AdminTenantTierDetail,
} from '@/lib/api/admin';
import { Textarea, errMessage } from '@/routes/common';
import { parseTierConfig, buildTierConfigPatch } from '@/routes/admin/tiers/tierConfig';

// TierForm creates a tier (tier absent) or edits one (tier present, token fixed).
//
// The settings fields are rendered from the platform's OWN dimension vocabulary
// rather than a list kept here, so this editor offers exactly the keys the server's
// registry accepts (ADR-065 decision 8) — it cannot misspell one, and a fourth
// governance dimension becomes sellable the day it is declared instead of the day
// someone remembers to add it to this file.
export function TierForm({
  tier,
  onDone,
}: {
  tier?: AdminTenantTierDetail;
  onDone: (message: string) => void;
}) {
  const editing = tier != null;
  const { data: dimensions, error: dimensionsError } = useQuery(listGovernanceDimensions, []);

  const [token, setToken] = useState(tier?.token ?? '');
  const [name, setName] = useState(tier?.name ?? '');
  const [description, setDescription] = useState(tier?.description ?? '');
  // Settings are held as raw strings keyed by config key, so an empty field stays
  // distinguishable from a zero — "this tier declares no ceiling here" and "this
  // tier's ceiling is zero" are opposite facts, and the second is not writable at
  // all (a zero ceiling admits nothing, so the server rejects it).
  const [settings, setSettings] = useState<Record<string, string>>(() => parseTierConfig(tier?.config));
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const setSetting = (key: string, value: string) =>
    setSettings((prev) => ({ ...prev, [key]: value }));

  // Whether the settings editor actually RENDERED. Load-bearing, not cosmetic: the
  // fields are built from a query, and until it lands there are no fields — so the
  // form cannot claim to know what this tier's settings should be.
  const settingsEditorReady = dimensions != null && dimensions.length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // What to send as config — or UNDEFINED to leave the tier's settings alone.
      // The decision lives in buildTierConfigPatch, which is where its reasoning and
      // its tests are: sending "{}" built from an editor that never rendered would
      // clear the tier and re-price every tenant at it.
      const config = buildTierConfigPatch(dimensions, settings, tier?.config);
      if (editing) {
        await updateTenantTier(tier.token, {
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          config,
        });
        onDone(`Tier “${tier.token}” updated`);
      } else {
        await createTenantTier({
          token: token.trim(),
          name: name.trim() || undefined,
          description: description.trim() || undefined,
          config,
        });
        onDone(`Tier “${token.trim()}” created`);
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
        htmlFor="tier-token"
        description={editing ? 'The tier id used across the platform; it cannot change.' : undefined}
      >
        {editing ? (
          <Input id="tier-token" value={token} disabled />
        ) : (
          <TokenField
            id="tier-token"
            entityType="tenant tier"
            value={token}
            onChange={setToken}
            seed={name}
            placeholder="platinum"
          />
        )}
      </FormField>
      <FormField label="Name" htmlFor="tier-name">
        <Input id="tier-name" value={name} placeholder="Platinum" onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField
        label="Description"
        htmlFor="tier-description"
        description="What this tier is sold as. Operator-facing only."
      >
        <Textarea
          id="tier-description"
          value={description}
          placeholder="Premium packaging: the highest ceilings and the broadest set of AI models."
          onChange={(e) => setDescription(e.target.value)}
        />
      </FormField>

      <div className="space-y-3">
        <div>
          <h3 className="text-sm font-medium">Ceilings</h3>
          <p className="text-sm text-muted-foreground">
            The defaults every tenant at this tier is held to. Leave a field blank for the platform
            default — which is a real limit, never unlimited. An individual tenant can still be given
            an override, which is recorded as an exception to this tier.
          </p>
        </div>
        {dimensionsError ? (
          // Say plainly that settings are untouched, not just that something failed.
          // An operator who saves a rename here needs to know the ceilings survived —
          // otherwise the safe behavior looks identical to a silent wipe.
          <p className="text-sm text-destructive">
            Settings unavailable ({dimensionsError}). This tier’s existing ceilings are left
            unchanged; name and description can still be saved.
          </p>
        ) : !settingsEditorReady ? (
          <p className="text-sm text-muted-foreground">Loading settings…</p>
        ) : (
          <div className="grid grid-cols-2 gap-2">
            {(dimensions ?? []).map((d) => (
              <FormFieldPair
                key={d.name}
                label={d.label}
                unit={d.rateUnit}
                rateField={d.rateField}
                burstField={d.burstField}
                rate={settings[d.rateField] ?? ''}
                burst={settings[d.burstField] ?? ''}
                onRate={(v) => setSetting(d.rateField, v)}
                onBurst={(v) => setSetting(d.burstField, v)}
              />
            ))}
          </div>
        )}
      </div>

      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create tier'}
        </Button>
      </div>
    </div>
  );
}

// FormFieldPair renders one dimension's rate + burst inputs. The labels come from
// the dimension itself, so a new one reads correctly without being named here.
function FormFieldPair({
  label,
  unit,
  rateField,
  burstField,
  rate,
  burst,
  onRate,
  onBurst,
}: {
  label: string;
  unit: string;
  rateField: string;
  burstField: string;
  rate: string;
  burst: string;
  onRate: (v: string) => void;
  onBurst: (v: string) => void;
}) {
  return (
    <>
      <FormField label={`${label} rate (${unit})`} htmlFor={`tier-${rateField}`}>
        <Input
          id={`tier-${rateField}`}
          type="number"
          min="0"
          value={rate}
          placeholder="platform default"
          onChange={(e) => onRate(e.target.value)}
        />
      </FormField>
      <FormField label={`${label} burst`} htmlFor={`tier-${burstField}`}>
        <Input
          id={`tier-${burstField}`}
          type="number"
          min="0"
          step="1"
          value={burst}
          placeholder="platform default"
          onChange={(e) => onBurst(e.target.value)}
        />
      </FormField>
    </>
  );
}
