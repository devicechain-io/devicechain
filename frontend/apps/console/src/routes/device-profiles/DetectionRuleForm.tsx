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
// is rejected at publish with the compiler's message. As an authoring aid the form also
// debounce-compiles the rule through event-processing's validateDetectionRules gate
// (slice 7a-2), surfacing the compiler's diagnostics inline before publish; that check is
// advisory and never blocks the draft save.
//
// The per-type field set mirrors the event-processing compiler's per-type contract
// (internal/rules/compile.go): each rule type allows exactly the fields its lowering reads
// and FORBIDS the rest (fail-closed), so the form emits only the fields valid for the
// chosen type — a clean rebuild per type, never a merge that could leave a stale field the
// compiler then rejects.

import { useEffect, useState } from 'react';
import { useTranslation, Trans } from 'react-i18next';
import type { TFunction } from 'i18next';
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
  listScopeGroups,
  listEntityGroupVersions,
  type DetectionRule,
  type DetectionRuleCreateRequest,
  type ScopeGroup,
  type ScopeGroupVersion,
} from '@/lib/api/device-management';
import { previewSelector } from '@/lib/api/browse';
import { validateDetectionRule } from '@/lib/api/event-processing';
import { sameLogicalRule } from './rule-equal';

// ── The rule taxonomy (mirrors rules.RuleType) ─────────────────────────────

type RuleType = 'threshold' | 'duration' | 'absence' | 'repeating' | 'deltaRate' | 'aggregate' | 'correlation';

// Option arrays are FUNCTIONS of `t` (called inside the component that renders them), never
// module-scope useTranslation — only the `value` fields are machine discriminants (serialized
// into the rule JSON / compared in logic); `label`/`description` are display text.
function ruleTypeOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'threshold', label: t('ruleTypeThresholdLabel'), description: t('ruleTypeThresholdDescription') },
    { value: 'duration', label: t('ruleTypeDurationLabel'), description: t('ruleTypeDurationDescription') },
    { value: 'absence', label: t('ruleTypeAbsenceLabel'), description: t('ruleTypeAbsenceDescription') },
    { value: 'repeating', label: t('ruleTypeRepeatingLabel'), description: t('ruleTypeRepeatingDescription') },
    { value: 'deltaRate', label: t('ruleTypeDeltaRateLabel'), description: t('ruleTypeDeltaRateDescription') },
    { value: 'aggregate', label: t('ruleTypeAggregateLabel'), description: t('ruleTypeAggregateDescription') },
    { value: 'correlation', label: t('ruleTypeCorrelationLabel'), description: t('ruleTypeCorrelationDescription') },
  ];
}

// Severity labels are shared with the alarms area's own vocabulary (already translated there);
// reused here rather than duplicating the catalog entries.
function severityOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'critical', label: t('alarms:sevCritical') },
    { value: 'major', label: t('alarms:sevMajor') },
    { value: 'minor', label: t('alarms:sevMinor') },
    { value: 'warning', label: t('alarms:sevWarning') },
    { value: 'indeterminate', label: t('alarms:sevIndeterminate') },
  ];
}

// The structured leaf comparison allows eq/ne (it lowers to CEL, where equality is
// well-defined); the engine-side aggregate/delta comparison is ordered-only.
function allOps(t: TFunction): ComboboxOption[] {
  return [
    { value: 'gt', label: t('ruleOpGt') },
    { value: 'ge', label: t('ruleOpGe') },
    { value: 'lt', label: t('ruleOpLt') },
    { value: 'le', label: t('ruleOpLe') },
    { value: 'eq', label: t('ruleOpEq') },
    { value: 'ne', label: t('ruleOpNe') },
  ];
}
function orderedOps(t: TFunction): ComboboxOption[] {
  return allOps(t).filter((o) => o.value !== 'eq' && o.value !== 'ne');
}

function aggFuncOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'count', label: t('ruleAggFuncCount') },
    { value: 'sum', label: t('ruleAggFuncSum') },
    { value: 'avg', label: t('ruleAggFuncAvg') },
    { value: 'min', label: t('ruleAggFuncMin') },
    { value: 'max', label: t('ruleAggFuncMax') },
  ];
}

function windowModeOptions(t: TFunction): ComboboxOption[] {
  return [
    { value: 'tumbling', label: t('ruleWindowModeTumblingLabel'), description: t('ruleWindowModeTumblingDescription') },
    { value: 'sliding', label: t('ruleWindowModeSlidingLabel'), description: t('ruleWindowModeSlidingDescription') },
    { value: 'session', label: t('ruleWindowModeSessionLabel'), description: t('ruleWindowModeSessionDescription') },
    { value: 'count', label: t('ruleWindowModeCountLabel'), description: t('ruleWindowModeCountDescription') },
  ];
}

// Condition editors: a required-leaf type offers structured|cel; an optional-leaf type
// also offers "match every event"; absence takes no leaf at all.
type CondMode = 'structured' | 'cel' | 'none';
type BoundKind = 'literal' | 'attr';
type ActionKind = 'raiseAlarm' | 'sendCommand' | 'httpCall' | 'publish';

