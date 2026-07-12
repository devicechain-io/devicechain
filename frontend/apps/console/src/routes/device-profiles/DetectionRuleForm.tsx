// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The detection-rule authoring form (ADR-051 / ADR-057, slice 7a): the single
// alarm-authoring path since the 6d cutover retired the AlarmDefinition form. It is a
// type-picker → typed sub-form whose fields map 1:1 onto the flat event-processing
// `rules.Rule` schema; the user never writes CEL on the common path (an advanced raw-CEL
// leaf sits behind a disclosure). The form emits the opaque `rules.Rule` JSON as the
// rule's `definition`; device-management stores it whole, and event-processing performs
// the authoritative type/cost/injection validation when the profile is PUBLISHED (this
// draft save only checks JSON well-formedness) — so a mistyped rule that saves as a draft
// is rejected at publish with the compiler's message. Inline validation against the
// compiler is slice 7a-2.
//
// The per-type field set mirrors the event-processing compiler's per-type contract
// (internal/rules/compile.go): each rule type allows exactly the fields its lowering reads
// and FORBIDS the rest (fail-closed), so the form emits only the fields valid for the
// chosen type — a clean rebuild per type, never a merge that could leave a stale field the
// compiler then rejects.

import { useState } from 'react';
import { normalizeToken } from '@devicechain/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { Combobox, type ComboboxOption } from '@/components/ui/combobox';
import { ErrorBanner } from '@/components/ui/error-banner';
import { Textarea, errMessage } from '@/routes/common';
import {
  createDetectionRule,
  updateDetectionRule,
  type DetectionRule,
  type DetectionRuleCreateRequest,
} from '@/lib/api/device-management';

// ── The rule taxonomy (mirrors rules.RuleType) ─────────────────────────────

type RuleType = 'threshold' | 'duration' | 'absence' | 'repeating' | 'deltaRate' | 'aggregate' | 'correlation';

const RULE_TYPES: ComboboxOption[] = [
  { value: 'threshold', label: 'Threshold', description: 'Fires while a metric compares past a bound.' },
  { value: 'duration', label: 'Duration', description: 'Fires when a comparison stays true for a sustained time.' },
  { value: 'absence', label: 'Absence', description: 'Fires when a device goes silent for a window.' },
  { value: 'repeating', label: 'Repeating', description: 'Fires when N matching events fall inside a window.' },
  { value: 'deltaRate', label: 'Rate of change', description: 'Fires on the change between consecutive samples.' },
  { value: 'aggregate', label: 'Windowed aggregate', description: 'Fires when an aggregate over a window crosses a bound.' },
  { value: 'correlation', label: 'Area correlation', description: 'Fires when distinct devices report under one anchor.' },
];

const SEVERITIES: ComboboxOption[] = [
  { value: 'critical', label: 'Critical' },
  { value: 'major', label: 'Major' },
  { value: 'minor', label: 'Minor' },
  { value: 'warning', label: 'Warning' },
  { value: 'indeterminate', label: 'Indeterminate' },
];

// The structured leaf comparison allows eq/ne (it lowers to CEL, where equality is
// well-defined); the engine-side aggregate/delta comparison is ordered-only.
const ALL_OPS: ComboboxOption[] = [
  { value: 'gt', label: '> greater than' },
  { value: 'ge', label: '≥ at least' },
  { value: 'lt', label: '< less than' },
  { value: 'le', label: '≤ at most' },
  { value: 'eq', label: '= equal to' },
  { value: 'ne', label: '≠ not equal to' },
];
const ORDERED_OPS = ALL_OPS.filter((o) => o.value !== 'eq' && o.value !== 'ne');

const AGG_FUNCS: ComboboxOption[] = [
  { value: 'count', label: 'count' },
  { value: 'sum', label: 'sum' },
  { value: 'avg', label: 'average' },
  { value: 'min', label: 'minimum' },
  { value: 'max', label: 'maximum' },
];

const WINDOW_MODES: ComboboxOption[] = [
  { value: 'tumbling', label: 'Tumbling', description: 'Fixed, non-overlapping time windows.' },
  { value: 'sliding', label: 'Sliding', description: 'Trailing time window, re-evaluated per event.' },
  { value: 'session', label: 'Session', description: 'Closes after a gap of silence.' },
  { value: 'count', label: 'Count', description: 'A window of N events, not time.' },
];

