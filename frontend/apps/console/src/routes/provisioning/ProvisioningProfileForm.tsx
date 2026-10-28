// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The provisioning-profile create/edit form. Create returns the secret the server generated, once,
// and hands it straight to `onCreated` without keeping it; an edit is a PARTIAL update that names
// only the fields the operator changed (profileForm.ts).

import '@/routes/trust-anchors/catalog';
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
import { SegmentedControl } from '@/components/ui/segmented-control';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listDeviceTypes } from '@/lib/api/device-management';
import { listTrustAnchors } from '@/lib/api/trust-anchors';
import {
  PROVISIONING_STRATEGIES,
  createProvisioningProfile,
  getProvisioningProfile,
  updateProvisioningProfile,
  type IssuedProfileSecret,
  type ProvisioningProfile,
} from '@/lib/api/provisioning';
import {
  draftOf,
  emptyDraft,
  profileCreateRequest,
  profileEditRequest,
  profileProblems,
  type ProfileDraft,
} from './profileForm';
import { DERIVED_TOKEN_PREFIX } from '@/routes/trust-anchors/anchorRules';

export function ProvisioningProfileForm({
  entity,
  readOnly = false,
  onCreated,
  onSaved,
}: {
  entity?: ProvisioningProfile;
  /** Show the profile without offering to change it (a member without device:write). */
  readOnly?: boolean;
  /** Create only: the new profile and its one-time secret. The caller shows it and drops it. */
  onCreated?: (issued: IssuedProfileSecret) => void;
  /** Edit only. */
  onSaved?: (message: string) => void;
}) {
  const { t } = useTranslation(['provisioning', 'trustAnchors', 'common']);
  const editing = entity != null;
  // The draft the form was seeded from; an edit names only what differs from it.
  const initial = useMemo<ProfileDraft>(() => (entity ? draftOf(entity) : emptyDraft()), [entity]);
  const [token, setToken] = useState(entity?.token ?? '');
  const [draft, setDraft] = useState<ProfileDraft>(initial);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<ProfileDraft>) => setDraft((d) => ({ ...d, ...patch }));

  const { data: types } = useQuery(
    () => listDeviceTypes({ pageNumber: 1, pageSize: 1000 }).then((r) => r.results),
    [],
  );
  const typeOptions: ComboboxOption[] = (types ?? []).map((ty) => ({
    value: ty.token,
    label: ty.name || ty.token,
    description: ty.name ? ty.token : undefined,
  }));
  // A profile whose type is not in the first page still shows it.
  if (entity && !typeOptions.some((o) => o.value === entity.deviceType.token)) {
    typeOptions.push({ value: entity.deviceType.token, label: entity.deviceType.name || entity.deviceType.token });
  }

  // The anchors a certificate profile can name. Only enabled ones are offered (the server refuses
  // the rest), plus the one the profile already names so the field never reads as empty.
  const { data: anchors } = useQuery(
    () => (draft.certificate ? listTrustAnchors({ pageNumber: 1, pageSize: 100 }) : Promise.resolve(null)),
    [draft.certificate],
  );
  const anchorOptions: ComboboxOption[] = (anchors?.results ?? [])
    .filter((a) => a.enabled || a.token === initial.trustAnchorToken)
    .map((a) => ({ value: a.token, label: a.name || a.token, description: a.name ? a.token : undefined }));
  if (entity?.trustAnchor && !anchorOptions.some((o) => o.value === entity.trustAnchor!.token)) {
    anchorOptions.push({ value: entity.trustAnchor.token, label: entity.trustAnchor.name || entity.trustAnchor.token });
  }

  const problems = profileProblems(draft);
  const request = editing ? profileEditRequest(initial, draft) : null;
  const dirty = request !== null && Object.keys(request).length > 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      if (editing) {
        await updateProvisioningProfile(entity.token, request ?? {});
        onSaved?.(t('provisioning:updatedToast', { token: entity.token }));
      } else {
        const issued = await createProvisioningProfile(profileCreateRequest(token, draft));
        onCreated?.(issued);
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = readOnly || busy;

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {editing && !entity.secretIssued && !draft.certificate && (
        <p className="rounded-md border border-warning/40 bg-warning/5 p-3 text-sm text-foreground">
          {t('provisioning:rotateRequiredNotice')}
        </p>
      )}
      <FormField
        label={t('common:colToken')}
        htmlFor="pp-token"
        description={editing ? t('provisioning:tokenFixed') : undefined}
      >
        {editing ? (
          <Input id="pp-token" value={token} disabled />
        ) : (
          <TokenField
            id="pp-token"
            entityType="provisioning-profile"
            value={token}
            onChange={setToken}
            seed={draft.name}
            placeholder={t('provisioning:tokenPlaceholder')}
            checkAvailability={(tok) => getProvisioningProfile(tok).then((p) => p === null)}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="pp-name">
        <Input id="pp-name" value={draft.name} disabled={disabled} onChange={(e) => set({ name: e.target.value })} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="pp-description">
        <Textarea
          id="pp-description"
          value={draft.description}
          disabled={disabled}
          onChange={(e) => set({ description: e.target.value })}
        />
      </FormField>
      {!editing && (
        <FormField
          label={t('trustAnchors:authLabel')}
          htmlFor="pp-auth"
          description={t(draft.certificate ? 'trustAnchors:authHelp_CERTIFICATE' : 'trustAnchors:authHelp_SECRET')}
        >
          <SegmentedControl
            ariaLabel={t('trustAnchors:authLabel')}
            tone="joined"
            size="sm"
            fill
            value={draft.certificate ? 'CERTIFICATE' : 'SECRET'}
            onValueChange={(v) => set({ certificate: v === 'CERTIFICATE' })}
            options={[
              { value: 'SECRET', label: t('trustAnchors:auth_SECRET') },
              { value: 'CERTIFICATE', label: t('trustAnchors:auth_CERTIFICATE') },
            ]}
          />
        </FormField>
      )}
      {draft.certificate ? (
        <FormField
          label={t('trustAnchors:anchorLabel')}
          htmlFor="pp-anchor"
          description={t('trustAnchors:anchorHelp', { prefix: DERIVED_TOKEN_PREFIX })}
        >
          <Combobox
            id="pp-anchor"
            value={draft.trustAnchorToken}
            onChange={(v) => set({ trustAnchorToken: v })}
            options={anchorOptions}
            placeholder={t('trustAnchors:anchorSelect')}
            emptyMessage={t('trustAnchors:anchorEmpty')}
            disabled={disabled}
            allowClear={false}
          />
        </FormField>
      ) : (
        <FormField
          label={t('provisioning:keyLabel')}
          htmlFor="pp-key"
          description={t('provisioning:keyHelp')}
        >
          <Input
            id="pp-key"
            value={draft.provisionKey}
            disabled={disabled}
            onChange={(e) => set({ provisionKey: e.target.value })}
            className="font-mono text-xs"
            placeholder={t('provisioning:keyPlaceholder')}
            autoComplete="off"
          />
        </FormField>
      )}
      <FormField
        label={t('provisioning:typeLabel')}
        htmlFor="pp-type"
        description={t('provisioning:typeHelp')}
      >
        <Combobox
          id="pp-type"
          value={draft.deviceTypeToken}
          onChange={(v) => set({ deviceTypeToken: v })}
          options={typeOptions}
          placeholder={t('provisioning:typeSelect')}
          disabled={disabled}
          allowClear={false}
        />
      </FormField>
      <FormField
        label={t('provisioning:strategyLabel')}
        htmlFor="pp-strategy"
        description={t(`provisioning:strategyHelp_${draft.strategy}`, { defaultValue: '' })}
      >
        <SegmentedControl
          ariaLabel={t('provisioning:strategyLabel')}
          tone="joined"
          size="sm"
          fill
          value={draft.strategy}
          disabled={disabled}
          onValueChange={(v) => set({ strategy: v })}
          options={PROVISIONING_STRATEGIES.map((s) => ({ value: s, label: t(`provisioning:strategy_${s}`) }))}
        />
      </FormField>
      <FormField
        label={t('provisioning:maxDevicesLabel')}
        htmlFor="pp-max"
        description={
          problems.includes('cap') ? t('provisioning:maxDevicesInvalid') : t('provisioning:maxDevicesHelp')
        }
      >
        <Input
          id="pp-max"
          inputMode="numeric"
          value={draft.maxDevices}
          disabled={disabled}
          onChange={(e) => set({ maxDevices: e.target.value })}
          placeholder={t('provisioning:maxDevicesPlaceholder')}
        />
      </FormField>
      <FormField
        label={t('provisioning:expiresLabel')}
        htmlFor="pp-expires"
        description={t('provisioning:expiresHelp')}
      >
        <Input
          id="pp-expires"
          type="datetime-local"
          value={draft.expiresAt}
          disabled={disabled}
          onChange={(e) => set({ expiresAt: e.target.value })}
        />
      </FormField>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={draft.enabled}
          disabled={disabled}
          onCheckedChange={(c) => set({ enabled: c === true })}
          aria-label={t('provisioning:enabledLabel')}
        />
        <span>{t('provisioning:enabledLabel')}</span>
        <span className="text-muted-foreground">{t('provisioning:enabledHelp')}</span>
      </label>
      {!readOnly && (
        <div className="flex gap-2">
          <Button
            onClick={submit}
            loading={busy}
            disabled={busy || problems.length > 0 || (editing ? !dirty : !token.trim())}
          >
            {editing ? t('common:saveChanges') : t('provisioning:createAction')}
          </Button>
        </div>
      )}
    </div>
  );
}
