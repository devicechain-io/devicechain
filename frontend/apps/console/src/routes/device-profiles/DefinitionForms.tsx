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
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { Combobox } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage } from '@/routes/common';
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

// Optional trimmed string → undefined when empty (so the request omits it).
const opt = (s: string): string | undefined => (s.trim() === '' ? undefined : s.trim());
// Optional numeric text → number | undefined.
const optNum = (s: string): number | undefined => (s.trim() === '' ? undefined : Number(s));

function SubmitRow({
  editing,
  singular,
  busy,
  disabled,
  onClick,
}: {
  editing: boolean;
  singular: string;
  busy: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <div className="flex gap-2 pt-1">
      <Button onClick={onClick} loading={busy} disabled={disabled}>
        {editing ? 'Save changes' : `Create ${singular}`}
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
      const request: MetricDefinitionCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        metricKey: metricKey.trim(),
        name: opt(name),
        description: opt(description),
        dataType,
        unit: opt(unit),
        minValue: optNum(minValue),
        maxValue: optNum(maxValue),
        // Carry forward fields this form doesn't edit (full-replace update).
        enum: entity?.enum ?? undefined,
        descriptor: entity?.descriptor ?? undefined,
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateMetricDefinition(entity.token, request);
        onDone(`Metric “${request.metricKey}” updated`);
      } else {
        await createMetricDefinition(request);
        onDone(`Metric “${request.metricKey}” created`);
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
      <FormField label="Metric key" htmlFor="m-key" description="The key measurements carry, e.g. temperature.">
        <Input id="m-key" value={metricKey} onChange={(e) => setMetricKey(e.target.value)} placeholder="temperature" />
      </FormField>
      <FormField label="Token" htmlFor="m-token" description={editing ? 'The id; it cannot change.' : undefined}>
        {editing ? (
          <Input id="m-token" value={token} disabled />
        ) : (
          <TokenField id="m-token" entityType="metric-definition" value={token} onChange={setToken} seed={metricKey} placeholder="metric-temperature" />
        )}
      </FormField>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <FormField label="Data type" htmlFor="m-type">
          <Combobox id="m-type" value={dataType} onChange={setDataType} options={METRIC_DATA_TYPES} allowClear={false} />
        </FormField>
        <FormField label="Unit" htmlFor="m-unit" description="UCUM, e.g. Cel">
          <Input id="m-unit" value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="Cel" />
        </FormField>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Min" htmlFor="m-min">
            <Input id="m-min" type="number" value={minValue} onChange={(e) => setMinValue(e.target.value)} />
          </FormField>
          <FormField label="Max" htmlFor="m-max">
            <Input id="m-max" type="number" value={maxValue} onChange={(e) => setMaxValue(e.target.value)} />
          </FormField>
        </div>
      </div>
      <FormField label="Name" htmlFor="m-name">
        <Input id="m-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="m-desc">
        <Textarea id="m-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <SubmitRow
        editing={editing}
        singular="metric"
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
      const request: CommandDefinitionCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        commandKey: commandKey.trim(),
        name: opt(name),
        description: opt(description),
        parameterSchema: opt(parameterSchema),
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateCommandDefinition(entity.token, request);
        onDone(`Command “${request.commandKey}” updated`);
      } else {
        await createCommandDefinition(request);
        onDone(`Command “${request.commandKey}” created`);
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
      <FormField label="Command key" htmlFor="c-key" description="The command a device accepts, e.g. set_point.">
        <Input id="c-key" value={commandKey} onChange={(e) => setCommandKey(e.target.value)} placeholder="set_point" />
      </FormField>
      <FormField label="Token" htmlFor="c-token" description={editing ? 'The id; it cannot change.' : undefined}>
        {editing ? (
          <Input id="c-token" value={token} disabled />
        ) : (
          <TokenField id="c-token" entityType="command-definition" value={token} onChange={setToken} seed={commandKey} placeholder="command-set-point" />
        )}
      </FormField>
      <FormField label="Name" htmlFor="c-name">
        <Input id="c-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="c-desc">
        <Textarea id="c-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <FormField
        label="Parameter schema"
        htmlFor="c-schema"
        description='Optional JSON array of parameters, each { "name", "dataType", … }. Leave blank for a no-argument command.'
      >
        <Textarea
          id="c-schema"
          value={parameterSchema}
          onChange={(e) => setParameterSchema(e.target.value)}
          placeholder='[{"name":"level","dataType":"INT","required":true}]'
          className="min-h-28"
        />
      </FormField>
      <SubmitRow
        editing={editing}
        singular="command"
        busy={busy}
        disabled={busy || !commandKey.trim() || (!editing && !token.trim())}
        onClick={submit}
      />
    </div>
  );
}
