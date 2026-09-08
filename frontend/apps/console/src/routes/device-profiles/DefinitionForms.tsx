// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Create/edit forms for the device-profile definition kinds (ADR-045 slice d):
// metric (ADR-016) and command (ADR-043). Each binds its owning profile by token and
// adapts its fields to the typed create/update request. Updates are full-replace, so
// fields a form does not surface (enum/descriptor, parameter schema, metadata) are
// carried forward from the edited entity rather than nulled — the data-loss trap
// ADR-045 slice a hit. Alarm authoring is no longer a profile definition form: it
// moved to the DETECT DetectionRule path (ADR-057), whose console form is slice 7.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { Combobox } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { errMessage } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';
import { METRIC_DATA_TYPES } from '@/lib/vocab';
import {
  createMetricDefinition,
  updateMetricDefinition,
  createCommandDefinition,
  updateCommandDefinition,
  type MetricDefinition,
  type CommandDefinition,
  type MetricDefinitionCreateRequest,
  type CommandDefinitionCreateRequest,
} from '@/lib/api/device-management';
import { optNum, optText } from '@/lib/utils';

// Shared with the other forms that feed a full-replace request — see lib/utils.ts for
// why they return null rather than undefined. `optNum` there also rejects a
// non-numeric entry, which the copy that used to live here did not: typing "abc" into
// a metric bound sent NaN to the API.
const opt = optText;

