// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The detection-rule MODEL: the taxonomy, the per-type authoring shape, and the two halves of
// the round trip between a stored rules.Rule definition and the fields a form edits.
//
// 🔴 IT LIVES IN ITS OWN MODULE BECAUSE IT IS NOT A COMPONENT'S BUSINESS. All of this used to
// sit inside DetectionRuleForm.tsx and was exported from there purely so tests could reach it —
// which meant a test that wanted to read one const array pulled in a route component's entire
// dependency graph, and the canvas editor could not share the taxonomy at all without depending
// on a route component. Exporting internals so tests can see them is a sign the thing wanted to
// be somewhere else; this is that somewhere else.
//
// It holds no JSX, no hooks, and no translation calls: the option lists a picker renders are
// presentation and stay with the form. What is here is the part with a right answer.

// ── The rule taxonomy ──────────────────────────────────────────────────────

// 🔴 ONE LIST, AND THE OTHER THREE DERIVE FROM IT. This file used to carry the taxonomy
// three times — the union type, `ruleTypeOptions`, and `KNOWN_TYPES` down in
// `parseDefinition` — and nothing made them agree. `KNOWN_TYPES` was the one that decided
// whether a stored rule was UNDERSTOOD, so when it went stale the form silently relabelled
// a real rule as a threshold; the other two copies were the ones a reviewer would notice.
// That is how `connectivity` came to be a kind the compiler has always accepted and the
// console could not open, and it is why adding it to the picker alone would not have fixed
// anything.
//
// `as const` + an indexed access type means a fourth copy cannot be added without deleting
// this one. The picker's option list, which lives with the form because it needs translation,
// is a plain map over this array — so it cannot fall behind by construction rather than by
// anybody remembering.
export const RULE_TYPES = [
  'threshold',
  'duration',
  'absence',
  'repeating',
  'deltaRate',
  'aggregate',
  'correlation',
  // A leaf-less, config-less EDGE trigger: the presence state change IS the signal, so the
  // compiler forbids every authorable field for it. It is therefore the SIMPLEST kind in the
  // taxonomy, not a complicated one — the reason it was missing is that nothing checked.
  'connectivity',
] as const;

export type RuleType = (typeof RULE_TYPES)[number];

// The i18n key stem for a kind: `deltaRate` → `ruleTypeDeltaRateLabel`. Derived rather than
// tabulated — a `Record<RuleType, string>` mapping each kind to its own capitalized name was
// eight lines that could only ever say the same thing this expression says, and its one
// benefit (a compile error on a new kind) bought nothing but a mechanical entry.
//
// 🔴 WHAT ACTUALLY NEEDS ENFORCING IS THAT THE KEY RESOLVES, and neither form did that. The
// taxonomy lockstep test now checks every stem against both locale catalogs, which catches
// the failure that matters — a kind whose picker row renders its own key as its label.
export const ruleTypeKey = (value: RuleType): string => value[0].toUpperCase() + value.slice(1);

// Condition editors: a required-leaf type offers structured|cel; an optional-leaf type
// also offers "match every event"; absence takes no leaf at all.
export type CondMode = 'structured' | 'cel' | 'none';
export type BoundKind = 'literal' | 'attr';
export type ActionKind = 'raiseAlarm' | 'sendCommand' | 'httpCall' | 'publish';

