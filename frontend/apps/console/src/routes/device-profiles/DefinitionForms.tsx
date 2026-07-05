// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Create/edit forms for the three device-profile definition kinds (ADR-045 slice
// d): metric (ADR-016), command (ADR-043), and alarm rule (ADR-041). Each binds its
// owning profile by token and adapts its fields to the typed create/update request.
// Updates are full-replace, so fields a form does not surface (enum/descriptor,
// parameter schema, duration/repeat tiers, metadata) are carried forward from the
// edited entity rather than nulled — the data-loss trap ADR-045 slice a hit.

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { Combobox } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage } from '@/routes/common';
import { useQuery } from '@/lib/hooks/use-query';
import { METRIC_DATA_TYPES, ALARM_OPERATORS, ALARM_SEVERITIES, ALARM_CONDITION_SIMPLE } from '@/lib/vocab';
import {
  createMetricDefinition,
  updateMetricDefinition,
  createCommandDefinition,
  updateCommandDefinition,
  createAlarmDefinition,
  updateAlarmDefinition,
  listMetricDefinitions,
  type MetricDefinition,
  type CommandDefinition,
  type AlarmDefinition,
  type MetricDefinitionCreateRequest,
  type CommandDefinitionCreateRequest,
  type AlarmDefinitionCreateRequest,
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
        description="Optional JSON array of parameters (ADR-043). Leave blank for a no-argument command."
      >
        <Textarea
          id="c-schema"
          value={parameterSchema}
          onChange={(e) => setParameterSchema(e.target.value)}
          placeholder='[{"key":"level","type":"INT"}]'
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

// ── Alarm rule ────────────────────────────────────────────────────────────

// The console authors SIMPLE static-threshold rules (the common case). Dynamic
// thresholds (thresholdAttr) and DURATION/REPEATING tiers are modeled but not
// offered here yet; on edit their values are carried forward untouched.
export function AlarmDefinitionForm({
  profileToken,
  entity,
  onDone,
}: {
  profileToken: string;
  entity?: AlarmDefinition;
  onDone: (message: string) => void;
}) {
  const editing = entity != null;
  const [alarmKey, setAlarmKey] = useState(entity?.alarmKey ?? '');
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [metricKey, setMetricKey] = useState(entity?.metricKey ?? '');
  const [operator, setOperator] = useState(entity?.operator ?? 'GT');
  const [severity, setSeverity] = useState(entity?.severity ?? 'CRITICAL');
  const [threshold, setThreshold] = useState(entity?.threshold?.toString() ?? '');
  const [enabled, setEnabled] = useState(entity?.enabled ?? true);
  const [description, setDescription] = useState(entity?.description ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Watched metric options come from the profile's declared metrics.
  const { data: metrics } = useQuery(() => listMetricDefinitions(profileToken), [profileToken]);
  const metricOptions = (metrics ?? []).map((m) => ({ value: m.metricKey, label: m.metricKey, description: m.name ?? undefined }));
  const noMetrics = metrics != null && metricOptions.length === 0;

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const request: AlarmDefinitionCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        alarmKey: alarmKey.trim(),
        metricKey: metricKey.trim(),
        name: opt(name),
        description: opt(description),
        conditionType: ALARM_CONDITION_SIMPLE,
        operator,
        severity,
        threshold: optNum(threshold),
        enabled,
        // Carry forward the advanced tiers this form doesn't edit.
        thresholdAttr: entity?.thresholdAttr ?? undefined,
        durationSeconds: entity?.durationSeconds ?? undefined,
        repeatCount: entity?.repeatCount ?? undefined,
        repeatWindowSeconds: entity?.repeatWindowSeconds ?? undefined,
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateAlarmDefinition(entity.token, request);
        onDone(`Alarm rule “${request.alarmKey}” updated`);
      } else {
        await createAlarmDefinition(request);
        onDone(`Alarm rule “${request.alarmKey}” created`);
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
      <FormField label="Alarm key" htmlFor="a-key" description="Stable id for this alarm, e.g. overheat.">
        <Input id="a-key" value={alarmKey} onChange={(e) => setAlarmKey(e.target.value)} placeholder="overheat" />
      </FormField>
      <FormField label="Token" htmlFor="a-token" description={editing ? 'The id; it cannot change.' : undefined}>
        {editing ? (
          <Input id="a-token" value={token} disabled />
        ) : (
          <TokenField id="a-token" entityType="alarm-definition" value={token} onChange={setToken} seed={alarmKey} placeholder="alarm-overheat" />
        )}
      </FormField>
      <FormField
        label="Watched metric"
        htmlFor="a-metric"
        description={noMetrics ? 'Declare a metric on this profile first.' : 'The metric this rule evaluates.'}
      >
        <Combobox
          id="a-metric"
          value={metricKey}
          onChange={setMetricKey}
          options={metricOptions}
          placeholder="Select a metric…"
          disabled={noMetrics}
        />
      </FormField>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <FormField label="Operator" htmlFor="a-op">
          <Combobox id="a-op" value={operator} onChange={setOperator} options={ALARM_OPERATORS} allowClear={false} />
        </FormField>
        <FormField label="Threshold" htmlFor="a-thr">
          <Input id="a-thr" type="number" value={threshold} onChange={(e) => setThreshold(e.target.value)} placeholder="85" />
        </FormField>
        <FormField label="Severity" htmlFor="a-sev">
          <Combobox id="a-sev" value={severity} onChange={setSeverity} options={ALARM_SEVERITIES} allowClear={false} />
        </FormField>
      </div>
      <FormField label="Name" htmlFor="a-name">
        <Input id="a-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField label="Description" htmlFor="a-desc">
        <Textarea id="a-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-input"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Enabled (a disabled rule is kept but not evaluated)
      </label>
      <SubmitRow
        editing={editing}
        singular="alarm rule"
        busy={busy}
        disabled={busy || !alarmKey.trim() || !metricKey.trim() || threshold.trim() === '' || (!editing && !token.trim())}
        onClick={submit}
      />
    </div>
  );
}
