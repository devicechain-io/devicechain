// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Properties tab of an asset: the values filling the contract its TYPE publishes.
//
// The form is rendered from the type's ACTIVE PUBLISHED contract, not from its draft,
// because that is the contract the server validates the save against — a form built
// from the draft would let an operator fill in a property the API is about to refuse.
//
// The parsing, per-field validation and JSON serialization come from
// @devicechain/widgets, the same helpers the command-button widget and the device
// command form use over the same descriptor shape. Three forms over one contract that
// each coerced values their own way would produce documents that differ by which
// screen an operator happened to use.
//
// TWO BEHAVIOURS TO KNOW, both inherited from those shared helpers and both stated
// because they are visible to an operator:
//
//   - A declared BOOLEAN is ALWAYS written, as true or false. Its checkbox has no
//     third state, so "absent" is not a value this form can express for one.
//   - A STRUCTURED (OBJECT) property cannot be filled in here. Rather than saving a
//     document with it silently dropped, the panel refuses to open as a form at all
//     and shows the stored document instead — a partial editor that looked complete
//     would delete data an author could not see it holding.

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { CommandParameter } from '@devicechain/dashboards';
import { parseParameterSchema, isScalar, validateParams, buildPayload } from '@devicechain/widgets';
import { hasAuthority } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage, useReload } from '@/routes/common';
import { CommandParameterForm } from '@/routes/devices/CommandParameterForm';
import { getActiveAssetTypeVersion, updateAsset, type Asset } from '@/lib/api/assets';

// storedValues projects an asset's stored property document onto the string-keyed
// value map the shared form edits. Values the document does not carry are left
// ABSENT rather than seeded from the contract's declared defaults: a default is an
// authoring hint the API deliberately does not materialize, and pre-filling one here
// would make the next save write a value the asset never had.
function storedValues(document: string | null | undefined): Record<string, string> {
  if (!document) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(document);
  } catch {
    return {};
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
    if (v === null || typeof v === 'object') continue;
    out[k] = typeof v === 'boolean' ? (v ? 'true' : 'false') : String(v);
  }
  return out;
}

export function AssetPropertiesPanel({
  asset,
  onSaved,
}: {
  asset: Asset;
  onSaved: () => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const [reloadKey] = useReload();
  const typeToken = asset.assetType?.token ?? '';

  const { data, loading, error } = useQuery(
    () => (typeToken ? getActiveAssetTypeVersion(typeToken) : Promise.resolve(null)),
    [typeToken, reloadKey],
  );

  const params = useMemo<CommandParameter[]>(
    () => parseParameterSchema(data?.propertySchema),
    [data?.propertySchema],
  );
  const structured = params.some((p) => !isScalar(p));

  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setValues(storedValues(asset.properties));
    setErrors({});
  }, [asset.token, asset.properties]);

  const save = async () => {
    const found = validateParams(params, values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      // buildPayload returns undefined when no property contributes a value, which is
      // exactly the "this asset carries none" state — sent as an explicit null so the
      // stored document is cleared rather than left behind.
      const payload = buildPayload(params, values);
      await updateAsset(asset.token, { properties: payload ?? null });
      toast(t('entities:assetPropertiesSavedToast'));
      onSaved();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  if (!typeToken) return <ErrorState description={t('entities:assetPropertiesNoType')} />;
  if (loading && !data) return <LoadingState description={t('common:loading')} />;
  if (error) return <ErrorState description={error} />;

  if (!data) {
    return (
      <p className="max-w-prose rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
        {t('entities:assetPropertiesNoContract')}
      </p>
    );
  }

  if (structured) {
    return (
      <div className="max-w-3xl space-y-3">
        <p className="max-w-prose text-sm font-medium text-amber-600 dark:text-amber-500">
          {t('entities:assetPropertiesStructured')}
        </p>
        <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs">
          {asset.properties ?? t('entities:assetPropertiesEmpty')}
        </pre>
      </div>
    );
  }

  if (params.length === 0) {
    return (
      <p className="max-w-prose rounded-md border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
        {t('entities:assetPropertiesContractDeclaresNone', { version: data.version })}
      </p>
    );
  }

  return (
    <div className="max-w-3xl space-y-4">
      <p className="max-w-prose text-sm text-muted-foreground">
        {t('entities:assetPropertiesExplain', { version: data.version })}
      </p>
      <CommandParameterForm
        params={params}
        values={values}
        errors={errors}
        disabled={!canWrite}
        onChange={(name, value) => setValues((v) => ({ ...v, [name]: value }))}
      />
      {canWrite && (
        <Button onClick={save} loading={busy}>
          {t('common:save')}
        </Button>
      )}
    </div>
  );
}
