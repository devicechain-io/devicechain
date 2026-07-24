// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device-profile registry resource (ADR-045). A profile is the reusable
// capability contract a device type adopts: it owns the metric and command
// definitions, versioned as one unit. The Basic tab edits the profile header; the
// Metrics / Commands / Detection Rules tabs author its draft definitions; the Versions
// tab publishes/rolls back what devices actually resolve. Alarm authoring moved off the
// profile-owned AlarmDefinition to the DETECT DetectionRule path (ADR-057); the
// Detection Rules tab is that path's console form (slice 7a).

import { useState } from 'react';
import { normalizeToken } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { SuggestField } from '@/components/ui/suggest-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage, typeCountLabel, StatusBadge } from '@/routes/common';
import {
  tokenColumn,
  descriptionColumn,
  createdColumn,
  type RegistryResource,
} from '@/components/registry';
import { DefinitionsPanel } from './DefinitionsPanel';
import { VersionsPanel } from './VersionsPanel';
import { MetricDefinitionForm, CommandDefinitionForm } from './DefinitionForms';
import { DetectionRuleAuthoring } from './DetectionRuleAuthoring';
import { RuleHealthPanel } from './RuleHealthPanel';
import {
  listDeviceProfiles,
  getDeviceProfile,
  createDeviceProfile,
  updateDeviceProfile,
  deleteDeviceProfile,
  listMetricDefinitions,
  deleteMetricDefinition,
  listCommandDefinitions,
  deleteCommandDefinition,
  listDetectionRules,
  deleteDetectionRule,
  type DeviceProfile,
  type DeviceProfileCreateRequest,
} from '@/lib/api/device-management';

// The rule type is inside the opaque definition JSON; read it defensively for the
// list column (event-processing owns the taxonomy — the console only labels it).
const ruleTypeLabel = (definition: string): string => {
  try {
    const t = (JSON.parse(definition) as { type?: unknown }).type;
    return typeof t === 'string' ? t : '—';
  } catch {
    return '—';
  }
};

const Dash = () => <span className="text-muted-foreground">—</span>;

// Basic-tab form: the profile header (token, name, description, category). Category
// is the free-text capability facet (ADR-045 decision 8), suggesting the values
// already in use via SuggestField. Metadata is carried forward untouched on edit.
function ProfileForm({ entity, onDone }: { entity?: DeviceProfile; onDone: (message: string) => void }) {
  const editing = entity != null;
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? '');
  const [category, setCategory] = useState(entity?.category ?? '');
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const request: DeviceProfileCreateRequest = {
        token: editing ? entity.token : token.trim(),
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        category: category.trim() || undefined,
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateDeviceProfile(entity.token, request);
        onDone(`Device profile “${entity.token}” updated`);
      } else {
        await createDeviceProfile(request);
        onDone(`Device profile “${request.token}” created`);
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
      <FormField label="Token" htmlFor="p-token" description={editing ? 'The profile id; it cannot change.' : undefined}>
        {editing ? (
          <Input id="p-token" value={token} disabled />
        ) : (
          <TokenField
            id="p-token"
            entityType={normalizeToken('device profile')}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder="thermostat-v2"
          />
        )}
      </FormField>
      <FormField label="Name" htmlFor="p-name">
        <Input id="p-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField
        label="Category"
        htmlFor="p-category"
        description="Functional device class, e.g. thermostat, meter, gateway. Suggests categories already in use; you can type a new one."
      >
        <SuggestField id="p-category" facet="CATEGORY" value={category} onChange={setCategory} placeholder="thermostat" />
      </FormField>
      <FormField label="Description" htmlFor="p-description">
        <Textarea id="p-description" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? 'Save changes' : 'Create device profile'}
        </Button>
      </div>
    </div>
  );
}

