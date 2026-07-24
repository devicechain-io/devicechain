// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The node inspector (ADR-053 slice 9b): the config-editing panel for the selected canvas
// node. Its fields are per-type projections of the same rules.Rule field groups the form
// builder edits (the canvas is the ceiling, the form the floor — both target one schema). It
// mutates the opaque node config; the server-authoritative compileCanvas is what validates it,
// so this panel is permissive and the diagnostics land on the node.

import { useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Textarea } from '@/routes/common';
import { useQuery } from '@/lib/hooks/use-query';
import { listConnectors } from '@/lib/api/connectors';
import {
  AGG_FUNCS,
  COMPARE_OPS,
  OP_SYMBOL,
  SEVERITIES,
  WINDOW_MODES,
  type NodeConfig,
  type NodeType,
} from './model';

// A minimal styled <select> for the fixed rules vocabularies (op/agg/mode/severity/action),
// which are closed enums — not the free-text Combobox the form uses for open facets.
function Select({
  value,
  onChange,
  children,
  id,
}: {
  value: string;
  onChange: (v: string) => void;
  children: ReactNode;
  id?: string;
}) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
    >
      {children}
    </select>
  );
}

const asRecord = (v: unknown): Record<string, unknown> => (v && typeof v === 'object' ? (v as Record<string, unknown>) : {});
const numVal = (v: unknown): number => (typeof v === 'number' ? v : 0);
const strVal = (v: unknown): string => (typeof v === 'string' ? v : '');

// DurationField edits a millisecond value as a number + unit, keeping the config in ms (the
// wire form) while showing a friendly unit.
function DurationField({ label, id, ms, onChange }: { label: string; id: string; ms: number; onChange: (ms: number) => void }) {
  const { t } = useTranslation('deviceProfiles');
  // Pick the coarsest exact unit for display.
  const unit = ms !== 0 && ms % 3_600_000 === 0 ? 'h' : ms !== 0 && ms % 60_000 === 0 ? 'm' : ms % 1000 === 0 ? 's' : 'ms';
  const factor = unit === 'h' ? 3_600_000 : unit === 'm' ? 60_000 : unit === 's' ? 1000 : 1;
  const shown = ms / factor;
  return (
    <FormField label={label} htmlFor={id}>
      <div className="flex gap-2">
        <Input
          id={id}
          type="number"
          min={0}
          value={Number.isFinite(shown) ? shown : 0}
          // Round to whole milliseconds — the Go config fields are int64, so a fractional ms
          // (e.g. 90.5 ms) would fail to decode and surface an opaque compile error.
          onChange={(e) => onChange(Math.round(Math.max(0, Number(e.target.value)) * factor))}
        />
        <Select value={unit} onChange={(u) => onChange(Math.round(shown * (u === 'h' ? 3_600_000 : u === 'm' ? 60_000 : u === 's' ? 1000 : 1)))}>
          <option value="ms">{t('inspectorUnitMs')}</option>
          <option value="s">{t('inspectorUnitSec')}</option>
          <option value="m">{t('inspectorUnitMin')}</option>
          <option value="h">{t('inspectorUnitHr')}</option>
        </Select>
      </div>
    </FormField>
  );
}

const opOptions = COMPARE_OPS.map((op) => (
  <option key={op} value={op}>
    {OP_SYMBOL[op]} ({op})
  </option>
));

// The engine-side aggregate/deltaRate comparison accepts only the four ORDERED operators — the
// compiler rejects eq/ne there (they are valid only in a predicate leaf, which lowers to CEL).
const orderedOpOptions = COMPARE_OPS.filter((op) => op !== 'eq' && op !== 'ne').map((op) => (
  <option key={op} value={op}>
    {OP_SYMBOL[op]} ({op})
  </option>
));