// Condition editors: a required-leaf type offers structured|cel; an optional-leaf type
// also offers "match every event"; absence takes no leaf at all.
type CondMode = 'structured' | 'cel' | 'none';
type BoundKind = 'literal' | 'attr';
type ActionKind = 'raiseAlarm' | 'sendCommand';

interface ActionRow {
  type: ActionKind;
  alarmKey: string;
  command: string;
  payload: string;
}

// Per-type authoring shape derived from compile.go.
const conditionRequired = (t: RuleType) => t === 'threshold' || t === 'duration';
const conditionForbidden = (t: RuleType) => t === 'absence';
const actionsForbidden = (t: RuleType) => t === 'correlation'; // its series is an area anchor, not a device

// ── Small field helpers ────────────────────────────────────────────────────

function DurationField({
  id,
  label,
  value,
  onChange,
  placeholder,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <FormField label={label} htmlFor={id} description="A duration, e.g. 30s, 5m, 1h30m.">
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} />
    </FormField>
  );
}

// A structured/CEL/none condition editor bound to one leaf.
function ConditionEditor({
  ruleType,
  mode,
  setMode,
  metric,
  setMetric,
  op,
  setOp,
  boundKind,
  setBoundKind,
  threshold,
  setThreshold,
  thresholdAttr,
  setThresholdAttr,
  cel,
  setCel,
}: {
  ruleType: RuleType;
  mode: CondMode;
  setMode: (m: CondMode) => void;
  metric: string;
  setMetric: (v: string) => void;
  op: string;
  setOp: (v: string) => void;
  boundKind: BoundKind;
  setBoundKind: (v: BoundKind) => void;
  threshold: string;
  setThreshold: (v: string) => void;
  thresholdAttr: string;
  setThresholdAttr: (v: string) => void;
  cel: string;
  setCel: (v: string) => void;
}) {
  if (conditionForbidden(ruleType)) return null;

  const required = conditionRequired(ruleType);
  const modeOptions: ComboboxOption[] = [
    { value: 'structured', label: 'Structured comparison' },
    { value: 'cel', label: 'Advanced (CEL expression)' },
    ...(required ? [] : [{ value: 'none', label: 'Match every event' } as ComboboxOption]),
  ];

  return (
    <div className="space-y-3 rounded-md border p-3">
      <FormField
        label="Condition"
        htmlFor="dr-cond-mode"
        description={
          required
            ? 'The comparison this rule fires on.'
            : 'An optional per-event gate. “Match every event” lets the temporal shape carry the logic.'
        }
      >
        <Combobox
          id="dr-cond-mode"
          value={mode}
          onChange={(v) => setMode(v as CondMode)}
          options={modeOptions}
          allowClear={false}
        />
      </FormField>

      {mode === 'structured' && (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_auto]">
            <FormField label="Metric" htmlFor="dr-cond-metric" description="The metric key the event carries.">
              <Input
                id="dr-cond-metric"
                value={metric}
                onChange={(e) => setMetric(e.target.value)}
                placeholder="temperature"
              />
            </FormField>
            <FormField label="Operator" htmlFor="dr-cond-op">
              <Combobox id="dr-cond-op" value={op} onChange={setOp} options={ALL_OPS} allowClear={false} />
            </FormField>
          </div>
          <FormField label="Bound" htmlFor="dr-cond-bound-kind" description="A fixed value, or the device's own attribute.">
            <Combobox
              id="dr-cond-bound-kind"
              value={boundKind}
              onChange={(v) => setBoundKind(v as BoundKind)}
              options={[
                { value: 'literal', label: 'Literal value' },
                { value: 'attr', label: 'Device attribute (per-device threshold)' },
              ]}
              allowClear={false}
            />
          </FormField>
          {boundKind === 'literal' ? (
            <FormField label="Threshold" htmlFor="dr-cond-threshold">
              <Input
                id="dr-cond-threshold"
                type="number"
                value={threshold}
                onChange={(e) => setThreshold(e.target.value)}
                placeholder="80"
              />
            </FormField>
          ) : (
            <FormField
              label="Threshold attribute"
              htmlFor="dr-cond-attr"
              description="The device attribute whose numeric value is the bound, e.g. tempLimit."
            >
              <Input
                id="dr-cond-attr"
                value={thresholdAttr}
                onChange={(e) => setThresholdAttr(e.target.value)}
                placeholder="tempLimit"
              />
            </FormField>
          )}
        </div>
      )}

      {mode === 'cel' && (
        <FormField
          label="CEL expression"
          htmlFor="dr-cond-cel"
          description="An advanced predicate over the event vocabulary (m, attr, device). Type-checked and cost-gated at publish."
        >
          <Textarea
            id="dr-cond-cel"
            value={cel}
            onChange={(e) => setCel(e.target.value)}
            placeholder={'"temperature" in m && m["temperature"] > 80'}
            className="min-h-20 font-mono text-xs"
          />
        </FormField>
      )}
    </div>
  );
}