export const deviceProfileResource: RegistryResource<DeviceProfile> = {
  basePath: '/device-profiles',
  i18nKey: 'deviceProfile',
  banner: 'devices',
  list: listDeviceProfiles,
  load: getDeviceProfile,
  remove: deleteDeviceProfile,
  idOf: (p) => p.id,
  tokenOf: (p) => p.token,
  nameOf: (p) => p.name ?? null,
  columns: [
    tokenColumn<DeviceProfile>(),
    { header: 'entities:deviceProfileColCategory', cell: (p) => p.category || <Dash /> },
    {
      header: 'entities:deviceProfileColUsedBy',
      cell: (p) => (
        <span className={p.deviceTypeCount === 0 ? 'text-muted-foreground' : 'tabular-nums'}>
          {typeCountLabel(p.deviceTypeCount)}
        </span>
      ),
    },
    descriptionColumn<DeviceProfile>(),
    createdColumn<DeviceProfile>(),
  ],
  renderForm: (p, onDone) => <ProfileForm entity={p} onDone={onDone} />,
  detailTabs: [
    {
      value: 'metrics',
      label: 'entities:deviceProfileMetricsTab',
      render: (p) => (
        <DefinitionsPanel
          profileToken={p.token}
          singular="metric"
          description="The typed, unit-bearing measurements a device reports."
          load={listMetricDefinitions}
          remove={deleteMetricDefinition}
          removeConfirm={(d) => `Delete metric “${d.metricKey}”?`}
          columns={[
            { header: 'Key', cell: (d) => <span className="font-medium">{d.metricKey}</span> },
            { header: 'Type', cell: (d) => d.dataType },
            { header: 'Unit', cell: (d) => d.unit || <Dash /> },
            {
              header: 'Range',
              cell: (d) =>
                d.minValue == null && d.maxValue == null ? (
                  <Dash />
                ) : (
                  <span className="tabular-nums">
                    {d.minValue ?? '−∞'} … {d.maxValue ?? '∞'}
                  </span>
                ),
            },
          ]}
          renderForm={(e, onDone) => <MetricDefinitionForm profileToken={p.token} entity={e} onDone={onDone} />}
        />
      ),
    },
    {
      value: 'commands',
      label: 'entities:deviceProfileCommandsTab',
      render: (p) => (
        <DefinitionsPanel
          profileToken={p.token}
          singular="command"
          description="The commands a device accepts, with their optional parameter schema."
          load={listCommandDefinitions}
          remove={deleteCommandDefinition}
          removeConfirm={(d) => `Delete command “${d.commandKey}”?`}
          columns={[
            { header: 'Key', cell: (d) => <span className="font-medium">{d.commandKey}</span> },
            { header: 'Name', cell: (d) => d.name || <Dash /> },
            { header: 'Parameters', cell: (d) => (d.parameterSchema ? 'declared' : <Dash />) },
          ]}
          renderForm={(e, onDone) => <CommandDefinitionForm profileToken={p.token} entity={e} onDone={onDone} />}
        />
      ),
    },
    {
      value: 'detection-rules',
      label: 'entities:deviceProfileDetectionRulesTab',
      render: (p) => (
        <DefinitionsPanel
          profileToken={p.token}
          singular="detection rule"
          description="Rules that raise alarms and send commands off this profile's telemetry. Author them step by step in the Form, or visually on the Canvas — both compile to the same rule."
          load={listDetectionRules}
          remove={deleteDetectionRule}
          removeConfirm={(d) => `Delete detection rule “${d.token}”?`}
          drawerClassName="sm:max-w-4xl"
          columns={[
            { header: 'Name', cell: (d) => <span className="font-medium">{d.name || d.token}</span> },
            { header: 'Type', cell: (d) => ruleTypeLabel(d.definition) },
            { header: 'Enabled', cell: (d) => <StatusBadge enabled={d.enabled} /> },
          ]}
          renderForm={(e, onDone) => <DetectionRuleAuthoring profileToken={p.token} entity={e} onDone={onDone} />}
        />
      ),
    },
    {
      value: 'rule-health',
      label: 'entities:deviceProfileRuleHealthTab',
      // Key by token so switching between two profile detail URLs (which React Router does
      // not remount on a param-only change) resets the live feed + stream status instead of
      // leaking profile A's firings under profile B (Fable 7c MEDIUM 1).
      render: (p) => <RuleHealthPanel key={p.token} profileToken={p.token} />,
    },
    {
      value: 'versions',
      label: 'entities:deviceProfileVersionsTab',
      render: (p, reload) => (
        <VersionsPanel
          profileToken={p.token}
          activeVersion={p.activeVersion ?? null}
          deviceTypeCount={p.deviceTypeCount}
          onChanged={reload}
        />
      ),
    },
  ],
};