interface ActionRow {
  type: ActionKind;
  alarmKey: string;
  command: string;
  payload: string;
  // The per-action REACT guard (slice 9c) — authored on the canvas (a Branch node), not here. The
  // form carries it through UNCHANGED so opening a canvas-authored guarded rule in the form and
  // saving does not silently strip the guard; a guarded row shows a read-only note steering the
  // author back to the canvas to change it.
  guard?: string;
  // The outbound REACT actions (httpCall / publish, ADR-060) are authored on the Canvas, not in
  // this form. The form carries such an action through VERBATIM (the original wire object) so a
  // canvas-authored connector rule opened here and saved is not corrupted — mirroring the guard
  // pass-through. `raw`, when set, is the whole wire action and is emitted unchanged.
  raw?: Record<string, unknown>;
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
  const { t } = useTranslation('deviceProfiles');
  return (
    <FormField label={label} htmlFor={id} description={t('ruleDurationFieldDescription')}>
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
  const { t } = useTranslation('deviceProfiles');
  if (conditionForbidden(ruleType)) return null;

  const required = conditionRequired(ruleType);
  const modeOptions: ComboboxOption[] = [
    { value: 'structured', label: t('ruleConditionModeStructuredLabel') },
    { value: 'cel', label: t('ruleConditionModeCelLabel') },
    ...(required ? [] : [{ value: 'none', label: t('ruleConditionModeNoneLabel') } as ComboboxOption]),
  ];

  return (
    <div className="space-y-3 rounded-md border p-3">
      <FormField
        label={t('ruleConditionLabel')}
        htmlFor="dr-cond-mode"
        description={required ? t('ruleConditionDescriptionRequired') : t('ruleConditionDescriptionOptional')}
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
            <FormField label={t('ruleMetricLabel')} htmlFor="dr-cond-metric" description={t('ruleMetricDescription')}>
              <Input
                id="dr-cond-metric"
                value={metric}
                onChange={(e) => setMetric(e.target.value)}
                placeholder={t('ruleMetricPlaceholder')}
              />
            </FormField>
            <FormField label={t('ruleOperatorLabel')} htmlFor="dr-cond-op">
              <Combobox id="dr-cond-op" value={op} onChange={setOp} options={allOps(t)} allowClear={false} />
            </FormField>
          </div>
          <FormField label={t('ruleBoundLabel')} htmlFor="dr-cond-bound-kind" description={t('ruleBoundDescription')}>
            <Combobox
              id="dr-cond-bound-kind"
              value={boundKind}
              onChange={(v) => setBoundKind(v as BoundKind)}
              // 'literal'/'attr' are the BoundKind discriminant, never user text — only the
              // labels (translated above) are display text.
              /* eslint-disable i18next/no-literal-string */
              options={[
                { value: 'literal', label: t('ruleBoundKindLiteralLabel') },
                { value: 'attr', label: t('ruleBoundKindAttrLabel') },
              ]}
              /* eslint-enable i18next/no-literal-string */
              allowClear={false}
            />
          </FormField>
          {boundKind === 'literal' ? (
            <FormField label={t('ruleThresholdLabel')} htmlFor="dr-cond-threshold">
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
              label={t('ruleThresholdAttrLabel')}
              htmlFor="dr-cond-attr"
              description={t('ruleThresholdAttrDescription')}
            >
              <Input
                id="dr-cond-attr"
                value={thresholdAttr}
                onChange={(e) => setThresholdAttr(e.target.value)}
                placeholder={t('ruleThresholdAttrPlaceholder')}
              />
            </FormField>
          )}
        </div>
      )}

