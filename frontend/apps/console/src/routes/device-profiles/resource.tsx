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
import { useTranslation } from 'react-i18next';
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
import { DefinitionsPanel, type DefinitionColumn } from './DefinitionsPanel';
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
  type MetricDefinition,
  type CommandDefinition,
  type DetectionRule,
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

// Extracted so it can legally call useTranslation: the `columns` config below is a
// module-level array whose `cell:` entries are plain callbacks invoked from inside
// ResourceDetailPage, not components React can attach hook state to (same reasoning
// as the detail-tab bodies further down this file).
function UsedByCount({ count }: { count: number }) {
  const { t } = useTranslation('devices');
  return (
    <span className={count === 0 ? 'text-muted-foreground' : 'tabular-nums'}>
      {typeCountLabel(count, t)}
    </span>
  );
}

// Basic-tab form: the profile header (token, name, description, category). Category
// is the free-text capability facet (ADR-045 decision 8), suggesting the values
// already in use via SuggestField. Metadata is carried forward untouched on edit.
function ProfileForm({ entity, onDone }: { entity?: DeviceProfile; onDone: (message: string) => void }) {
  const { t } = useTranslation(['deviceProfiles', 'common', 'entities']);
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
        onDone(t('deviceProfiles:profileUpdatedToast', { token: entity.token }));
      } else {
        await createDeviceProfile(request);
        onDone(t('deviceProfiles:profileCreatedToast', { token: request.token }));
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
        label={t('common:colToken')}
        htmlFor="p-token"
        description={editing ? t('deviceProfiles:profileTokenLockedHint') : undefined}
      >
        {editing ? (
          <Input id="p-token" value={token} disabled />
        ) : (
          <TokenField
            id="p-token"
            entityType={normalizeToken('device profile')}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('deviceProfiles:profileTokenPlaceholder')}
          />
        )}
      </FormField>
      <FormField label={t('common:colName')} htmlFor="p-name">
        <Input id="p-name" value={name} onChange={(e) => setName(e.target.value)} />
      </FormField>
      <FormField
        label={t('entities:deviceProfileColCategory')}
        htmlFor="p-category"
        description={t('deviceProfiles:profileCategoryHint')}
      >
        <SuggestField
          id="p-category"
          facet="CATEGORY"
          value={category}
          onChange={setCategory}
          placeholder={t('deviceProfiles:profileCategoryPlaceholder')}
        />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="p-description">
        <Textarea id="p-description" value={description} onChange={(e) => setDescription(e.target.value)} />
      </FormField>
      <div className="flex gap-2">
        <Button onClick={submit} loading={busy} disabled={busy || (!editing && !token.trim())}>
          {editing ? t('common:saveChanges') : t('deviceProfiles:profileCreateButton')}
        </Button>
      </div>
    </div>
  );
}

// Detail-tab bodies as proper components (not inline arrow-function `render`
// callbacks) so each can legally call useTranslation — `deviceProfileResource` is a
// module-level config object, and its `render:` entries are plain callbacks invoked
// from inside ResourceDetailPage, not components React can attach hook state to.
// Column `header` values are i18n keys (resolved inside DefinitionsPanel), just
// like the top-level registry `columns` config in this file. Declared as plain
// consts rather than inline in the JSX below so they're outside the JSX tree the
// (b)-sweep lint scans (mode: jsx-only) — matching how `@/components/registry`'s
// own tokenColumn()/descriptionColumn() are plain functions, not inline literals.
const metricColumns: DefinitionColumn<MetricDefinition>[] = [
  { header: 'deviceProfiles:defColKey', cell: (d) => <span className="font-medium">{d.metricKey}</span> },
  { header: 'common:colType', cell: (d) => d.dataType },
  { header: 'deviceProfiles:defColUnit', cell: (d) => d.unit || <Dash /> },
  {
    header: 'deviceProfiles:defColRange',
    cell: (d) =>
      d.minValue == null && d.maxValue == null ? (
        <Dash />
      ) : (
        <span className="tabular-nums">
          {d.minValue ?? '−∞'} … {d.maxValue ?? '∞'}
        </span>
      ),
  },
];

function MetricsTab({ profile }: { profile: DeviceProfile }) {
  const { t } = useTranslation('deviceProfiles');
  return (
    <DefinitionsPanel
      profileToken={profile.token}
      i18nKey="defMetric"
      description={t('defMetricDescription')}
      load={listMetricDefinitions}
      remove={deleteMetricDefinition}
      removeConfirm={(d) => t('defMetricRemoveConfirm', { key: d.metricKey })}
      columns={metricColumns}
      renderForm={(e, onDone) => <MetricDefinitionForm profileToken={profile.token} entity={e} onDone={onDone} />}
    />
  );
}

function commandColumns(t: (key: string) => string): DefinitionColumn<CommandDefinition>[] {
  return [
    { header: 'deviceProfiles:defColKey', cell: (d) => <span className="font-medium">{d.commandKey}</span> },
    { header: 'common:colName', cell: (d) => d.name || <Dash /> },
    { header: 'deviceProfiles:defColParameters', cell: (d) => (d.parameterSchema ? t('deviceProfiles:defParamSchemaDeclared') : <Dash />) },
  ];
}

function CommandsTab({ profile }: { profile: DeviceProfile }) {
  const { t } = useTranslation(['deviceProfiles']);
  return (
    <DefinitionsPanel
      profileToken={profile.token}
      i18nKey="defCommand"
      description={t('defCommandDescription')}
      load={listCommandDefinitions}
      remove={deleteCommandDefinition}
      removeConfirm={(d) => t('defCommandRemoveConfirm', { key: d.commandKey })}
      columns={commandColumns(t)}
      renderForm={(e, onDone) => <CommandDefinitionForm profileToken={profile.token} entity={e} onDone={onDone} />}
    />
  );
}

const detectionRuleColumns: DefinitionColumn<DetectionRule>[] = [
  { header: 'common:colName', cell: (d) => <span className="font-medium">{d.name || d.token}</span> },
  { header: 'common:colType', cell: (d) => ruleTypeLabel(d.definition) },
  { header: 'common:enabled', cell: (d) => <StatusBadge enabled={d.enabled} /> },
];

function DetectionRulesTab({ profile }: { profile: DeviceProfile }) {
  const { t } = useTranslation('deviceProfiles');
  return (
    <DefinitionsPanel
      profileToken={profile.token}
      i18nKey="defDetectionRule"
      description={t('defDetectionRuleDescription')}
      load={listDetectionRules}
      remove={deleteDetectionRule}
      removeConfirm={(d) => t('defDetectionRuleRemoveConfirm', { key: d.token })}
      className="sm:max-w-4xl"
      columns={detectionRuleColumns}
      renderForm={(e, onDone) => <DetectionRuleAuthoring profileToken={profile.token} entity={e} onDone={onDone} />}
    />
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
      cell: (p) => <UsedByCount count={p.deviceTypeCount} />,
    },
    descriptionColumn<DeviceProfile>(),
    createdColumn<DeviceProfile>(),
  ],
  renderForm: (p, onDone) => <ProfileForm entity={p} onDone={onDone} />,
  detailTabs: [
    {
      value: 'metrics',
      label: 'entities:deviceProfileMetricsTab',
      render: (p) => <MetricsTab profile={p} />,
    },
    {
      value: 'commands',
      label: 'entities:deviceProfileCommandsTab',
      render: (p) => <CommandsTab profile={p} />,
    },
    {
      value: 'detection-rules',
      label: 'entities:deviceProfileDetectionRulesTab',
      render: (p) => <DetectionRulesTab profile={p} />,
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
