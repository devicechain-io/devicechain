// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reverse round-trip (ADR-053 §6): synthesize a canvas graph from a form-authored (or
// canvas-less) rules.Rule definition so ANY detection rule opens on the canvas. It is a pure,
// deterministic frontend synthesis — one Source + one condition + the rule's actions — with NO
// server call. The forward direction (canvas → definition) is server-authoritative
// (compileCanvas); this is only for re-opening a rule the canvas did not author.
//
// It is the inverse of the Go graph lowering, so it must read the rules.Rule wire form exactly:
// durations are Go duration strings ("10m0s"), the leaf bound is a literal `threshold` number
// or a `thresholdAttr` string, and operator/agg/mode values are the rules-native tokens.

import {
  CANVAS_SCHEMA_VERSION,
  isConditionType,
  type ActionConfig,
  type CanvasDefinition,
  type CanvasNode,
  type Leaf,
  type NodeType,
  endpoint,
} from './model';

// parseGoDuration converts a Go duration string ("1h30m", "600ms", "5m0s", "0s") to
// milliseconds, or null if it is not a well-formed Go duration. It accepts the unit set Go
// emits (ns, us/µs, ms, s, m, h) and a leading sign; an empty string is 0 (Go marshals a zero
// Duration as "0s", but the schema also treats "" as zero).
export function parseGoDuration(s: string): number | null {
  if (s === '' || s === '0' || s === '0s') return 0;
  const unitMs: Record<string, number> = {
    ns: 1e-6,
    us: 1e-3,
    'µs': 1e-3,
    'μs': 1e-3,
    ms: 1,
    s: 1000,
    m: 60_000,
    h: 3_600_000,
  };
  let rest = s;
  let sign = 1;
  if (rest.startsWith('-')) {
    sign = -1;
    rest = rest.slice(1);
  } else if (rest.startsWith('+')) {
    rest = rest.slice(1);
  }
  if (rest === '') return null;
  let total = 0;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)/g;
  let consumed = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(rest)) !== null) {
    if (match.index !== consumed) return null; // a gap means an unparseable token
    total += parseFloat(match[1]) * unitMs[match[2]];
    consumed = re.lastIndex;
  }
  if (consumed !== rest.length) return null; // trailing junk
  // Round to whole milliseconds: float accumulation yields e.g. 8000.999999999999 for "8.001s",
  // and the Go config fields are int64 — a fractional value would fail to decode. Millisecond
  // precision is the canvas's declared floor; genuinely sub-ms durations round to the nearest ms.
  return sign * Math.round(total);
}

// The subset of the rules.Rule wire shape the synthesis reads.
interface WireCondition {
  metric?: string;
  op?: string;
  threshold?: number;
  thresholdAttr?: string;
  cel?: string;
}
interface WireAction {
  type?: string;
  raiseAlarm?: { alarmKey?: string };
  sendCommand?: { command?: string; payload?: string };
  // The per-action REACT guard (slice 9c). When present, the synthesis inserts a branch node
  // carrying it between the condition and this action, so a guarded rule re-opens on the canvas.
  guard?: string;
}
interface WireRule {
  name?: string;
  description?: string;
  type?: string;
  severity?: string;
  actions?: WireAction[];
  when?: WireCondition;
  metric?: string;
  window?: string;
  hold?: string;
  timeout?: string;
  gap?: string;
  count?: number;
  rate?: boolean;
  windowMode?: string;
  agg?: string;
  op?: string;
  threshold?: number;
  anchorType?: string;
  memberCap?: number;
}

// leafFromWire maps a wire `when` to the canvas Leaf (bound literal XOR attribute). An absent
// or empty leaf yields undefined (the "match every event" / absence case).
function leafFromWire(w: WireCondition | undefined): Leaf | undefined {
  if (!w) return undefined;
  const leaf: Leaf = {};
  if (w.metric) leaf.metric = w.metric;
  if (w.op) leaf.op = w.op as Leaf['op'];
  if (w.thresholdAttr) leaf.threshold = { kind: 'attribute', attribute: w.thresholdAttr };
  else if (typeof w.threshold === 'number') leaf.threshold = { kind: 'literal', value: w.threshold };
  if (w.cel) leaf.cel = w.cel;
  return Object.keys(leaf).length === 0 ? undefined : leaf;
}