export interface ActionRow {
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
// isKnownRuleType narrows an arbitrary stored `type` against the ONE taxonomy. A type
// guard rather than an `includes` at the call site, so the narrowing is the compiler's and
// no cast is needed to use the result.
export function isKnownRuleType(v: unknown): v is RuleType {
  return typeof v === 'string' && (RULE_TYPES as readonly string[]).includes(v);
}

export const conditionRequired = (t: RuleType) => t === 'threshold' || t === 'duration';
// absence is timer-driven off the roster; connectivity reads the typed presence edge and
// never evaluates a leaf at all (compileConnectivity forbids every authorable field). A leaf
// offered for either would be authored, saved, and silently do nothing.
export const conditionForbidden = (t: RuleType) => t === 'absence' || t === 'connectivity';
export const actionsForbidden = (t: RuleType) => t === 'correlation'; // its series is an area anchor, not a device

// Whether a group scope cannot apply to this kind. Absence is timer-driven off the roster
// and correlation is anchor-keyed, so neither is device-keyed enough for a group to select.
//
// 🔴 CONNECTIVITY IS DELIBERATELY NOT IN THIS SET, and that was checked against the gate
// that enforces it rather than inferred from the shape: the publish resolver refuses a
// scope on exactly {absence, correlation}, so a scoped connectivity rule is accepted. Adding
// it here "to be safe" would withhold a legal scope and show the author a refusal the
// server would never make. The set was also written out twice in this file; one predicate
// now, because the two copies were a divergence waiting for a third kind.
export const scopeUnsupported = (t: RuleType) => t === 'absence' || t === 'correlation';

/**
 * The compiler's ceiling on one rule's action chain (rules.MaxActionsPerRule).
 *
 * Mirrored so the Add button disables at the limit instead of letting an author write a ninth
 * action and meet the refusal at publish. It DECIDES NOTHING — the compiler re-checks and the
 * compiler wins — but a stale copy still misinforms at the moment of authoring, which is why
 * the taxonomy lockstep test pins it against the Go const. It was previously a bare `8` inline
 * on the button, linked to the backend by nothing at all.
 */
export const MAX_ACTIONS_PER_RULE = 8;

// ── Serialization: form state → rules.Rule JSON ────────────────────────────

export interface BuildArgs {
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
export function buildDefinition(a: BuildArgs): string {
  const def: Record<string, unknown> = { name: a.definitionName, type: a.ruleType };
  if (a.description.trim()) def.description = a.description.trim();
  if (a.severity) def.severity = a.severity;

  const when = buildWhen(a);
  if (when) def.when = when;

  switch (a.ruleType) {
    case 'threshold':
      break;
    // Every authorable field is forbidden by the compiler for this kind — the presence edge
    // IS the signal — so it emits nothing at all beyond the header and its actions.
    case 'connectivity':
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

export interface ParsedDefinition {
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


// rebuildFrom re-emits what this form WOULD save for a definition it has just read, with no
// operator edit in between. It is the second half of the round-trip whose first half is
// parseDefinition, and it exists only to be compared against the stored bytes.
//
// The mapping is 1:1 with ParsedDefinition modulo the form-state field names, which is the
// point: if the two shapes ever diverge, this stops compiling rather than quietly measuring
// the wrong thing.
export function rebuildFrom(p: ParsedDefinition): string {
  return buildDefinition({
    definitionName: p.name,
    description: p.description,
    severity: p.severity,
    ruleType: p.type,
    condMode: p.condMode,
    condMetric: p.condMetric,
    condOp: p.condOp,
    boundKind: p.boundKind,
    condThreshold: p.condThreshold,
    condAttr: p.condAttr,
    cel: p.cel,
    valueMetric: p.valueMetric,
    aggFunc: p.aggFunc,
    windowMode: p.windowMode,
    windowStr: p.window,
    holdStr: p.hold,
    timeoutStr: p.timeout,
    gapStr: p.gap,
    countStr: p.count,
    rate: p.rate,
    aggOp: p.aggOp,
    aggThreshold: p.aggThreshold,
    anchorType: p.anchorType,
    memberCapStr: p.memberCap,
    actions: p.actions,
  });
}

// Reads a stored definition into form state. It is defensive (a hand- or API-authored rule
// may carry shapes the form does not model): anything unreadable falls back to a sensible
// default so the drawer always opens; the compiler re-validates on the next publish.
export function parseDefinition(raw: string): ParsedDefinition | null {
  let d: Record<string, unknown>;
  try {
    const parsed: unknown = JSON.parse(raw);
    // 🔴 `null` AND `[1,2]` ARE WELL-FORMED JSON, and the draft-save path admits anything
    // well-formed. Only a THROW was treated as unreadable, so a definition of literal `null`
    // got past this and died on the first property read — crashing the drawer instead of
    // showing the warning built for exactly this case.
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    d = parsed as Record<string, unknown>;
  } catch {
    return null;
  }
  const str = (v: unknown): string => (typeof v === 'string' ? v : '');
  const numStr = (v: unknown): string => (typeof v === 'number' ? String(v) : '');
  // 🔴 THE FALLBACK IS STILL HERE, AND IT IS STILL A RELABEL. A type this form does not
  // know becomes a threshold on screen, which is a lie about a rule that exists. What has
  // changed is that it can no longer happen SILENTLY: `isKnownRuleType` reads the single
  // taxonomy above, and the caller compares the rebuilt definition with the stored one and
  // warns when they differ (see `lossyOpen`). The relabel is kept rather than refused
  // because the drawer must still open — an operator has to be able to read and rename a
  // rule the form cannot fully model.
  const type = isKnownRuleType(d.type) ? d.type : 'threshold';

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

