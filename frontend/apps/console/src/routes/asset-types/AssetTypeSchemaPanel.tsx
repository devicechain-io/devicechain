// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Properties tab of an asset type: the DRAFT property contract its assets carry.
//
// The contract is a JSON array of typed field descriptors, edited here as text, which
// is the same surface a command definition's parameter schema is authored through
// (DefinitionForms) and the same descriptor shape — one vocabulary, one editor idiom.
// Validation is the SERVER'S: it rejects a malformed document, an unrecognized
// constraint key, and a contract nothing could satisfy, and the message it returns is
// what an author reads. A client-side re-implementation of those rules would be a
// second opinion that is wrong the moment the server's changes.
//
// The draft binds nothing until it is published on the Versions tab. That is said in
// the panel rather than left to be discovered, because an author who edits here and
// then wonders why their assets still refuse the new property is asking exactly the
// question this line answers.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { FormField } from '@/components/ui/form-field';
import { useToast } from '@/components/ui/toast';
import { useAuth } from '@/auth/AuthProvider';
import { hasAuthority } from '@devicechain/client';
import { errMessage } from '@/routes/common';
import { updateAssetType, type AssetType } from '@/lib/api/assets';

// The placeholder is an example of the JSON shape, not user-facing prose, so it is
// hoisted out of the JSX tree to keep it clear of the i18n literal-string lint — the
// same reason CommandParameterForm hoists its numeric step token.
const SCHEMA_PLACEHOLDER = '[{"name": "vendor", "dataType": "STRING"}]';

export function AssetTypeSchemaPanel({
  assetType,
  onSaved,
}: {
  assetType: AssetType;
  onSaved: () => void;
}) {
  const { t } = useTranslation(['entities', 'common']);
  const { claims } = useAuth();
  const canWrite = hasAuthority(claims, 'device:write');
  const { toast } = useToast();
  const [schema, setSchema] = useState(assetType.propertySchema ?? '');
  const [busy, setBusy] = useState(false);

  // Re-seed when the tab is shown for a different type, or after a save reloads the
  // parent. Without this the textarea keeps the first type's draft.
  useEffect(() => {
    setSchema(assetType.propertySchema ?? '');
  }, [assetType.token, assetType.propertySchema]);

  const save = async () => {
    setBusy(true);
    try {
      // An empty textarea sends an explicit null, which WITHDRAWS the contract — a
      // different state from an empty array, which declares that assets of this type
      // carry nothing. The hint under the field says so, because the two look alike
      // and behave oppositely once published.
      const trimmed = schema.trim();
      await updateAssetType(assetType.token, { propertySchema: trimmed === '' ? null : trimmed });
      toast(t('entities:assetSchemaSavedToast'));
      onSaved();
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-3xl space-y-4">
      <p className="max-w-prose text-sm text-muted-foreground">
        {t('entities:assetSchemaExplain')}
      </p>
      {assetType.activeVersion == null ? (
        <p className="max-w-prose text-sm font-medium text-amber-600 dark:text-amber-500">
          {t('entities:assetSchemaNotPublished')}
        </p>
      ) : (
        <p className="max-w-prose text-sm text-muted-foreground">
          {t('entities:assetSchemaActiveHint', { version: assetType.activeVersion })}
        </p>
      )}
      <FormField
        label={t('entities:assetSchemaFieldLabel')}
        htmlFor="asset-property-schema"
        description={t('entities:assetSchemaFieldHint')}
      >
        <Textarea
          id="asset-property-schema"
          rows={14}
          className="font-mono text-xs"
          value={schema}
          disabled={!canWrite}
          onChange={(e) => setSchema(e.target.value)}
          placeholder={SCHEMA_PLACEHOLDER}
        />
      </FormField>
      {canWrite && (
        <Button onClick={save} loading={busy}>
          {t('common:save')}
        </Button>
      )}
    </div>
  );
}