      {mode === 'cel' && (
        <FormField
          label={t('ruleCelLabel')}
          htmlFor="dr-cond-cel"
          description={t('ruleCelDescription')}
        >
          <Textarea
            id="dr-cond-cel"
            value={cel}
            onChange={(e) => setCel(e.target.value)}
            // Example CEL syntax, not prose — the metric key + operators here are literal rule
            // grammar, never translated (mirrors the domain-note carve-out for CEL fragments).
            // eslint-disable-next-line i18next/no-literal-string
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
  initialDefinition,
  onDone,
}: {
  profileToken: string;
  entity?: DetectionRule;
  // An unsaved rules.Rule definition JSON to pre-fill a NEW rule from — the seam the NL door
  // (ADR-056 slice 1) hands its compiled draft through. Unlike `entity` it does NOT flip the
  // form into edit mode: there is no stored rule yet, so the token stays required/editable and
  // saving runs the create path. Ignored when `entity` is set (editing an existing rule wins).
  initialDefinition?: string;
  onDone: (message: string) => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const editing = entity != null;
  // Pre-fill precedence: the stored rule when editing, else an NL/handoff draft, else blank.
  const initial = editing
    ? parseDefinition(entity.definition)
    : initialDefinition != null
      ? parseDefinition(initialDefinition)
      : null;

  // Header.
  const [token, setToken] = useState(entity?.token ?? '');
  const [name, setName] = useState(entity?.name ?? initial?.name ?? '');
  const [description, setDescription] = useState(entity?.description ?? initial?.description ?? '');
  const [severity, setSeverity] = useState(initial?.severity ?? '');
  const [enabled, setEnabled] = useState(entity?.enabled ?? true);
  // ADR-062 S4 group scope: a rule may be pinned to a published dynamic entity-group version,
  // so it fires only for events whose resolved entity is a member of that group@version.
  const [scoped, setScoped] = useState<boolean>(!!entity?.entityGroupToken);
  const [scopeGroupToken, setScopeGroupToken] = useState<string>(entity?.entityGroupToken ?? '');
  const [scopeGroupVersion, setScopeGroupVersion] = useState<number | null>(entity?.entityGroupVersion ?? null);
  const [scopeGroups, setScopeGroups] = useState<ScopeGroup[]>([]);
  const [scopeVersions, setScopeVersions] = useState<ScopeGroupVersion[]>([]);
  const [scopeVersionsStatus, setScopeVersionsStatus] = useState<'idle' | 'loading' | 'ok' | 'error'>('idle');
  const [scopeCount, setScopeCount] = useState<
    { status: 'idle' | 'checking' | 'ok' | 'error'; total?: number; message?: string }
  >({ status: 'idle' });
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
  // A stored definition the form can't parse (hand-/API-authored into a shape it doesn't
  // model) opens as a blank threshold; warn that saving replaces it rather than silently
  // clobbering the original (Fable L2).
  const unparseable = editing && initial == null;

  // Client-side guard for the obvious omissions, so the button hints instantly (no round
  // trip) before the inline compiler check or the publish gate rejects. It mirrors only the
  // cheap required-field rules; the compiler remains authoritative.
  const hint = validationHint({
    t,
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

  // The emitted rules.Rule JSON, computed only when the form passes the local hint (so an
  // incomplete rule is never sent for validation or save). buildDefinition is a pure
  // object-build + JSON.stringify, so recomputing per render is cheap and the string VALUE
  // is stable across renders when inputs are — which keeps the validation effect below from
  // firing on every render.
  const definition =
    hint == null
      ? buildDefinition({
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
        })
      : null;

  // Inline server-authoritative validation: debounce-compile the emitted rule through
  // event-processing's gate so a type error or cost-over-ceiling the local hint can't see
  // (CEL type-checking, the predicate cost ceiling, semantic constraints like a constant
  // count-over-count aggregate) shows BEFORE publish. Advisory only — it never blocks the
  // draft save (a draft may be a work in progress; publish is the enforcing gate), and a
  // transport/permission error is swallowed rather than surfaced as a rule problem.
  const validateToken = editing ? entity.token : token.trim();
  const [validation, setValidation] = useState<{ status: 'checking' | 'ok' | 'error'; message?: string } | null>(null);
  useEffect(() => {
    if (definition == null) {
      setValidation(null);
      return;
    }
    let cancelled = false;
    setValidation({ status: 'checking' });
    const timer = setTimeout(async () => {
      try {
        // Bound the wait: a hung (accepting-but-not-responding) pod would otherwise leave
        // "Checking…" up until the browser's own network timeout. Racing a 10s timeout clears
        // the advisory feedback instead (Fable LOW); the stray loser timer is a harmless no-op.
        const res = await Promise.race([
          // Pass the actual scoped state (a group is chosen) so the gate rejects a scope on an
          // unsupported kind (absence/correlation) inline, not only at publish (ADR-062 S4).
          // Keyed on the group being chosen, not the checkbox — an empty scope saves unscoped.
          validateDetectionRule(validateToken, definition, scoped && !!scopeGroupToken),
          new Promise<never>((_, reject) => setTimeout(() => reject(new Error('validate timeout')), 10_000)),
        ]);
        if (cancelled) return;
        setValidation(res.ok ? { status: 'ok' } : { status: 'error', message: res.message ?? undefined });
      } catch {
        if (!cancelled) setValidation(null);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [definition, validateToken, scoped, scopeGroupToken]);

  // Load the dynamic groups a rule can scope to, the first time scoping is turned on.
  useEffect(() => {
    if (!scoped || scopeGroups.length > 0) return;
    let cancelled = false;
    listScopeGroups()
      .then((gs) => {
        if (!cancelled) setScopeGroups(gs);
      })
      .catch(() => {
        /* the picker just stays empty; the field is optional */
      });
    return () => {
      cancelled = true;
    };
  }, [scoped, scopeGroups.length]);

  // Load the selected group's published versions; default to the newest when none is chosen.
  useEffect(() => {
    if (!scopeGroupToken) {
      setScopeVersions([]);
      setScopeVersionsStatus('idle');
      return;
    }
    let cancelled = false;
    setScopeVersionsStatus('loading');
    listEntityGroupVersions(scopeGroupToken)
      .then((vs) => {
        if (cancelled) return;
        setScopeVersions(vs);
        setScopeVersionsStatus('ok');
        setScopeGroupVersion((cur) =>
          cur != null && vs.some((v) => v.version === cur) ? cur : (vs[0]?.version ?? null),
        );
      })
      .catch(() => {
        if (cancelled) return;
        setScopeVersions([]);
        setScopeVersionsStatus('error');
      });
    return () => {
      cancelled = true;
    };
  }, [scopeGroupToken]);

  // Live "matches N entities" preview for the PINNED version's frozen selector (the exact set
  // the scoped rule will target), mirroring the browse screen's previewSelector count.
  useEffect(() => {
    // Skip the preview for an unsupported kind (the count line is hidden anyway) and any
    // incomplete scope — no wasted selector query.
    const unsupportedKind = ruleType === 'absence' || ruleType === 'correlation';
    if (!scoped || !scopeGroupToken || scopeGroupVersion == null || unsupportedKind) {
      setScopeCount({ status: 'idle' });
      return;
    }
    const version = scopeVersions.find((v) => v.version === scopeGroupVersion);
    if (!version) {
      setScopeCount({ status: 'idle' });
      return;
    }
    let cancelled = false;
    setScopeCount({ status: 'checking' });
    previewSelector(version.memberType, version.selector, 1)
      .then((preview) => {
        if (cancelled) return;
        if (!preview.valid || !preview.members) {
          setScopeCount({ status: 'error', message: preview.error ?? t('ruleScopeSelectorInvalidFallback') });
          return;
        }
        setScopeCount({ status: 'ok', total: preview.members.pagination.totalRecords ?? 0 });
      })
      .catch(() => {
        if (!cancelled) setScopeCount({ status: 'idle' });
      });
    return () => {
      cancelled = true;
    };
  }, [scoped, scopeGroupToken, scopeGroupVersion, scopeVersions, ruleType]);

  const submit = async () => {
    if (definition == null) return; // guarded by the disabled button; satisfies the type narrowing
    setFormError(null);
    setBusy(true);
    try {
      // Preserve a canvas-authored rule's AuthoringGraph sidecar across an INCIDENTAL form
      // edit (parking it via Enabled, a metadata change) — anything that leaves the rules.Rule
      // logically unchanged. If the form changed the definition, the stored graph would no
      // longer mirror it, so we drop it (the canvas re-synthesizes a fresh layout via the
      // reverse round-trip). Comparison is logical (parsed, order-independent), because the
      // form re-emits its own key order, not the canvas's canonical bytes. (Fable 9b-1 MED.)
      const keepGraph = editing && entity.authoringGraph != null && sameLogicalRule(definition, entity.definition);
      const request: DetectionRuleCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        definition,
        authoringGraph: keepGraph ? entity!.authoringGraph : undefined,
        enabled,
        metadata: entity?.metadata ?? undefined,
        // ADR-062 S4 group scope — sent together or not at all; cleared when un-scoped.
        entityGroupToken: scoped && scopeGroupToken ? scopeGroupToken : undefined,
        entityGroupVersion: scoped && scopeGroupToken && scopeGroupVersion != null ? scopeGroupVersion : undefined,
      };
      if (editing) {
        await updateDetectionRule(entity.token, request);
        onDone(t('ruleUpdatedToast', { token: request.token }));
      } else {
        await createDetectionRule(request);
        onDone(t('ruleCreatedToast', { token: request.token }));
      }
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const showCount = ruleType === 'repeating' || ruleType === 'correlation' || (ruleType === 'aggregate' && windowMode === 'count');
  const aggNeedsValueMetric = ruleType === 'aggregate' && aggFunc !== 'count';

  // ADR-062 S4 scope picker options + the unsupported-kind guard (absence is timer-driven off
  // the roster; correlation is anchor-keyed — a group scope cannot apply to either, and the
  // publish gate rejects it).
  const scopeGroupOptions: ComboboxOption[] = scopeGroups.map((g) => ({
    value: g.token,
    label: t('ruleScopeGroupOptionLabel', { name: g.name ?? g.token, memberType: g.memberType }),
  }));
  const scopeVersionOptions: ComboboxOption[] = scopeVersions.map((v) => ({
    value: String(v.version),
    label: v.label ? `v${v.version} — ${v.label}` : `v${v.version}`,
  }));
  // A scope is only actually applied once a GROUP is chosen — key the advisory refusal + the
  // half-set gate on that, not on the checkbox alone (ticking "scope" without picking a group
  // still saves an UNSCOPED rule, so a refusal warning there would be misleading).
  const scopeChosen = scoped && !!scopeGroupToken;
  const scopeUnsupportedKind = scopeChosen && (ruleType === 'absence' || ruleType === 'correlation');
  // The member family of the selected group, to word the count ("3 areas" not "3 entities").
  const scopeMemberType = scopeGroups.find((g) => g.token === scopeGroupToken)?.memberType ?? 'entity';
  // Block save on a half-set scope (a group with no version chosen — an unpublished group, or a
  // click before the versions fetch resolved); the backend would reject the publish anyway.
  const scopeHint = scopeChosen && scopeGroupVersion == null ? t('ruleScopeHalfSetHint') : null;

  return (
    <div className="space-y-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}
      {unparseable && <p className="rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">{t('ruleUnparseableWarning')}</p>}

      <FormField label={t('common:colName')} htmlFor="dr-name" description={t('ruleNameDescription')}>
        <Input id="dr-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('ruleNamePlaceholder')} />
      </FormField>
      <FormField label={t('common:colDescription')} htmlFor="dr-description">
        <Textarea
          id="dr-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t('ruleDescriptionPlaceholder')}
        />
      </FormField>
      <FormField label={t('common:colToken')} htmlFor="dr-token" description={editing ? t('ruleTokenLockedDescription') : undefined}>
        {editing ? (
          <Input id="dr-token" value={token} disabled />
        ) : (
          <TokenField
            id="dr-token"
            entityType={normalizeToken('detection rule')}
            value={token}
            onChange={setToken}
            seed={name}
            placeholder={t('ruleTokenPlaceholder')}
          />
        )}
      </FormField>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <FormField label={t('ruleTypePickerLabel')} htmlFor="dr-type" description={t('ruleTypePickerDescription')}>
          <Combobox
            id="dr-type"
            value={ruleType}
            onChange={(v) => {
              const newType = v as RuleType;
              setRuleType(newType);
              // A required-leaf type has no "match every event" mode; if the previous type
              // left the condition at 'none', coerce it back to structured so the editor
              // reappears (otherwise the leaf silently stays empty — Fable H1).
              // eslint-disable-next-line i18next/no-literal-string -- 'structured' is the CondMode discriminant.
              if (conditionRequired(newType) && condMode === 'none') setCondMode('structured');
            }}
            options={ruleTypeOptions(t)}
            allowClear={false}
          />
        </FormField>
        <FormField
          label={t('alarms:colSeverity')}
          htmlFor="dr-severity"
          description={hasRaiseAlarm ? t('ruleSeverityDescriptionRequired') : t('ruleSeverityDescriptionOptional')}
        >
          <Combobox
            id="dr-severity"
            value={severity}
            onChange={setSeverity}
            options={severityOptions(t)}
            placeholder={t('ruleSeverityPlaceholder')}
          />
        </FormField>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} className="h-4 w-4" />
        {t('ruleEnabledCheckboxLabel')}
      </label>

      {/* ADR-062 S4 group scope: pin the rule to a published dynamic entity-group version so it
          fires only for member entities (e.g. an area group for "devices in an arid area"). */}
      <div className="space-y-3 rounded-md border border-border/60 p-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={scoped}
            onChange={(e) => setScoped(e.target.checked)}
            className="h-4 w-4"
          />
          {t('ruleScopeCheckboxLabel')}
        </label>
        {scoped && (
          <div className="space-y-3 pl-6">
            <FormField
              label={t('ruleScopeGroupLabel')}
              htmlFor="dr-scope-group"
              description={t('ruleScopeGroupDescription')}
            >
              <Combobox
                id="dr-scope-group"
                value={scopeGroupToken}
                onChange={(v) => {
                  setScopeGroupToken(v);
                  setScopeGroupVersion(null);
                }}
                options={scopeGroupOptions}
                placeholder={scopeGroups.length === 0 ? t('ruleScopeGroupPlaceholderEmpty') : t('ruleScopeGroupPlaceholderSelect')}
              />
            </FormField>
            {scopeGroupToken && (
              <FormField
                label={t('ruleScopeVersionLabel')}
                htmlFor="dr-scope-version"
                description={t('ruleScopeVersionDescription')}
              >
                <Combobox
                  id="dr-scope-version"
                  value={scopeGroupVersion != null ? String(scopeGroupVersion) : ''}
                  onChange={(v) => setScopeGroupVersion(v ? Number(v) : null)}
                  options={scopeVersionOptions}
                  allowClear={false}
                  placeholder={scopeVersions.length === 0 ? t('ruleScopeVersionPlaceholderEmpty') : t('ruleScopeVersionPlaceholderSelect')}
                />
              </FormField>
            )}
            {scopeGroupToken && scopeVersionsStatus === 'ok' && scopeVersions.length === 0 && (
              <p className="text-sm text-amber-700 dark:text-amber-400">{t('ruleScopeNoPublishedVersion')}</p>
            )}
            {scopeGroupToken && scopeVersionsStatus === 'error' && (
              <p className="text-sm text-amber-700 dark:text-amber-400">{t('ruleScopeVersionsLoadError')}</p>
            )}
            {scopeUnsupportedKind && (
              <p className="text-sm text-amber-700 dark:text-amber-400">{t('ruleScopeUnsupportedKind', { ruleType })}</p>
            )}
            {scopeGroupToken && scopeGroupVersion != null && !scopeUnsupportedKind && (
              <p className="text-sm text-muted-foreground" aria-live="polite">
                {scopeCount.status === 'checking' && t('ruleScopeCounting')}
                {scopeCount.status === 'ok' &&
                  t('ruleScopeMatchCount', { count: scopeCount.total ?? 0, memberType: scopeMemberType })}
                {scopeCount.status === 'error' && t('ruleScopeSelectorUnavailable', { message: scopeCount.message })}
              </p>
            )}
          </div>
        )}
      </div>

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
        <DurationField id="dr-hold" label={t('ruleHoldLabel')} value={holdStr} onChange={setHoldStr} placeholder={t('ruleDurationPlaceholder5m')} />
      )}

      {ruleType === 'absence' && (
        <DurationField id="dr-timeout" label={t('ruleTimeoutLabel')} value={timeoutStr} onChange={setTimeoutStr} placeholder={t('ruleDurationPlaceholder15m')} />
      )}

      {ruleType === 'deltaRate' && (
        <div className="space-y-3">
          <FormField label={t('ruleValueMetricLabel')} htmlFor="dr-value-metric" description={t('ruleValueMetricDescriptionDelta')}>
            <Input
              id="dr-value-metric"
              value={valueMetric}
              onChange={(e) => setValueMetric(e.target.value)}
              placeholder={t('ruleMetricPlaceholder')}
            />
          </FormField>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={rate} onChange={(e) => setRate(e.target.checked)} className="h-4 w-4" />
            {t('ruleRateCheckboxLabel')}
          </label>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[auto_1fr]">
            <FormField label={t('ruleOperatorLabel')} htmlFor="dr-agg-op">
              <Combobox id="dr-agg-op" value={aggOp} onChange={setAggOp} options={orderedOps(t)} allowClear={false} />
            </FormField>
            <FormField label={t('ruleThresholdLabel')} htmlFor="dr-agg-threshold">
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
            <FormField label={t('ruleAggFuncLabel')} htmlFor="dr-agg-func">
              <Combobox id="dr-agg-func" value={aggFunc} onChange={setAggFunc} options={aggFuncOptions(t)} allowClear={false} />
            </FormField>
            <FormField label={t('ruleWindowModeLabel')} htmlFor="dr-window-mode">
              <Combobox
                id="dr-window-mode"
                value={windowMode}
                onChange={setWindowMode}
                options={windowModeOptions(t)}
                allowClear={false}
              />
            </FormField>
          </div>
          {aggNeedsValueMetric && (
            <FormField label={t('ruleValueMetricLabel')} htmlFor="dr-agg-value-metric" description={t('ruleValueMetricDescriptionAgg')}>
              <Input
                id="dr-agg-value-metric"
                value={valueMetric}
                onChange={(e) => setValueMetric(e.target.value)}
                placeholder={t('ruleMetricPlaceholder')}
              />
            </FormField>
          )}
          {(windowMode === 'tumbling' || windowMode === 'sliding') && (
            <DurationField id="dr-agg-window" label={t('ruleWindowLabel')} value={windowStr} onChange={setWindowStr} placeholder={t('ruleDurationPlaceholder5m')} />
          )}
          {windowMode === 'session' && (
            <DurationField id="dr-agg-gap" label={t('ruleSessionGapLabel')} value={gapStr} onChange={setGapStr} placeholder={t('ruleDurationPlaceholder1m')} />
          )}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[auto_1fr]">
            <FormField label={t('ruleOperatorLabel')} htmlFor="dr-agg-op2">
              <Combobox id="dr-agg-op2" value={aggOp} onChange={setAggOp} options={orderedOps(t)} allowClear={false} />
            </FormField>
            <FormField label={t('ruleThresholdLabel')} htmlFor="dr-agg-threshold2">
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
        <DurationField id="dr-rep-window" label={t('ruleWithinWindowLabel')} value={windowStr} onChange={setWindowStr} placeholder={t('ruleDurationPlaceholder10m')} />
      )}

      {ruleType === 'correlation' && (
        <div className="space-y-3">
          <FormField
            label={t('ruleAnchorTypeLabel')}
            htmlFor="dr-anchor"
            description={t('ruleAnchorTypeDescription')}
          >
            <Input id="dr-anchor" value={anchorType} onChange={(e) => setAnchorType(e.target.value)} placeholder={t('ruleAnchorPlaceholder')} />
          </FormField>
          <DurationField id="dr-corr-window" label={t('ruleWithinWindowLabel')} value={windowStr} onChange={setWindowStr} placeholder={t('ruleDurationPlaceholder5m')} />
          <FormField
            label={t('ruleMemberCapLabel')}
            htmlFor="dr-member-cap"
            description={t('ruleMemberCapDescription')}
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
          label={ruleType === 'correlation' ? t('ruleCountLabelCorrelation') : t('ruleCountLabelDefault')}
          htmlFor="dr-count"
          description={
            ruleType === 'correlation'
              ? t('ruleCountDescriptionCorrelation')
              : ruleType === 'repeating'
                ? t('ruleCountDescriptionRepeating')
                : t('ruleCountDescriptionAggregate')
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
        <p className="rounded-md border border-dashed px-3 py-3 text-sm text-muted-foreground">{t('ruleActionsForbiddenNote')}</p>
      ) : (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm font-medium">{t('ruleActionsSectionTitle')}</p>
              <p className="text-sm text-muted-foreground">{t('ruleActionsSectionDescription')}</p>
            </div>
            <Button size="sm" variant="outline" onClick={addAction} disabled={actions.length >= 8}>
              {t('ruleAddActionButton')}
            </Button>
          </div>
          {actions.map((a, i) => (
            <div key={i} className="space-y-3 rounded-md border p-3">
              <div className="flex items-center justify-between gap-2">
                <FormField label={t('ruleActionNumberLabel', { number: i + 1 })} htmlFor={`dr-action-${i}`}>
                  {a.raw ? (
                    // An outbound action (httpCall / publish) is Canvas-authored; the form shows it
                    // read-only and preserves it. No Combobox — it can't be retyped here.
                    <Input
                      id={`dr-action-${i}`}
                      value={a.type === 'publish' ? t('ruleActionKindPublishReadonly') : t('ruleActionKindHttpCallReadonly')}
                      readOnly
                      disabled
                    />
                  ) : (
                    <Combobox
                      id={`dr-action-${i}`}
                      value={a.type}
                      onChange={(v) => setActionAt(i, { type: v as ActionKind })}
                      // 'raiseAlarm'/'sendCommand' are the ActionKind discriminant, never user
                      // text — only the labels (translated above) are display text.
                      /* eslint-disable i18next/no-literal-string */
                      options={[
                        { value: 'raiseAlarm', label: t('ruleActionKindRaiseAlarmLabel') },
                        { value: 'sendCommand', label: t('ruleActionKindSendCommandLabel') },
                      ]}
                      /* eslint-enable i18next/no-literal-string */
                      allowClear={false}
                    />
                  )}
                </FormField>
                <Button size="sm" variant="ghost" onClick={() => removeAction(i)}>
                  {t('ruleRemoveActionButton')}
                </Button>
              </div>
              {a.guard && (
                <p className="rounded-md border border-dashed bg-muted/30 px-2 py-1.5 text-xs text-muted-foreground">
                  <Trans
                    t={t}
                    i18nKey="ruleActionGuardNote"
                    values={{ guard: a.guard }}
                    components={{ mono: <span className="font-mono" /> }}
                  />
                </p>
              )}
              {a.raw ? (
                <p className="rounded-md border border-dashed bg-muted/30 px-2 py-1.5 text-xs text-muted-foreground">
                  {t('ruleOutboundActionNote', {
                    kind: a.type === 'publish' ? t('ruleOutboundKindPublish') : t('ruleOutboundKindWebhook'),
                  })}
                </p>
              ) : a.type === 'raiseAlarm' ? (
                <FormField
                  label={t('ruleAlarmKeyLabel')}
                  htmlFor={`dr-alarm-key-${i}`}
                  description={t('ruleAlarmKeyDescription')}
                >
                  <Input
                    id={`dr-alarm-key-${i}`}
                    value={a.alarmKey}
                    onChange={(e) => setActionAt(i, { alarmKey: e.target.value })}
                    placeholder={t('ruleAlarmKeyPlaceholder')}
                  />
                </FormField>
              ) : (
                <div className="space-y-3">
                  <FormField label={t('ruleCommandLabel')} htmlFor={`dr-command-${i}`} description={t('ruleCommandDescription')}>
                    <Input
                      id={`dr-command-${i}`}
                      value={a.command}
                      onChange={(e) => setActionAt(i, { command: e.target.value })}
                      placeholder={t('ruleCommandPlaceholder')}
                    />
                  </FormField>
                  <FormField
                    label={t('rulePayloadLabel')}
                    htmlFor={`dr-payload-${i}`}
                    description={t('rulePayloadDescription')}
                  >
                    <Textarea
                      id={`dr-payload-${i}`}
                      value={a.payload}
                      onChange={(e) => setActionAt(i, { payload: e.target.value })}
                      // A JSON payload example, not prose — technical syntax, never translated.
                      // eslint-disable-next-line i18next/no-literal-string
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

      {(hint || scopeHint) && <p className="text-sm text-amber-600 dark:text-amber-500">{hint ?? scopeHint}</p>}

      {/* Inline compiler feedback (advisory — does not block the draft save). */}
      {!hint && !scopeHint && validation?.status === 'checking' && (
        <p className="text-sm text-muted-foreground">{t('ruleCheckingCompiles')}</p>
      )}
      {!hint && !scopeHint && validation?.status === 'error' && (
        <p className="text-sm text-red-600 dark:text-red-500">{t('ruleCompilerError', { message: validation.message })}</p>
      )}
      {!hint && !scopeHint && validation?.status === 'ok' && (
        <p className="text-sm text-emerald-600 dark:text-emerald-500">{t('ruleCompilesOk')}</p>
      )}

      <div className="flex gap-2 pt-1">
        <Button onClick={submit} loading={busy} disabled={busy || hint != null || scopeHint != null}>
          {editing ? t('common:saveChanges') : t('ruleCreateButton')}
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
  // An outbound action (httpCall / publish) the form doesn't edit is emitted verbatim from the
  // wire object captured at load (guard already inside it), so a canvas rule round-trips losslessly.
  if (a.raw) return a.raw;
  // Preserve a canvas-authored guard verbatim (the form cannot edit it, but must not drop it).
  const withGuard = (o: Record<string, unknown>): Record<string, unknown> => (a.guard ? { ...o, guard: a.guard } : o);
  if (a.type === 'sendCommand') {
    const sc: Record<string, unknown> = { command: a.command.trim() };
    if (a.payload.trim()) sc.payload = a.payload;
    return withGuard({ type: 'sendCommand', sendCommand: sc });
  }
  const ra: Record<string, unknown> = {};
  if (a.alarmKey.trim()) ra.alarmKey = a.alarmKey.trim();
  return withGuard({ type: 'raiseAlarm', raiseAlarm: ra });
}

function numOrZero(s: string): number {
  const n = Number(s);
  return Number.isFinite(n) ? n : 0;
}
function intOrZero(s: string): number {
  // Number (not parseInt) so this agrees with the hint's isPosInt check: parseInt("1e2")
  // is 1 but Number("1e2") is 100 — a mismatch would let the hint pass a value the emit
  // then corrupts (Fable L1). A non-integer is caught by the hint before it reaches here.
  const n = Number(s);
  return Number.isInteger(n) ? n : 0;
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
    const wireType = str(act.type);
    // Outbound actions (ADR-060) are canvas-authored; carry them through verbatim so a save
    // from the form doesn't rewrite them as a raiseAlarm (which the fallback below would do).
    if (wireType === 'httpCall' || wireType === 'publish') {
      return { type: wireType, alarmKey: '', command: '', payload: '', raw: act };
    }
    const t = wireType === 'sendCommand' ? 'sendCommand' : 'raiseAlarm';
    const ra = (act.raiseAlarm ?? {}) as Record<string, unknown>;
    const sc = (act.sendCommand ?? {}) as Record<string, unknown>;
    return {
      type: t as ActionKind,
      alarmKey: str(ra.alarmKey),
      command: str(sc.command),
      payload: str(sc.payload),
      guard: str(act.guard) || undefined,
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
  t: TFunction;
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
  // A non-empty, non-negative integer / finite number, matching what the emit (intOrZero /
  // numOrZero) will faithfully serialize — so the hint never passes a value the emit corrupts.
  const posInt = (s: string) => Number.isInteger(Number(s)) && Number(s) > 0;
  const finite = (s: string) => s.trim() !== '' && Number.isFinite(Number(s));

  const { t } = a;
  if (!a.editing && !a.token.trim()) return t('ruleHintTokenRequired');
  if (!a.definitionName) return t('ruleHintNameRequired');

  // Condition. Absence forbids a leaf entirely; for every other type a chosen structured or
  // CEL condition must be complete. A required-leaf type (threshold/duration) has no
  // "match every event" mode — so 'none' there is a missing condition, not a valid choice
  // (Fable H1: a type switch could leave the mode at 'none' with the editor hidden).
  if (!conditionForbidden(a.ruleType)) {
    if (conditionRequired(a.ruleType) && a.condMode === 'none') return t('ruleHintConditionRequired');
    if (a.condMode === 'structured') {
      if (!a.condMetric.trim()) return t('ruleHintConditionMetric');
      if (a.boundKind === 'literal' && !finite(a.condThreshold)) return t('ruleHintConditionThreshold');
      if (a.boundKind === 'attr' && !a.condAttr.trim()) return t('ruleHintConditionThresholdAttr');
    }
    if (a.condMode === 'cel' && !a.cel.trim()) return t('ruleHintCelEmpty');
  }

  switch (a.ruleType) {
    case 'duration':
      if (!a.holdStr.trim()) return t('ruleHintDurationHold');
      break;
    case 'absence':
      if (!a.timeoutStr.trim()) return t('ruleHintAbsenceTimeout');
      break;
    case 'repeating':
      if (!posInt(a.countStr)) return t('ruleHintRepeatingCount');
      if (!a.windowStr.trim()) return t('ruleHintRepeatingWindow');
      break;
    case 'deltaRate':
      if (!a.valueMetric.trim()) return t('ruleHintDeltaMetric');
      if (!finite(a.aggThreshold)) return t('ruleHintDeltaThreshold');
      break;
    case 'aggregate':
      if (a.aggFunc !== 'count' && !a.valueMetric.trim()) return t('ruleHintAggMetric');
      if (!finite(a.aggThreshold)) return t('ruleHintAggThreshold');
      if ((a.windowMode === 'tumbling' || a.windowMode === 'sliding') && !a.windowStr.trim())
        return t('ruleHintAggWindow');
      if (a.windowMode === 'session' && !a.gapStr.trim()) return t('ruleHintAggGap');
      if (a.windowMode === 'count' && !posInt(a.countStr)) return t('ruleHintAggCount');
      break;
    case 'correlation':
      if (!a.anchorType.trim()) return t('ruleHintCorrAnchor');
      if (!posInt(a.countStr)) return t('ruleHintCorrCount');
      if (!a.windowStr.trim()) return t('ruleHintCorrWindow');
      break;
  }

  // Actions are neither emitted nor rendered for correlation, so don't validate the stale
  // rows a user may have left before switching to it (Fable M1 — an unremovable hidden row
  // would otherwise block a valid rule).
  if (!actionsForbidden(a.ruleType)) {
    // A raiseAlarm action requires a severity tier.
    if (a.hasRaiseAlarm && !a.severity) return t('ruleHintRaiseAlarmSeverity');
    for (const act of a.actions) {
      if (act.type === 'sendCommand' && !act.command.trim()) return t('ruleHintSendCommandNeedsCommand');
    }
  }
  return null;
}
