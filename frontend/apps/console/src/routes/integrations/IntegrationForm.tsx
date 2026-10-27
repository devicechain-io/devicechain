// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The integration create/edit form. Create returns the bearer token the server generated, once,
// and hands it straight to `onCreated` without keeping it; an edit is a PARTIAL update that names
// only the fields the operator changed (integrationForm.ts). The type is fixed at creation.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Checkbox } from '@/components/ui/checkbox';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { MultiSelect } from '@/components/ui/multi-select';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listDeviceTypes } from '@/lib/api/device-management';
import {
  INTEGRATION_TYPES,
  createIntegration,
  getIntegration,
  updateIntegration,
  type Integration,
  type IntegrationType,
  type IssuedIntegrationToken,
} from '@/lib/api/integrations';
import {
  FRAME_ENCODINGS,
  draftOf,
  emptyDraft,
  identityChoices,
  integrationCreateRequest,
  integrationEditRequest,
  integrationProblems,
  isGeneric,
  withType,
  type IntegrationDraft,
} from './integrationDraft';

const POINTER_EXAMPLE = { items: '/uplinks', time: '/time', frame: '/frame' };

export function IntegrationForm({
  entity,
  readOnly = false,
  onCreated,
  onSaved,
}: {
  entity?: Integration;
  /** Show the integration without offering to change it (a member without device:write). */
  readOnly?: boolean;
  /** Create only: the new integration and its one-time token. The caller shows it and drops it. */
  onCreated?: (issued: IssuedIntegrationToken) => void;
  /** Edit only. */
  onSaved?: (message: string) => void;
}) {
  const { t } = useTranslation(['integrations', 'common']);
  const editing = entity != null;
  // The draft the form was seeded from; an edit names only what differs from it.
  const initial = useMemo<IntegrationDraft>(() => (entity ? draftOf(entity) : emptyDraft()), [entity]);
  const [token, setToken] = useState(entity?.token ?? '');
  const [draft, setDraft] = useState<IntegrationDraft>(initial);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<IntegrationDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const { data: types } = useQuery(
    () => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results),
    [],
  );
  const typeOptions: ComboboxOption[] = (types ?? []).map((ty) => ({
    value: ty.token,
    label: ty.name || ty.token,
    description: ty.name ? ty.token : undefined,
  }));
  // A scope entry that is not in the first page still shows.
  for (const dt of entity?.deviceTypes ?? []) {
    if (!typeOptions.some((o) => o.value === dt.token)) {
      typeOptions.push({ value: dt.token, label: dt.name || dt.token });
    }
  }

  const problems = integrationProblems(draft);
  const request = editing ? integrationEditRequest(initial, draft) : null;
  const dirty = request !== null && Object.keys(request).length > 0;
  const generic = isGeneric(draft.type);
  const identities = identityChoices(draft.type);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateIntegration(entity.token, request ?? {});
        onSaved?.(t('integrations:updatedToast', { token: entity.token }));
      } else {
        onCreated?.(await createIntegration(integrationCreateRequest(token, draft)));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = readOnly || busy;
  const typeSelectOptions: ComboboxOption[] = INTEGRATION_TYPES.map((ty) => ({
    value: ty,
    label: t(`integrations:type_${ty}`),
  }));

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      <FormField
        label={t('integrations:typeLabel')}
        htmlFor="ig-type"
        description={editing ? t('integrations:typeFixed') : t(`integrations:typeHelp_${draft.type}`)}
      >
        <Combobox
          id="ig-type"
          value={draft.type}
          onChange={(v) => setDraft((d) => withType(d, v as IntegrationType))}
          options={typeSelectOptions}
          disabled={editing || disabled}
          allowClear={false}
        />
      </FormField>
      <FormField
        label={t('common:colToken')}
        htmlFor="ig-token"
        description={editing ? t('integrations:tokenFixed') : t('integrations:tokenHelp')}
      >
        {editing ? (
          <Input id="ig-token" value={token} disabled />
        ) : (
          <TokenField
            id="ig-token"
            entityType="integration"
            value={token}
            onChange={setToken}
            seed={draft.name}
            placeholder={t('integrations:tokenPlaceholder')}
            checkAvailability={(tok) => getIntegration(tok).then((i) => i === null)}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="ig-name">
        <Input id="ig-name" value={draft.name} disabled={disabled} onChange={(e) => set({ name: e.target.value })} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="ig-description">
        <Textarea
          id="ig-description"
          value={draft.description}
          disabled={disabled}
          onChange={(e) => set({ description: e.target.value })}
        />
      </FormField>
      <FormField
        label={t('integrations:scopeLabel')}
        htmlFor="ig-scope"
        description={problems.includes('scope') ? t('integrations:scopeRequired') : t('integrations:scopeHelp')}
      >
        <MultiSelect
          id="ig-scope"
          value={draft.deviceTypeTokens}
          onChange={(v) => set({ deviceTypeTokens: v })}
          options={typeOptions}
          placeholder={t('integrations:scopePlaceholder')}
          disabled={disabled}
        />
      </FormField>
      {identities.length > 1 && (
        <FormField label={t('integrations:identityLabel')} htmlFor="ig-identity" description={t('integrations:identityHelp')}>
          <SegmentedControl
            ariaLabel={t('integrations:identityLabel')}
            tone="joined"
            size="sm"
            fill
            value={draft.identity}
            disabled={disabled}
            onValueChange={(v) => set({ identity: v })}
            options={identities.map((i) => ({ value: i, label: t(`integrations:identity_${i}`) }))}
          />
        </FormField>
      )}
      {generic && (
        <GenericSettings draft={draft} set={set} problems={problems} disabled={disabled} />
      )}
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={draft.linkMetrics}
          disabled={disabled}
          onCheckedChange={(c) => set({ linkMetrics: c === true })}
          aria-label={t('integrations:linkMetricsLabel')}
        />
        <span>{t('integrations:linkMetricsLabel')}</span>
        <span className="text-muted-foreground">{t('integrations:linkMetricsHelp')}</span>
      </label>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={draft.enabled}
          disabled={disabled}
          onCheckedChange={(c) => set({ enabled: c === true })}
          aria-label={t('integrations:enabledLabel')}
        />
        <span>{t('integrations:enabledLabel')}</span>
        <span className="text-muted-foreground">{t('integrations:enabledHelp')}</span>
      </label>
      {!readOnly && (
        <div className="flex gap-2">
          <Button
            onClick={submit}
            loading={busy}
            disabled={busy || problems.length > 0 || (editing ? !dirty : !token.trim())}
          >
            {editing ? t('common:saveChanges') : t('integrations:createAction')}
          </Button>
        </div>
      )}
    </div>
  );
}

/** The settings a generic source is described by: where its device identifier is, and the optional JSON shape hints. */
function GenericSettings({
  draft,
  set,
  problems,
  disabled,
}: {
  draft: IntegrationDraft;
  set: (patch: Partial<IntegrationDraft>) => void;
  problems: string[];
  disabled: boolean;
}) {
  const { t } = useTranslation('integrations');
  const binary = draft.type === 'GENERIC_BINARY';
  const idKinds = binary ? (['header', 'query'] as const) : (['pointer', 'header', 'query'] as const);
  const bad = (field: string) => problems.includes(field);
  const text = {
    aliasName: bad('aliasName') ? t('aliasNameInvalid') : t('aliasNameHelp'),
    idValue: bad('idValue') ? t('idValueInvalid') : t(`idValueHelp_${draft.idKind}`),
    itemsPointer: bad('itemsPointer') ? t('itemsPointerInvalid') : t('itemsPointerHelp'),
    timePointer: bad('timePointer') ? t('timePointerInvalid') : t('timePointerHelp'),
    framePointer: bad('framePointer') ? t('framePointerInvalid') : t('framePointerHelp'),
    frameEncoding: bad('frameEncoding') ? t('frameEncodingInvalid') : t('frameEncodingHelp'),
  };
  return (
    <div className="space-y-4 rounded-md border p-4" data-shot="integration-generic-settings">
      <p className="text-sm font-medium text-foreground">{t('genericTitle')}</p>
      <FormField label={t('aliasNameLabel')} htmlFor="ig-alias-name" description={text.aliasName}>
        <Input
          id="ig-alias-name"
          value={draft.aliasName}
          disabled={disabled}
          onChange={(e) => set({ aliasName: e.target.value })}
          placeholder={t('aliasNamePlaceholder')}
          className="font-mono text-xs"
          autoComplete="off"
        />
      </FormField>
      <FormField label={t('idKindLabel')} htmlFor="ig-id-kind" description={t(`idKindHelp_${binary ? 'binary' : 'json'}`)}>
        <SegmentedControl
          ariaLabel={t('idKindLabel')}
          tone="joined"
          size="sm"
          fill
          value={draft.idKind}
          disabled={disabled}
          onValueChange={(v) => set({ idKind: v })}
          options={idKinds.map((k) => ({ value: k, label: t(`idKind_${k}`) }))}
        />
      </FormField>
      <FormField label={t('idValueLabel')} htmlFor="ig-id-value" description={text.idValue}>
        <Input
          id="ig-id-value"
          value={draft.idValue}
          disabled={disabled}
          onChange={(e) => set({ idValue: e.target.value })}
          placeholder={t(`idValuePlaceholder_${draft.idKind}`)}
          className="font-mono text-xs"
          autoComplete="off"
        />
      </FormField>
      {!binary && (
        <>
          <FormField label={t('itemsPointerLabel')} htmlFor="ig-items" description={text.itemsPointer}>
            <Input id="ig-items" value={draft.itemsPointer} disabled={disabled} onChange={(e) => set({ itemsPointer: e.target.value })} placeholder={POINTER_EXAMPLE.items} className="font-mono text-xs" />
          </FormField>
          <FormField label={t('timePointerLabel')} htmlFor="ig-time" description={text.timePointer}>
            <Input id="ig-time" value={draft.timePointer} disabled={disabled} onChange={(e) => set({ timePointer: e.target.value })} placeholder={POINTER_EXAMPLE.time} className="font-mono text-xs" />
          </FormField>
          <FormField label={t('framePointerLabel')} htmlFor="ig-frame" description={text.framePointer}>
            <Input id="ig-frame" value={draft.framePointer} disabled={disabled} onChange={(e) => set({ framePointer: e.target.value })} placeholder={POINTER_EXAMPLE.frame} className="font-mono text-xs" />
          </FormField>
          {draft.framePointer.trim() !== '' && (
            <FormField label={t('frameEncodingLabel')} htmlFor="ig-frame-encoding" description={text.frameEncoding}>
              <SegmentedControl
                ariaLabel={t('frameEncodingLabel')}
                tone="joined"
                size="sm"
                fill
                value={draft.frameEncoding}
                disabled={disabled}
                onValueChange={(v) => set({ frameEncoding: v })}
                options={FRAME_ENCODINGS.map((e) => ({ value: e, label: e }))}
              />
            </FormField>
          )}
        </>
      )}
    </div>
  );
}