// LeafEditor edits a when-leaf: none (match every) / structured (metric·op·bound) / raw CEL.
// `allowNone` is false for threshold/duration where the comparison IS the rule.
function LeafEditor({ leaf, allowNone, onChange }: { leaf: unknown; allowNone: boolean; onChange: (leaf: unknown) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const l = asRecord(leaf);
  const bound = asRecord(l.threshold);
  const mode: 'none' | 'structured' | 'cel' = l.cel ? 'cel' : l.metric || l.op || l.threshold ? 'structured' : allowNone ? 'none' : 'structured';
  const boundKind: 'literal' | 'attribute' = bound.kind === 'attribute' ? 'attribute' : 'literal';

  const setMode = (m: string) => {
    if (m === 'none') onChange({});
    else if (m === 'cel') onChange({ cel: strVal(l.cel) });
    else onChange({ metric: strVal(l.metric), op: strVal(l.op) || 'gt', threshold: { kind: 'literal', value: numVal(bound.value) } });
  };

  return (
    <div className="space-y-3 rounded-md border border-dashed p-3">
      <FormField label={t('inspectorConditionLabel')} htmlFor="leaf-mode">
        <Select id="leaf-mode" value={mode} onChange={setMode}>
          {allowNone && <option value="none">{t('inspectorMatchEveryEvent')}</option>}
          <option value="structured">{t('inspectorComparison')}</option>
          <option value="cel">{t('inspectorAdvancedCel')}</option>
        </Select>
      </FormField>
      {mode === 'structured' && (
        <div className="grid grid-cols-[1fr_auto] gap-2">
          <FormField label={t('inspectorMetricLabel')} htmlFor="leaf-metric">
            <Input id="leaf-metric" value={strVal(l.metric)} onChange={(e) => onChange({ ...l, metric: e.target.value })} placeholder={t('inspectorMetricPlaceholder')} />
          </FormField>
          <FormField label={t('inspectorOpLabel')} htmlFor="leaf-op">
            <Select id="leaf-op" value={strVal(l.op) || 'gt'} onChange={(op) => onChange({ ...l, op })}>
              {opOptions}
            </Select>
          </FormField>
          <FormField label={t('inspectorBoundLabel')} htmlFor="leaf-boundkind">
            <Select
              id="leaf-boundkind"
              value={boundKind}
              // eslint-disable-next-line i18next/no-literal-string -- 'attribute'/'literal' are the Bound.kind discriminant, not user text.
              onChange={(k) => onChange({ ...l, threshold: k === 'attribute' ? { kind: 'attribute', attribute: strVal(bound.attribute) } : { kind: 'literal', value: numVal(bound.value) } })}
            >
              <option value="literal">{t('inspectorLiteral')}</option>
              <option value="attribute">{t('inspectorDeviceAttribute')}</option>
            </Select>
          </FormField>
          {boundKind === 'literal' ? (
            <FormField label={t('inspectorValueLabel')} htmlFor="leaf-value">
              {/* eslint-disable-next-line i18next/no-literal-string -- 'literal' is the Bound.kind discriminant, not user text. */}
              <Input id="leaf-value" type="number" value={numVal(bound.value)} onChange={(e) => onChange({ ...l, threshold: { kind: 'literal', value: Number(e.target.value) } })} />
            </FormField>
          ) : (
            <FormField label={t('inspectorAttributeLabel')} htmlFor="leaf-attr">
              {/* eslint-disable-next-line i18next/no-literal-string -- 'attribute' is the Bound.kind discriminant, not user text. */}
              <Input id="leaf-attr" value={strVal(bound.attribute)} onChange={(e) => onChange({ ...l, threshold: { kind: 'attribute', attribute: e.target.value } })} placeholder={t('inspectorAttributePlaceholder')} />
            </FormField>
          )}
        </div>
      )}
      {mode === 'cel' && (
        <FormField label={t('inspectorCelExpressionLabel')} htmlFor="leaf-cel" description={t('inspectorCelExpressionDescription')}>
          <Textarea id="leaf-cel" value={strVal(l.cel)} onChange={(e) => onChange({ cel: e.target.value })} placeholder={t('inspectorCelPlaceholderThreshold')} />
        </FormField>
      )}
    </div>
  );
}

// meta edits the shared rule identity (name/description/severity) carried on a condition node.
function MetaFields({ config, set }: { config: NodeConfig; set: (patch: NodeConfig) => void }) {
  const { t } = useTranslation('deviceProfiles');
  return (
    <>
      <FormField label={t('inspectorNameLabel')} htmlFor="cfg-name">
        <Input id="cfg-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value })} placeholder={t('inspectorNamePlaceholder')} />
      </FormField>
      <FormField label={t('inspectorSeverityLabel')} htmlFor="cfg-severity" description={t('inspectorSeverityDescription')}>
        <Select id="cfg-severity" value={strVal(config.severity)} onChange={(v) => set({ severity: v || undefined })}>
          <option value="">{t('inspectorSeverityNone')}</option>
          {SEVERITIES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
      </FormField>
    </>
  );
}

// NodeInspector renders the field set for a node type. onChange receives the FULL new config.
export function NodeInspector({ type, config, onChange }: { type: NodeType; config: NodeConfig; onChange: (config: NodeConfig) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const set = (patch: NodeConfig) => onChange({ ...config, ...patch });
  const setLeaf = (leaf: unknown) => onChange({ ...config, when: leaf });

  switch (type) {
    case 'source':
      return (
        <p className="text-sm text-muted-foreground">
          <Trans
            t={t}
            i18nKey="inspectorSourceScopedTo"
            values={{ token: strVal(asRecord(config.scope).profileToken) }}
            components={{ mono: <span className="font-mono" /> }}
          />
        </p>
      );
    case 'threshold':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <LeafEditor leaf={config.when} allowNone={false} onChange={setLeaf} />
        </div>
      );
    case 'duration':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <LeafEditor leaf={config.when} allowNone={false} onChange={setLeaf} />
          <DurationField label={t('inspectorSustainedFor')} id="cfg-hold" ms={numVal(config.holdMs)} onChange={(ms) => set({ holdMs: ms })} />
        </div>
      );
    case 'absence':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <DurationField label={t('inspectorSilentForLabel')} id="cfg-timeout" ms={numVal(config.timeoutMs)} onChange={(ms) => set({ timeoutMs: ms })} />
        </div>
      );
    case 'aggregate': {
      const mode = strVal(config.windowMode) || 'tumbling';
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('inspectorAggregateLabel')} htmlFor="cfg-agg">
              <Select
                id="cfg-agg"
                value={strVal(config.agg) || 'avg'}
                onChange={(v) => {
                  // A count aggregate folds no value, so drop a stale metric the compiler would
                  // reject ("a count aggregate takes no value metric").
                  const next: NodeConfig = { ...config, agg: v };
                  if (v === 'count') delete next.metric;
                  onChange(next);
                }}
              >
                {AGG_FUNCS.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </Select>
            </FormField>
            <FormField label={t('inspectorWindowLabel')} htmlFor="cfg-mode">
              <Select
                id="cfg-mode"
                value={mode}
                onChange={(v) => {
                  // Each mode uses exactly one of window/gap/count; drop the others so a stale
                  // field from a previous mode can't trip the compiler's fail-closed forbid()
                  // (and leave a diagnostic pinned to a field the mode no longer renders) — M2.
                  const { windowMs, gapMs, count, ...rest } = config;
                  const next: NodeConfig = { ...rest, windowMode: v };
                  if (v === 'tumbling' || v === 'sliding') next.windowMs = typeof windowMs === 'number' ? windowMs : 60000;
                  else if (v === 'session') next.gapMs = typeof gapMs === 'number' ? gapMs : 60000;
                  else if (v === 'count') next.count = typeof count === 'number' ? count : 3;
                  onChange(next);
                }}
              >
                {WINDOW_MODES.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </Select>
            </FormField>
          </div>
          {config.agg !== 'count' && (
            <FormField label={t('inspectorValueMetricLabel')} htmlFor="cfg-metric">
              <Input id="cfg-metric" value={strVal(config.metric)} onChange={(e) => set({ metric: e.target.value })} placeholder={t('inspectorMetricPlaceholder')} />
            </FormField>
          )}
          {(mode === 'tumbling' || mode === 'sliding') && (
            <DurationField label={t('inspectorWindowLabel')} id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          )}
          {mode === 'session' && <DurationField label={t('inspectorSessionGapLabel')} id="cfg-gap" ms={numVal(config.gapMs)} onChange={(ms) => set({ gapMs: ms })} />}
          {mode === 'count' && (
            <FormField label={t('inspectorWindowSizeEventsLabel')} htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
          )}
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('inspectorOpLabel')} htmlFor="cfg-op">
              <Select id="cfg-op" value={strVal(config.op) || 'gt'} onChange={(v) => set({ op: v })}>
                {orderedOpOptions}
              </Select>
            </FormField>
            <FormField label={t('inspectorThresholdLabel')} htmlFor="cfg-threshold">
              <Input id="cfg-threshold" type="number" value={numVal(config.threshold)} onChange={(e) => set({ threshold: Number(e.target.value) })} />
            </FormField>
          </div>
        </div>
      );
    }
    case 'deltaRate':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <FormField label={t('inspectorValueMetricLabel')} htmlFor="cfg-metric">
            <Input id="cfg-metric" value={strVal(config.metric)} onChange={(e) => set({ metric: e.target.value })} placeholder={t('inspectorMetricPlaceholder')} />
          </FormField>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={!!config.rate} onChange={(e) => set({ rate: e.target.checked || undefined })} />
            {t('inspectorPerSecondRateLabel')}
          </label>
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('inspectorOpLabel')} htmlFor="cfg-op">
              <Select id="cfg-op" value={strVal(config.op) || 'gt'} onChange={(v) => set({ op: v })}>
                {orderedOpOptions}
              </Select>
            </FormField>
            <FormField label={t('inspectorThresholdLabel')} htmlFor="cfg-threshold">
              <Input id="cfg-threshold" type="number" value={numVal(config.threshold)} onChange={(e) => set({ threshold: Number(e.target.value) })} />
            </FormField>
          </div>
        </div>
      );
    case 'repeating':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <LeafEditor leaf={config.when} allowNone onChange={setLeaf} />
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('inspectorCountLabel')} htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
            <DurationField label={t('inspectorWithinLabel')} id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          </div>
        </div>
      );
    case 'correlation':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <FormField label={t('inspectorAnchorTypeLabel')} htmlFor="cfg-anchor" description={t('inspectorAnchorTypeDescription')}>
            <Input id="cfg-anchor" value={strVal(config.anchorType)} onChange={(e) => set({ anchorType: e.target.value })} placeholder={t('inspectorAnchorTypePlaceholder')} />
          </FormField>
          <div className="grid grid-cols-2 gap-2">
            <FormField label={t('inspectorDistinctDevicesLabel')} htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
            <DurationField label={t('inspectorWithinLabel')} id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          </div>
        </div>
      );
    case 'branch':
      return (
        <div className="space-y-4">
          <FormField label={t('inspectorBranchLabelLabel')} htmlFor="cfg-branch-name" description={t('inspectorBranchLabelDescription')}>
            <Input id="cfg-branch-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value || undefined })} placeholder={t('inspectorBranchNamePlaceholder')} />
          </FormField>
          <FormField
            label={t('inspectorOnlyWhenCelLabel')}
            htmlFor="cfg-branch-when"
            description={t('inspectorOnlyWhenCelDescription')}
          >
            <Textarea id="cfg-branch-when" value={strVal(config.when)} onChange={(e) => set({ when: e.target.value })} placeholder={t('inspectorBranchWhenPlaceholder')} />
          </FormField>
          <p className="rounded-md border border-dashed px-2 py-1.5 text-xs text-muted-foreground">
            <Trans
              t={t}
              i18nKey="inspectorBranchHelp"
              components={{
                gt100: <span className="font-mono" />,
                oneHundred: <span className="font-mono" />,
                valueWord: <span className="font-mono" />,
                hasValueWord: <span className="font-mono" />,
                hasValueExpr: <span className="font-mono" />,
              }}
            />
          </p>
        </div>
      );
    case 'action':
      return <ActionFields config={config} set={set} onChange={onChange} />;
    case 'compute':
      return (
        <div className="space-y-4">
          <FormField
            label={t('inspectorNameLabel')}
            htmlFor="cfg-compute-name"
            description={t('inspectorComputeNameDescription')}
          >
            <Input id="cfg-compute-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value })} placeholder={t('inspectorComputeNamePlaceholder')} />
          </FormField>
          <FormField label={t('inspectorComputeValueCelLabel')} htmlFor="cfg-compute-expr" description={t('inspectorComputeValueCelDescription')}>
            <Textarea id="cfg-compute-expr" value={strVal(config.expr)} onChange={(e) => set({ expr: e.target.value })} placeholder={t('inspectorComputeCelPlaceholder')} />
          </FormField>
          <p className="rounded-md border border-dashed px-2 py-1.5 text-xs text-muted-foreground">
            <Trans
              t={t}
              i18nKey="inspectorComputeHelp"
              components={{
                valuePort: <span className="font-mono" />,
                tempFExample: <span className="font-mono" />,
                mWord: <span className="font-mono" />,
                attrWord: <span className="font-mono" />,
                valueWord: <span className="font-mono" />,
                hasValueWord: <span className="font-mono" />,
                seriesWord: <span className="font-mono" />,
                celWord: <span className="font-mono" />,
              }}
            />
          </p>
        </div>
      );
  }
}