// conditionConfig builds a condition node's config from the wire rule, per type — the exact
// inverse of the Go `build()` methods (durations Go-string→ms; only the fields the type uses).
function conditionConfig(rule: WireRule): Record<string, unknown> {
  const meta: Record<string, unknown> = {};
  if (rule.name) meta.name = rule.name;
  if (rule.description) meta.description = rule.description;
  if (rule.severity) meta.severity = rule.severity;
  const when = leafFromWire(rule.when);
  const durMs = (s: string | undefined): number => parseGoDuration(s ?? '') ?? 0;

  switch (rule.type as NodeType) {
    case 'threshold':
      return { ...meta, when: when ?? {} };
    case 'duration':
      return { ...meta, when: when ?? {}, holdMs: durMs(rule.hold) };
    case 'absence':
      return { ...meta, timeoutMs: durMs(rule.timeout) };
    case 'aggregate': {
      const cfg: Record<string, unknown> = {
        ...meta,
        agg: rule.agg,
        windowMode: rule.windowMode,
        op: rule.op,
        threshold: rule.threshold ?? 0,
      };
      if (rule.metric) cfg.metric = rule.metric;
      if (rule.window) cfg.windowMs = durMs(rule.window);
      if (rule.gap) cfg.gapMs = durMs(rule.gap);
      if (typeof rule.count === 'number') cfg.count = rule.count;
      if (when) cfg.when = when;
      return cfg;
    }
    case 'deltaRate': {
      const cfg: Record<string, unknown> = { ...meta, metric: rule.metric ?? '', op: rule.op, threshold: rule.threshold ?? 0 };
      if (rule.rate) cfg.rate = true;
      if (when) cfg.when = when;
      return cfg;
    }
    case 'repeating': {
      const cfg: Record<string, unknown> = { ...meta, count: rule.count ?? 0, windowMs: durMs(rule.window) };
      if (when) cfg.when = when;
      return cfg;
    }
    case 'correlation': {
      const cfg: Record<string, unknown> = { ...meta, anchorType: rule.anchorType ?? '', count: rule.count ?? 0, windowMs: durMs(rule.window) };
      if (typeof rule.memberCap === 'number') cfg.memberCap = rule.memberCap;
      if (when) cfg.when = when;
      return cfg;
    }
    default:
      return { ...meta };
  }
}

// actionConfig maps a wire action to a canvas action node config.
function actionConfig(a: WireAction): ActionConfig | null {
  if (a.type === 'raiseAlarm') {
    const cfg: ActionConfig = { action: 'raiseAlarm' };
    if (a.raiseAlarm?.alarmKey) cfg.alarmKey = a.raiseAlarm.alarmKey;
    return cfg;
  }
  if (a.type === 'sendCommand') {
    const cfg: ActionConfig = { action: 'sendCommand', command: a.sendCommand?.command ?? '' };
    if (a.sendCommand?.payload) cfg.payload = a.sendCommand.payload;
    return cfg;
  }
  return null;
}

// Layout constants — authoring-only coordinates (the compiler ignores `ui`). A branch lane sits
// between the condition and the action it gates.
const COL_X = { source: 40, condition: 320, branch: 500, action: 700 };
const ACTION_DY = 120;

export interface SynthesisResult {
  graph: CanvasDefinition | null;
  // A human-readable reason the rule could not be laid out on the canvas (unparseable JSON, an
  // unknown rule type). The editor falls back to the form when this is set.
  error?: string;
}

// graphFromDefinition synthesizes a canvas graph from a rules.Rule JSON definition: one
// profile-scoped Source, one condition node of the rule's type, and one Action node per
// declared action, wired source→condition→action. Deterministic and pure.
export function graphFromDefinition(definition: string, profileToken: string): SynthesisResult {
  let rule: WireRule;
  try {
    rule = JSON.parse(definition) as WireRule;
  } catch {
    return { graph: null, error: 'The rule definition is not valid JSON.' };
  }
  if (!rule || typeof rule !== 'object') {
    return { graph: null, error: 'The rule definition is not a rule object.' };
  }
  const type = rule.type as NodeType;
  if (!type || !isConditionType(type)) {
    return { graph: null, error: `This rule type (${rule.type ?? 'unknown'}) cannot be shown on the canvas.` };
  }

  const nodes: CanvasNode[] = [];
  const edges: CanvasDefinition['edges'] = [];

  const sourceId = 'source';
  nodes.push({
    id: sourceId,
    type: 'source',
    config: { scope: { kind: 'profile', profileToken } },
    ui: { x: COL_X.source, y: 120 },
  });

  const condId = 'condition';
  nodes.push({ id: condId, type, config: conditionConfig(rule), ui: { x: COL_X.condition, y: 120 } });
  edges.push({ from: endpoint(sourceId, 'out'), to: endpoint(condId, 'in') });

  const actions = Array.isArray(rule.actions) ? rule.actions : [];
  actions.forEach((a, i) => {
    const cfg = actionConfig(a);
    if (!cfg) return; // an unknown action type is dropped from the layout; publish still uses the stored definition
    const id = `action-${i}`;
    const y = 120 + i * ACTION_DY;
    nodes.push({ id, type: 'action', config: cfg as unknown as Record<string, unknown>, ui: { x: COL_X.action, y } });
    if (a.guard) {
      // A guarded action re-opens as condition → branch(guard) → action. The forward lowering
      // re-composes this exact Action.Guard from the branch predicate, so it round-trips to the
      // same bytes (a composed `(x) && (y)` guard round-trips as one branch carrying that whole
      // string, which is faithful — the canvas simply shows it as a single route).
      const bid = `branch-${i}`;
      nodes.push({ id: bid, type: 'branch', config: { when: a.guard }, ui: { x: COL_X.branch, y } });
      edges.push({ from: endpoint(condId, 'signal'), to: endpoint(bid, 'in') });
      edges.push({ from: endpoint(bid, 'out'), to: endpoint(id, 'in') });
    } else {
      edges.push({ from: endpoint(condId, 'signal'), to: endpoint(id, 'in') });
    }
  });

  return { graph: { schemaVersion: CANVAS_SCHEMA_VERSION, nodes, edges } };
}

// buildCanvasDefinition wraps the editor's node/edge lists into the versioned wire document
// sent to compileCanvas and stored as the AuthoringGraph sidecar.
export function buildCanvasDefinition(nodes: CanvasNode[], edges: CanvasDefinition['edges']): CanvasDefinition {
  return { schemaVersion: CANVAS_SCHEMA_VERSION, nodes, edges };
}