function SubmitRow({
  editing,
  createLabel,
  busy,
  disabled,
  onClick,
}: {
  editing: boolean;
  /** Already-translated "Create <noun>" label for the create case. */
  createLabel: string;
  busy: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  const { t } = useTranslation('common');
  return (
    <div className="flex gap-2 pt-1">
      <Button onClick={onClick} loading={busy} disabled={disabled}>
        {editing ? t('saveChanges') : createLabel}
      </Button>
    </div>
  );
}

// ── Metric definition ─────────────────────────────────────────────────────

export function MetricDefinitionForm({
  profileToken,
  entity,
  onDone,
}: {
  profileToken: string;
  entity?: MetricDefinition;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['deviceProfiles', 'common']);
  const editing = entity != null;
  const [metricKey, setMetricKey] = useState(entity?.metricKey ?? '');
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [dataType, setDataType] = useState(entity?.dataType ?? 'DOUBLE');
  const [unit, setUnit] = useState(entity?.unit ?? '');
  const [minValue, setMinValue] = useState(entity?.minValue?.toString() ?? '');
  const [maxValue, setMaxValue] = useState(entity?.maxValue?.toString() ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // 🔴 THE EDIT NAMES ONLY THE FIELDS THIS FORM SHOWS. enum, descriptor and
      // metadata used to be carried forward from the loaded entity because the update
      // was a full replace and leaving one out deleted it; the update is partial now,
      // so an unnamed field is left alone and re-sending a stale copy is the only way
      // left to lose one.
      const edited = {
        deviceProfileToken: profileToken,
        metricKey: metricKey.trim(),
        name: opt(name),
        description: opt(description),
        dataType,
        unit: opt(unit),
        minValue: optNum(minValue),
        maxValue: optNum(maxValue),
      };
      if (editing) {
        await updateMetricDefinition(entity.token, edited);
        onDone(t('deviceProfiles:defMetricUpdatedToast', { key: edited.metricKey }));
      } else {
        // A create has nothing to leave alone, so it names everything.
        const created: Required<MetricDefinitionCreateRequest> = {
          token: token.trim(),
          ...edited,
          enum: null,
          descriptor: null,
          metadata: null,
        };
        await createMetricDefinition(created);
        onDone(t('deviceProfiles:defMetricCreatedToast', { key: created.metricKey }));
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
      <FormField label={t('deviceProfiles:defMetricKeyLabel')} htmlFor="m-key" description={t('deviceProfiles:defMetricKeyHint')}>
        <Input
          id="m-key"
          value={metricKey}
          onChange={(e) => setMetricKey(e.target.value)}
          placeholder={t('deviceProfiles:defMetricKeyPlaceholder')}
        />
      </FormField>
      <FormField label={t('common:colToken')} htmlFor="m-token" description={editing ? t('deviceProfiles:defTokenLockedHint') : undefined}>
        {editing ? (
          <Input id="m-token" value={token} disabled />
        ) : (
          <TokenField
            id="m-token"
            entityType="metric-definition"
            value={token}
            onChange={setToken}
            seed={metricKey}
            placeholder={t('deviceProfiles:defMetricTokenPlaceholder')}
          />
        )}
      </FormField>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <FormField label={t('deviceProfiles:defDataTypeLabel')} htmlFor="m-type">
          <Combobox id="m-type" value={dataType} onChange={setDataType} options={METRIC_DATA_TYPES} allowClear={false} />
        </FormField>
        <FormField label={t('deviceProfiles:defColUnit')} htmlFor="m-unit" description={t('deviceProfiles:defUnitHint')}>
          <Input id="m-unit" value={unit} onChange={(e) => setUnit(e.target.value)} placeholder={t('deviceProfiles:defUnitPlaceholder')} />
        </FormField>
        <div className="grid grid-cols-2 gap-2">
          <FormField label={t('deviceProfiles:defMinLabel')} htmlFor="m-min">
            <Input id="m-min" type="number" value={minValue} onChange={(e) => setMinValue(e.target.value)} />
          </FormField>
          <FormField label={t('deviceProfiles:defMaxLabel')} htmlFor="m-max">
            <Input id="m-max" type="number" value={maxValue} onChange={(e) => setMaxValue(e.target.value)} />
          </FormField>
        </div>
      </div>
      <FormField label={t('common:colName')} htmlFor="m-name">
        <Input id="m-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="m-desc">
        <Textarea id="m-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <SubmitRow
        editing={editing}
        createLabel={t('deviceProfiles:defMetricCreateButton')}
        busy={busy}
        disabled={busy || !metricKey.trim() || (!editing && !token.trim())}
        onClick={submit}
      />
    </div>
  );
}

// ── Command definition ────────────────────────────────────────────────────

export function CommandDefinitionForm({
  profileToken,
  entity,
  onDone,
}: {
  profileToken: string;
  entity?: CommandDefinition;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation(['deviceProfiles', 'common']);
  const editing = entity != null;
  const [commandKey, setCommandKey] = useState(entity?.commandKey ?? '');
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [parameterSchema, setParameterSchema] = useState(entity?.parameterSchema ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      // The edit names only what this form shows; metadata is left alone rather than
      // carried forward. See the metric form above for why that reversed.
      const edited = {
        deviceProfileToken: profileToken,
        commandKey: commandKey.trim(),
        name: opt(name),
        description: opt(description),
        parameterSchema: opt(parameterSchema),
      };
      if (editing) {
        await updateCommandDefinition(entity.token, edited);
        onDone(t('deviceProfiles:defCommandUpdatedToast', { key: edited.commandKey }));
      } else {
        const created: Required<CommandDefinitionCreateRequest> = {
          token: token.trim(),
          ...edited,
          metadata: null,
        };
        await createCommandDefinition(created);
        onDone(t('deviceProfiles:defCommandCreatedToast', { key: created.commandKey }));
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
      <FormField label={t('deviceProfiles:defCommandKeyLabel')} htmlFor="c-key" description={t('deviceProfiles:defCommandKeyHint')}>
        <Input
          id="c-key"
          value={commandKey}
          onChange={(e) => setCommandKey(e.target.value)}
          placeholder={t('deviceProfiles:defCommandKeyPlaceholder')}
        />
      </FormField>
      <FormField label={t('common:colToken')} htmlFor="c-token" description={editing ? t('deviceProfiles:defTokenLockedHint') : undefined}>
        {editing ? (
          <Input id="c-token" value={token} disabled />
        ) : (
          <TokenField
            id="c-token"
            entityType="command-definition"
            value={token}
            onChange={setToken}
            seed={commandKey}
            placeholder={t('deviceProfiles:defCommandTokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="c-name">
        <Input id="c-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="c-desc">
        <Textarea id="c-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <FormField
        label={t('deviceProfiles:defParamSchemaLabel')}
        htmlFor="c-schema"
        description={t('deviceProfiles:defParamSchemaHint')}
      >
        <Textarea
          id="c-schema"
          value={parameterSchema}
          onChange={(e) => setParameterSchema(e.target.value)}
          placeholder={t('deviceProfiles:defParamSchemaPlaceholder')}
          className="min-h-28"
        />
      </FormField>
      <SubmitRow
        editing={editing}
        createLabel={t('deviceProfiles:defCommandCreateButton')}
        busy={busy}
        disabled={busy || !commandKey.trim() || (!editing && !token.trim())}
        onClick={submit}
      />
    </div>
  );
}