// ── REACT action node fields (raiseAlarm / sendCommand / httpCall / publish) ──
// A component (not an inline case) so the publish connector picker can fetch the
// tenant's connectors. Switching the action kind resets the config to just
// { action } — the compiler reads only the selected variant's fields, and a reset
// keeps a stale url/body from lingering in the stored graph when you change kind.
function ActionFields({
  config,
  set,
  onChange,
}: {
  config: NodeConfig;
  set: (patch: NodeConfig) => void;
  onChange: (config: NodeConfig) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const kind = strVal(config.action) || 'raiseAlarm';
  return (
    <div className="space-y-4">
      <FormField label={t('inspectorActionLabel')} htmlFor="cfg-action">
        <Select id="cfg-action" value={kind} onChange={(v) => onChange({ action: v })}>
          <option value="raiseAlarm">{t('inspectorActionRaiseAlarm')}</option>
          <option value="sendCommand">{t('inspectorActionSendCommand')}</option>
          <option value="httpCall">{t('inspectorActionHttpCall')}</option>
          <option value="publish">{t('inspectorActionPublish')}</option>
        </Select>
      </FormField>

      {kind === 'raiseAlarm' && (
        <FormField label={t('inspectorAlarmKeyLabel')} htmlFor="cfg-alarmkey" description={t('inspectorAlarmKeyDescription')}>
          <Input id="cfg-alarmkey" value={strVal(config.alarmKey)} onChange={(e) => set({ alarmKey: e.target.value || undefined })} placeholder={t('inspectorAlarmKeyPlaceholder')} />
        </FormField>
      )}

      {kind === 'sendCommand' && (
        <>
          <FormField label={t('inspectorCommandLabel')} htmlFor="cfg-command">
            <Input id="cfg-command" value={strVal(config.command)} onChange={(e) => set({ command: e.target.value })} placeholder={t('inspectorCommandPlaceholder')} />
          </FormField>
          <FormField label={t('inspectorPayloadJsonLabel')} htmlFor="cfg-payload" description={t('inspectorPayloadJsonDescription')}>
            <Textarea id="cfg-payload" value={strVal(config.payload)} onChange={(e) => set({ payload: e.target.value || undefined })} placeholder={t('inspectorPayloadJsonPlaceholder')} />
          </FormField>
        </>
      )}

      {kind === 'httpCall' && (
        <>
          <FormField label={t('inspectorUrlLabel')} htmlFor="cfg-url" description={t('inspectorUrlDescription')}>
            <Input id="cfg-url" value={strVal(config.url)} onChange={(e) => set({ url: e.target.value })} placeholder={t('inspectorUrlPlaceholder')} />
          </FormField>
          <FormField label={t('inspectorBodyCelLabel')} htmlFor="cfg-body" description={t('inspectorBodyCelDescription')}>
            <Textarea id="cfg-body" value={strVal(config.bodyTemplate)} onChange={(e) => set({ bodyTemplate: e.target.value || undefined })} placeholder={t('inspectorBodyCelPlaceholder')} />
          </FormField>
          <FormField label={t('inspectorHeadersLabel')} htmlFor="cfg-headers" description={t('inspectorHeadersDescription')}>
            <HeadersField value={config.headers} onChange={(h) => set({ headers: h })} />
          </FormField>
          <FormField label={t('inspectorSecretHandleLabel')} htmlFor="cfg-secretref" description={t('inspectorSecretHandleDescription')}>
            <Input id="cfg-secretref" value={strVal(config.secretRef)} onChange={(e) => set({ secretRef: e.target.value || undefined })} placeholder={t('inspectorSecretHandlePlaceholder')} />
          </FormField>
        </>
      )}

      {kind === 'publish' && (
        <>
          <FormField label={t('inspectorConnectorLabel')} htmlFor="cfg-connectorref" description={t('inspectorConnectorDescription')}>
            <ConnectorPicker value={strVal(config.connectorRef)} onChange={(v) => set({ connectorRef: v || undefined })} />
          </FormField>
          <FormField label={t('inspectorPayloadCelLabel')} htmlFor="cfg-payloadtemplate" description={t('inspectorPayloadCelDescription')}>
            <Textarea id="cfg-payloadtemplate" value={strVal(config.payloadTemplate)} onChange={(e) => set({ payloadTemplate: e.target.value || undefined })} placeholder={t('inspectorPayloadCelPlaceholder')} />
          </FormField>
        </>
      )}
    </div>
  );
}

// ConnectorPicker lists the tenant's connectors for the publish action. Only a
// published connector actually dispatches, but the list is all connectors (the
// publish-time existence check is server-side); an unknown/stale current value is
// still shown so it isn't silently dropped.
function ConnectorPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const { data, loading } = useQuery(() => listConnectors({ pageNumber: 1, pageSize: 100 }), []);
  const connectors = data?.results ?? [];
  const known = connectors.some((c) => c.token === value);
  // Only show the "no connectors" hint when there's also nothing selected — a stale/deleted
  // ref (value set, list empty) must still render the Select so the author can see and clear it.
  if (!loading && connectors.length === 0 && !value) {
    return (
      <p className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
        <Trans t={t} i18nKey="inspectorNoConnectorsHint" components={{ link: <Link to="/connectors" className="text-primary hover:underline" /> }} />
      </p>
    );
  }
  return (
    <Select id="cfg-connectorref" value={value} onChange={onChange}>
      <option value="">{loading ? t('common:loading') : t('inspectorSelectConnectorPlaceholder')}</option>
      {/* Show a "(not found)" row for a value not in the list — but only once loaded, so a valid
          ref doesn't flash as not-found while the query is in flight. */}
      {!loading && value && !known && <option value={value}>{t('inspectorConnectorNotFound', { token: value })}</option>}
      {connectors.map((c) => (
        <option key={c.token} value={c.token}>
          {c.name ? `${c.name} (${c.token})` : c.token}
        </option>
      ))}
    </Select>
  );
}

// HeadersField edits the httpCall static headers. It keeps the RAW text in local state so typing
// works normally: a controlled textarea re-derived through parseHeaders on every keystroke would
// erase an in-progress colon-less line (you could never type a header). The parsed map is written
// to the node config on each change; the textarea shows the local text. It is remounted per node
// (NodeInspector is keyed by node id), so the seed is always the selected node's headers.
function HeadersField({ value, onChange }: { value: unknown; onChange: (h: Record<string, string> | undefined) => void }) {
  const { t } = useTranslation('deviceProfiles');
  const [text, setText] = useState(() => formatHeaders(value));
  return (
    <Textarea
      id="cfg-headers"
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parseHeaders(e.target.value));
      }}
      placeholder={t('inspectorHeadersPlaceholder')}
    />
  );
}

// parseHeaders turns a "Key: Value" per line textarea into a header map (or undefined
// when empty). formatHeaders is the inverse for display.
function parseHeaders(raw: string): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const line of raw.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf(':');
    if (i < 0) continue;
    const k = t.slice(0, i).trim();
    if (k) out[k] = t.slice(i + 1).trim();
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function formatHeaders(h: unknown): string {
  if (!h || typeof h !== 'object') return '';
  return Object.entries(h as Record<string, string>)
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n');
}