// ── The form ───────────────────────────────────────────────────────────────

export function DetectionRuleForm({
  profileToken,
  entity,
  onDone,
}: {
  profileToken: string;
  entity?: DetectionRule;
  onDone: (message: string) => void;
}) {
  const editing = entity != null;
  const initial = editing ? parseDefinition(entity.definition) : null;

  // Header.
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? initial?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? initial?.description ?? '');
  const [severity, setSeverity] = useState(initial?.severity ?? '');
  const [enabled, setEnabled] = useState(entity?.enabled ?? true);
  const [ruleType, setRuleType] = useState<RuleType>(initial?.type ?? 'threshold');

  // Condition.
  const [condMode, setCondMode] = useState<CondMode>(
    initial?.condMode ?? (conditionRequired(initial?.type ?? 'threshold') ? 'structured' : 'none'),
  );
  const [condMetric, setCondMetric] = useState(initial?.condMetric ?? '');
  const [condOp, setCondOp] = useState(initial?.condOp ?? 'gt');
  const [boundKind, setBoundKind] = useState<BoundKind>(initial?.boundKind ?? 'literal');
  const [condThreshold, setCondThreshold] = useState(initial?.condThreshold ?? '');
  const [condAttr, setCondAttr] = useState(initial?.condAttr ?? '');
  const [cel, setCel] = useState(initial?.cel ?? '');

  // Type-specific.
  const [valueMetric, setValueMetric] = useState(initial?.valueMetric ?? '');
  const [aggFunc, setAggFunc] = useState(initial?.aggFunc ?? 'avg');
  const [windowMode, setWindowMode] = useState(initial?.windowMode ?? 'tumbling');
  const [windowStr, setWindowStr] = useState(initial?.window ?? '');
  const [holdStr, setHoldStr] = useState(initial?.hold ?? '');
  const [timeoutStr, setTimeoutStr] = useState(initial?.timeout ?? '');
  const [gapStr, setGapStr] = useState(initial?.gap ?? '');
  const [countStr, setCountStr] = useState(initial?.count ?? '');
  const [rate, setRate] = useState(initial?.rate ?? false);
  const [aggOp, setAggOp] = useState(initial?.aggOp ?? 'gt');
  const [aggThreshold, setAggThreshold] = useState(initial?.aggThreshold ?? '');
  const [anchorType, setAnchorType] = useState(initial?.anchorType ?? '');
  const [memberCapStr, setMemberCapStr] = useState(initial?.memberCap ?? '');

  // Actions.
  const [actions, setActions] = useState<ActionRow[]>(initial?.actions ?? []);

  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const setActionAt = (i: number, patch: Partial<ActionRow>) =>
    setActions((rows) => rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const addAction = () =>
    setActions((rows) => [...rows, { type: 'raiseAlarm', alarmKey: '', command: '', payload: '' }]);
  const removeAction = (i: number) => setActions((rows) => rows.filter((_, j) => j !== i));

  const definitionName = name.trim() || token.trim();
  const hasRaiseAlarm = !actionsForbidden(ruleType) && actions.some((a) => a.type === 'raiseAlarm');

  // Client-side guard for the obvious omissions, so the button hints before the publish
  // gate (or the coming inline validator) rejects. The compiler remains authoritative.
  const hint = validationHint({
    editing,
    token,
    definitionName,
    ruleType,
    condMode,
    condMetric,
    condThreshold,
    condAttr,
    boundKind,
    cel,
    valueMetric,
    aggFunc,
    windowMode,
    windowStr,
    holdStr,
    timeoutStr,
    gapStr,
    countStr,
    aggThreshold,
    anchorType,
    hasRaiseAlarm,
    severity,
    actions,
  });

  const submit = async () => {
    setFormError(null);
    setBusy(true);
    try {
      const definition = buildDefinition({
        definitionName,
        description,
        severity,
        ruleType,
        condMode,
        condMetric,
        condOp,
        boundKind,
        condThreshold,
        condAttr,
        cel,
        valueMetric,
        aggFunc,
        windowMode,
        windowStr,
        holdStr,
        timeoutStr,
        gapStr,
        countStr,
        rate,
        aggOp,
        aggThreshold,
        anchorType,
        memberCapStr,
        actions,
      });
      const request: DetectionRuleCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        definition,
        enabled,
        metadata: entity?.metadata ?? undefined,
      };
      if (editing) {
        await updateDetectionRule(entity.token, request);
        onDone(`Detection rule “${request.token}” updated`);
      } else {
        await createDetectionRule(request);
        onDone(`Detection rule “${request.token}” created`);
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const showCount = ruleType === 'repeating' || ruleType === 'correlation' || (ruleType === 'aggregate' && windowMode === 'count');
  const aggNeedsValueMetric = ruleType === 'aggregate' && aggFunc !== 'count';

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <FormField label="Name" htmlFor="dr-name" description="A human name for the rule.">
        <Input id="dr-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Overheating" />
      </FormField>
      <FormField label="Description" htmlFor="dr-description">
        <Textarea
          id="dr-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="What this rule detects and why."
        />
      </FormField>
      <FormField label="Token" htmlFor="dr-token" description={editing ? 'The id; it cannot change.' : undefined}>
        {editing ? (
          <Input id="dr-token" value={token} disabled />
        ) : (
          <TokenField
            id="dr-token"
            entityType={normalizeToken('detection rule')}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder="rule-overheating"
          />
        )}
      </FormField>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <FormField label="Rule type" htmlFor="dr-type" description="What the rule detects.">
          <Combobox
            id="dr-type"
            value={ruleType}
            onChange={(v) => setRuleType(v as RuleType)}
            options={RULE_TYPES}
            allowClear={false}
          />
        </FormField>
        <FormField
          label="Severity"
          htmlFor="dr-severity"
          description={hasRaiseAlarm ? 'Required — the tier a raised alarm carries.' : 'The detection tier (optional).'}
        >
          <Combobox
            id="dr-severity"
            value={severity}
            onChange={setSeverity}
            options={SEVERITIES}
            placeholder="none"
          />
        </FormField>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4" />
        Enabled — a disabled rule is kept with the profile but not run.
      </label>

      {/* Condition leaf (structured / CEL / none), hidden for absence. */}
      <ConditionEditor
        ruleType={ruleType}
        mode={condMode}
        setMode={setCondMode}
        metric={condMetric}
        setMetric={setCondMetric}
        op={condOp}
        setOp={setCondOp}
        boundKind={boundKind}
        setBoundKind={setBoundKind}
        threshold={condThreshold}
        setThreshold={setCondThreshold}
        thresholdAttr={condAttr}
        setThresholdAttr={setCondAttr}
        cel={cel}
        setCel={setCel}
      />

      {/* Type-specific temporal / value fields. */}
      {ruleType === 'duration' && (
        <DurationField id="dr-hold" label="Sustain for" value={holdStr} onChange={setHoldStr} placeholder="5m" />
      )}

      {ruleType === 'absence' && (
        <DurationField id="dr-timeout" label="Silence window" value={timeoutStr} onChange={setTimeoutStr} placeholder="15m" />
      )}

      {ruleType === 'deltaRate' && (
        <div className="space-y-3">
          <FormField label="Value metric" htmlFor="dr-value-metric" description="The metric whose change is measured.">
            <Input
              id="dr-value-metric"
              value={valueMetric}
              onChange={(e) => setValueMetric(e.target.value)}
              placeholder="temperature"
            />
          </FormField>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={rate} onChange={(e) => setRate(e.target.checked)} className="h-4 w-4" />
            Per-second rate (otherwise the raw delta between samples).
          </label>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[auto_1fr]">
            <FormField label="Operator" htmlFor="dr-agg-op">
              <Combobox id="dr-agg-op" value={aggOp} onChange={setAggOp} options={ORDERED_OPS} allowClear={false} />
            </FormField>
            <FormField label="Threshold" htmlFor="dr-agg-threshold">
              <Input
                id="dr-agg-threshold"
                type="number"
                value={aggThreshold}
                onChange={(e) => setAggThreshold(e.target.value)}
                placeholder="10"
              />
            </FormField>
          </div>
        </div>
      )}

      {ruleType === 'aggregate' && (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <FormField label="Aggregate" htmlFor="dr-agg-func">
              <Combobox id="dr-agg-func" value={aggFunc} onChange={setAggFunc} options={AGG_FUNCS} allowClear={false} />
            </FormField>
            <FormField label="Window mode" htmlFor="dr-window-mode">
              <Combobox
                id="dr-window-mode"
                value={windowMode}
                onChange={setWindowMode}
                options={WINDOW_MODES}
                allowClear={false}
              />
            </FormField>
          </div>
          {aggNeedsValueMetric && (
            <FormField label="Value metric" htmlFor="dr-agg-value-metric" description="The metric folded by the aggregate.">
              <Input
                id="dr-agg-value-metric"
                value={valueMetric}
                onChange={(e) => setValueMetric(e.target.value)}
                placeholder="temperature"
              />
            </FormField>
          )}
          {(windowMode === 'tumbling' || windowMode === 'sliding') && (
            <DurationField id="dr-agg-window" label="Window" value={windowStr} onChange={setWindowStr} placeholder="5m" />
          )}
          {windowMode === 'session' && (
            <DurationField id="dr-agg-gap" label="Session gap" value={gapStr} onChange={setGapStr} placeholder="1m" />
          )}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[auto_1fr]">
            <FormField label="Operator" htmlFor="dr-agg-op2">
              <Combobox id="dr-agg-op2" value={aggOp} onChange={setAggOp} options={ORDERED_OPS} allowClear={false} />
            </FormField>
            <FormField label="Threshold" htmlFor="dr-agg-threshold2">
              <Input
                id="dr-agg-threshold2"
                type="number"
                value={aggThreshold}
                onChange={(e) => setAggThreshold(e.target.value)}
                placeholder="30"
              />
            </FormField>
          </div>
        </div>
      )}

      {ruleType === 'repeating' && (
        <DurationField id="dr-rep-window" label="Within window" value={windowStr} onChange={setWindowStr} placeholder="10m" />
      )}

      {ruleType === 'correlation' && (
        <div className="space-y-3">
          <FormField
            label="Anchor type"
            htmlFor="dr-anchor"
            description="The anchor (area) relationship distinct devices roll up to."
          >
            <Input id="dr-anchor" value={anchorType} onChange={(e) => setAnchorType(e.target.value)} placeholder="zone" />
          </FormField>
          <DurationField id="dr-corr-window" label="Within window" value={windowStr} onChange={setWindowStr} placeholder="5m" />
          <FormField
            label="Member cap"
            htmlFor="dr-member-cap"
            description="Optional retained-member backstop; defaults to the platform limit."
          >
            <Input
              id="dr-member-cap"
              type="number"
              value={memberCapStr}
              onChange={(e) => setMemberCapStr(e.target.value)}
              placeholder="1024"
            />
          </FormField>
        </div>
      )}

      {showCount && (
        <FormField
          label={ruleType === 'correlation' ? 'Distinct devices' : 'Occurrences'}
          htmlFor="dr-count"
          description={
            ruleType === 'correlation'
              ? 'The number of distinct devices that must report within the window.'
              : ruleType === 'repeating'
                ? 'The number of matching events within the window.'
                : 'The number of events per count window.'
          }
        >
          <Input
            id="dr-count"
            type="number"
            value={countStr}
            onChange={(e) => setCountStr(e.target.value)}
            placeholder="3"
          />
        </FormField>
      )}

      {/* REACT action chain. */}
      {actionsForbidden(ruleType) ? (
        <p className="rounded-md border border-dashed px-3 py-3 text-sm text-muted-foreground">
          An area-correlation rule cannot carry actions — its series is an area anchor, not a device.
        </p>
      ) : (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium">Actions</p>
              <p className="text-sm text-muted-foreground">
                What happens when the rule fires. No action ⇒ it emits a subscribe-able signal only.
              </p>
            </div>
            <Button size="sm" variant="outline" onClick={addAction} disabled={actions.length >= 8}>
              Add action
            </Button>
          </div>
          {actions.map((a, i) => (
            <div key={i} className="space-y-3 rounded-md border p-3">
              <div className="flex items-center justify-between gap-2">
                <FormField label={`Action ${i + 1}`} htmlFor={`dr-action-${i}`}>
                  <Combobox
                    id={`dr-action-${i}`}
                    value={a.type}
                    onChange={(v) => setActionAt(i, { type: v as ActionKind })}
                    options={[
                      { value: 'raiseAlarm', label: 'Raise alarm' },
                      { value: 'sendCommand', label: 'Send command' },
                    ]}
                    allowClear={false}
                  />
                </FormField>
                <Button size="sm" variant="ghost" onClick={() => removeAction(i)}>
                  Remove
                </Button>
              </div>
              {a.type === 'raiseAlarm' ? (
                <FormField
                  label="Alarm key"
                  htmlFor={`dr-alarm-key-${i}`}
                  description="Optional. Repeated firings escalate one alarm keyed here; blank ⇒ the rule's token."
                >
                  <Input
                    id={`dr-alarm-key-${i}`}
                    value={a.alarmKey}
                    onChange={(e) => setActionAt(i, { alarmKey: e.target.value })}
                    placeholder="(rule token)"
                  />
                </FormField>
              ) : (
                <div className="space-y-3">
                  <FormField label="Command" htmlFor={`dr-command-${i}`} description="The command key on this profile.">
                    <Input
                      id={`dr-command-${i}`}
                      value={a.command}
                      onChange={(e) => setActionAt(i, { command: e.target.value })}
                      placeholder="set_point"
                    />
                  </FormField>
                  <FormField
                    label="Payload"
                    htmlFor={`dr-payload-${i}`}
                    description="Optional JSON object of arguments, stored verbatim."
                  >
                    <Textarea
                      id={`dr-payload-${i}`}
                      value={a.payload}
                      onChange={(e) => setActionAt(i, { payload: e.target.value })}
                      placeholder='{"level":0}'
                      className="min-h-16 font-mono text-xs"
                    />
                  </FormField>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {hint && <p className="text-sm text-amber-600 dark:text-amber-500">{hint}</p>}

      <div className="flex gap-2 pt-1">
        <Button onClick={submit} loading={busy} disabled={busy || hint != null}>
          {editing ? 'Save changes' : 'Create detection rule'}
        </Button>
      </div>
    </div>
  );
}

// ── Serialization: form state → rules.Rule JSON ────────────────────────────

interface BuildArgs {
  definitionName: string;
  description: string;
  severity: string;
  ruleType: RuleType;
  condMode: CondMode;
  condMetric: string;
  condOp: string;
  boundKind: BoundKind;
  condThreshold: string;
  condAttr: string;
  cel: string;
  valueMetric: string;
  aggFunc: string;
  windowMode: string;
  windowStr: string;
  holdStr: string;
  timeoutStr: string;
  gapStr: string;
  countStr: string;
  rate: boolean;
  aggOp: string;
  aggThreshold: string;
  anchorType: string;
  memberCapStr: string;
  actions: ActionRow[];
}

// buildDefinition emits ONLY the fields the chosen type's compiler lowering reads — a clean
// per-type rebuild, so a field left over from a previous type can never trip the compiler's
// fail-closed forbid() checks. Numbers/durations are emitted as the schema expects (durations
// as Go duration strings, thresholds as JSON numbers).
function buildDefinition(a: BuildArgs): string {
  const def: Record<string, unknown> = { name: a.definitionName, type: a.ruleType };
  if (a.description.trim()) def.description = a.description.trim();
  if (a.severity) def.severity = a.severity;

  const when = buildWhen(a);
  if (when) def.when = when;

  switch (a.ruleType) {
    case 'threshold':
      break;
    case 'duration':
      def.hold = a.holdStr.trim();
      break;
    case 'absence':
      def.timeout = a.timeoutStr.trim();
      break;
    case 'repeating':
      def.count = intOrZero(a.countStr);
      def.window = a.windowStr.trim();
      break;
    case 'deltaRate':
      def.metric = a.valueMetric.trim();
      def.op = a.aggOp;
      def.threshold = numOrZero(a.aggThreshold);
      if (a.rate) def.rate = true;
      break;
    case 'aggregate':
      def.agg = a.aggFunc;
      def.op = a.aggOp;
      def.threshold = numOrZero(a.aggThreshold);
      def.windowMode = a.windowMode;
      if (a.aggFunc !== 'count') def.metric = a.valueMetric.trim();
      if (a.windowMode === 'tumbling' || a.windowMode === 'sliding') def.window = a.windowStr.trim();
      else if (a.windowMode === 'session') def.gap = a.gapStr.trim();
      else if (a.windowMode === 'count') def.count = intOrZero(a.countStr);
      break;
    case 'correlation':
      def.anchorType = a.anchorType.trim();
      def.count = intOrZero(a.countStr);
      def.window = a.windowStr.trim();
      if (a.memberCapStr.trim()) def.memberCap = intOrZero(a.memberCapStr);
      break;
  }

  if (!actionsForbidden(a.ruleType) && a.actions.length > 0) {
    def.actions = a.actions.map((act) => buildAction(act));
  }

  return JSON.stringify(def);
}

function buildWhen(a: BuildArgs): Record<string, unknown> | undefined {
  if (conditionForbidden(a.ruleType) || a.condMode === 'none') return undefined;
  if (a.condMode === 'cel') {
    return a.cel.trim() ? { cel: a.cel } : undefined;
  }
  // structured
  const w: Record<string, unknown> = { metric: a.condMetric.trim(), op: a.condOp };
  if (a.boundKind === 'attr') w.thresholdAttr = a.condAttr.trim();
  else w.threshold = numOrZero(a.condThreshold);
  return w;
}

function buildAction(a: ActionRow): Record<string, unknown> {
  if (a.type === 'sendCommand') {
    const sc: Record<string, unknown> = { command: a.command.trim() };
    if (a.payload.trim()) sc.payload = a.payload;
    return { type: 'sendCommand', sendCommand: sc };
  }
  const ra: Record<string, unknown> = {};
  if (a.alarmKey.trim()) ra.alarmKey = a.alarmKey.trim();
  return { type: 'raiseAlarm', raiseAlarm: ra };
}

function numOrZero(s: string): number {
  const n = Number(s);
  return Number.isFinite(n) ? n : 0;
}
function intOrZero(s: string): number {
  const n = parseInt(s, 10);
  return Number.isFinite(n) ? n : 0;
}

// ── Parsing: existing rules.Rule JSON → form state (best-effort, for edit) ──

interface ParsedDefinition {
  name: string;
  description: string;
  severity: string;
  type: RuleType;
  condMode: CondMode;
  condMetric: string;
  condOp: string;
  boundKind: BoundKind;
  condThreshold: string;
  condAttr: string;
  cel: string;
  valueMetric: string;
  aggFunc: string;
  windowMode: string;
  window: string;
  hold: string;
  timeout: string;
  gap: string;
  count: string;
  rate: boolean;
  aggOp: string;
  aggThreshold: string;
  anchorType: string;
  memberCap: string;
  actions: ActionRow[];
}

const KNOWN_TYPES: RuleType[] = ['threshold', 'duration', 'absence', 'repeating', 'deltaRate', 'aggregate', 'correlation'];

// Reads a stored definition into form state. It is defensive (a hand- or API-authored rule
// may carry shapes the form does not model): anything unreadable falls back to a sensible
// default so the drawer always opens; the compiler re-validates on the next publish.
function parseDefinition(raw: string): ParsedDefinition | null {
  let d: Record<string, unknown>;
  try {
    d = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return null;
  }
  const str = (v: unknown): string => (typeof v === 'string' ? v : '');
  const numStr = (v: unknown): string => (typeof v === 'number' ? String(v) : '');
  const type = KNOWN_TYPES.includes(d.type as RuleType) ? (d.type as RuleType) : 'threshold';

  // Condition.
  const when = (d.when ?? {}) as Record<string, unknown>;
  let condMode: CondMode = 'none';
  let boundKind: BoundKind = 'literal';
  if (str(when.cel)) condMode = 'cel';
  else if (str(when.metric) || str(when.op)) {
    condMode = 'structured';
    boundKind = str(when.thresholdAttr) ? 'attr' : 'literal';
  } else if (conditionRequired(type)) {
    condMode = 'structured';
  }

  // Actions.
  const rawActions = Array.isArray(d.actions) ? (d.actions as Record<string, unknown>[]) : [];
  const actions: ActionRow[] = rawActions.map((act) => {
    const t = str(act.type) === 'sendCommand' ? 'sendCommand' : 'raiseAlarm';
    const ra = (act.raiseAlarm ?? {}) as Record<string, unknown>;
    const sc = (act.sendCommand ?? {}) as Record<string, unknown>;
    return {
      type: t as ActionKind,
      alarmKey: str(ra.alarmKey),
      command: str(sc.command),
      payload: str(sc.payload),
    };
  });

  return {
    name: str(d.name),
    description: str(d.description),
    severity: str(d.severity),
    type,
    condMode,
    condMetric: str(when.metric),
    condOp: str(when.op) || 'gt',
    boundKind,
    condThreshold: numStr(when.threshold),
    condAttr: str(when.thresholdAttr),
    cel: str(when.cel),
    valueMetric: str(d.metric),
    aggFunc: str(d.agg) || 'avg',
    windowMode: str(d.windowMode) || 'tumbling',
    window: str(d.window),
    hold: str(d.hold),
    timeout: str(d.timeout),
    gap: str(d.gap),
    count: numStr(d.count),
    rate: d.rate === true,
    aggOp: str(d.op) || 'gt',
    aggThreshold: numStr(d.threshold),
    anchorType: str(d.anchorType),
    memberCap: numStr(d.memberCap),
    actions,
  };
}

// ── Client-side pre-publish hint ────────────────────────────────────────────

interface HintArgs {
  editing: boolean;
  token: string;
  definitionName: string;
  ruleType: RuleType;
  condMode: CondMode;
  condMetric: string;
  condThreshold: string;
  condAttr: string;
  boundKind: BoundKind;
  cel: string;
  valueMetric: string;
  aggFunc: string;
  windowMode: string;
  windowStr: string;
  holdStr: string;
  timeoutStr: string;
  gapStr: string;
  countStr: string;
  aggThreshold: string;
  anchorType: string;
  hasRaiseAlarm: boolean;
  severity: string;
  actions: ActionRow[];
}

// validationHint returns the first obvious omission to surface before submit, or null. It is a
// UX convenience, NOT the authority — event-processing's compiler re-checks everything at
// publish (structure, CEL type/cost, injection). It deliberately mirrors only the cheap,
// unambiguous required-field rules from compile.go.
function validationHint(a: HintArgs): string | null {
  if (!a.editing && !a.token.trim()) return 'A token is required.';
  if (!a.definitionName) return 'A name is required.';

  // Condition. Absence forbids a leaf entirely; for every other type a chosen structured or
  // CEL condition must be complete (a blank structured gate would emit malformed CEL).
  if (!conditionForbidden(a.ruleType)) {
    if (a.condMode === 'structured') {
      if (!a.condMetric.trim()) return 'The condition needs a metric.';
      if (a.boundKind === 'literal' && a.condThreshold.trim() === '') return 'The condition needs a threshold value.';
      if (a.boundKind === 'attr' && !a.condAttr.trim()) return 'The condition needs a threshold attribute.';
    }
    if (a.condMode === 'cel' && !a.cel.trim()) return 'The CEL expression is empty.';
  }

  switch (a.ruleType) {
    case 'duration':
      if (!a.holdStr.trim()) return 'Duration needs a sustain-for time.';
      break;
    case 'absence':
      if (!a.timeoutStr.trim()) return 'Absence needs a silence window.';
      break;
    case 'repeating':
      if (!a.countStr.trim()) return 'Repeating needs an occurrence count.';
      if (!a.windowStr.trim()) return 'Repeating needs a window.';
      break;
    case 'deltaRate':
      if (!a.valueMetric.trim()) return 'Rate-of-change needs a value metric.';
      if (a.aggThreshold.trim() === '') return 'Rate-of-change needs a threshold.';
      break;
    case 'aggregate':
      if (a.aggFunc !== 'count' && !a.valueMetric.trim()) return 'This aggregate needs a value metric.';
      if (a.aggThreshold.trim() === '') return 'The aggregate needs a threshold.';
      if ((a.windowMode === 'tumbling' || a.windowMode === 'sliding') && !a.windowStr.trim())
        return 'The aggregate needs a window.';
      if (a.windowMode === 'session' && !a.gapStr.trim()) return 'A session aggregate needs a gap.';
      if (a.windowMode === 'count' && !a.countStr.trim()) return 'A count-window aggregate needs a count.';
      break;
    case 'correlation':
      if (!a.anchorType.trim()) return 'Correlation needs an anchor type.';
      if (!a.countStr.trim()) return 'Correlation needs a distinct-device count.';
      if (!a.windowStr.trim()) return 'Correlation needs a window.';
      break;
  }

  // A raiseAlarm action requires a severity tier.
  if (a.hasRaiseAlarm && !a.severity) return 'A raise-alarm action requires a severity.';
  for (const act of a.actions) {
    if (act.type === 'sendCommand' && !act.command.trim()) return 'A send-command action needs a command.';
  }
  return null;
}
