// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console-side model of the visual automation canvas (ADR-053 slice 9b). These types
// MIRROR the event-processing `internal/rules/graph` Go package exactly — the wire form the
// server-authoritative compiler (compileCanvas) decodes with unknown-field rejection — so a
// key or vocabulary drift here becomes a compile diagnostic, never a silent mis-lowering. The
// browser only ever holds this graph and shows diagnostics; it NEVER authors the rules.Rule
// definition directly (compileCanvas lowers the graph server-side). Operator / aggregate /
// window-mode values are the rules-native tokens (gt/ge/…), not display symbols; durations
// cross the wire as integer milliseconds.

export const CANVAS_SCHEMA_VERSION = 1;

// The three typed port signals. Edges may only join same-typed ports; a condition node is the
// only node with a stream input and a signal output, which is what makes the DETECT↔REACT
// boundary checkable. `value` is reserved for the future compute node.
export type PortType = 'stream' | 'signal' | 'value';

export type NodeType =
  | 'source'
  | 'threshold'
  | 'duration'
  | 'absence'
  | 'aggregate'
  | 'deltaRate'
  | 'repeating'
  | 'correlation'
  | 'branch'
  | 'action';

export type NodeCategory = 'source' | 'condition' | 'branch' | 'action';

// A node's typed ports + its category, keyed by port name — the client-side twin of the Go
// `catalog`. Used to validate a connection before the server re-checks it, and to render ports.
export interface NodeSpec {
  category: NodeCategory;
  label: string;
  in: Record<string, PortType>;
  out: Record<string, PortType>;
}

export const NODE_CATALOG: Record<NodeType, NodeSpec> = {
  source: { category: 'source', label: 'Source', in: {}, out: { out: 'stream' } },
  threshold: { category: 'condition', label: 'Threshold', in: { in: 'stream' }, out: { signal: 'signal' } },
  duration: { category: 'condition', label: 'Duration', in: { in: 'stream' }, out: { signal: 'signal' } },
  absence: { category: 'condition', label: 'Absence', in: { in: 'stream' }, out: { signal: 'signal' } },
  aggregate: { category: 'condition', label: 'Windowed aggregate', in: { in: 'stream' }, out: { signal: 'signal' } },
  deltaRate: { category: 'condition', label: 'Rate of change', in: { in: 'stream' }, out: { signal: 'signal' } },
  repeating: { category: 'condition', label: 'Repeating', in: { in: 'stream' }, out: { signal: 'signal' } },
  correlation: { category: 'condition', label: 'Area correlation', in: { in: 'stream' }, out: { signal: 'signal' } },
  branch: { category: 'branch', label: 'Branch', in: { in: 'signal' }, out: { out: 'signal' } },
  action: { category: 'action', label: 'Action', in: { in: 'signal' }, out: {} },
};

export const CONDITION_TYPES: NodeType[] = [
  'threshold',
  'duration',
  'absence',
  'aggregate',
  'deltaRate',
  'repeating',
  'correlation',
];

export const isConditionType = (t: NodeType): boolean => NODE_CATALOG[t]?.category === 'condition';

// ── Compile-target vocabularies (rules-native tokens) ─────────────────────
export type CompareOp = 'gt' | 'ge' | 'lt' | 'le' | 'eq' | 'ne';
export type AggFunc = 'count' | 'sum' | 'avg' | 'min' | 'max';
export type WindowMode = 'tumbling' | 'sliding' | 'session' | 'count';
export type Severity = 'critical' | 'major' | 'minor' | 'warning' | 'indeterminate';
export type ActionKind = 'raiseAlarm' | 'sendCommand';

export const COMPARE_OPS: CompareOp[] = ['gt', 'ge', 'lt', 'le', 'eq', 'ne'];
export const AGG_FUNCS: AggFunc[] = ['count', 'sum', 'avg', 'min', 'max'];
export const WINDOW_MODES: WindowMode[] = ['tumbling', 'sliding', 'session', 'count'];
export const SEVERITIES: Severity[] = ['critical', 'major', 'minor', 'warning', 'indeterminate'];

// Display-only glyphs for the ordered/equality operators (the wire form stays the token).
export const OP_SYMBOL: Record<CompareOp, string> = {
  gt: '>',
  ge: '≥',
  lt: '<',
  le: '≤',
  eq: '=',
  ne: '≠',
};

// ── Node config shapes (mirror the Go per-node config structs) ────────────

// A comparison bound: literal value XOR device-attribute reference.
export type Bound =
  | { kind: 'literal'; value: number }
  | { kind: 'attribute'; attribute: string };

// A predicate leaf, projecting onto rules.Condition. Structured (metric·op·bound) XOR raw CEL
// XOR empty (match-every). The compiler is the authority on coherence.
export interface Leaf {
  metric?: string;
  op?: CompareOp;
  threshold?: Bound;
  cel?: string;
}

export interface RuleMeta {
  name?: string;
  description?: string;
  severity?: Severity;
}

export interface SourceConfig {
  scope: { kind: 'profile'; profileToken: string };
  metricFilter?: string[];
}

export type ThresholdConfig = RuleMeta & { when: Leaf };
export type DurationConfig = RuleMeta & { when: Leaf; holdMs: number };
export type AbsenceConfig = RuleMeta & { timeoutMs: number };
export type AggregateConfig = RuleMeta & {
  agg: AggFunc;
  windowMode: WindowMode;
  metric?: string;
  windowMs?: number;
  gapMs?: number;
  count?: number;
  op: CompareOp;
  threshold: number;
  when?: Leaf;
};
export type DeltaRateConfig = RuleMeta & {
  metric: string;
  rate?: boolean;
  op: CompareOp;
  threshold: number;
  when?: Leaf;
};
export type RepeatingConfig = RuleMeta & { when?: Leaf; count: number; windowMs: number };
export type CorrelationConfig = RuleMeta & {
  anchorType: string;
  count: number;
  windowMs: number;
  memberCap?: number;
  when?: Leaf;
};
export interface ActionConfig {
  action: ActionKind;
  alarmKey?: string;
  command?: string;
  payload?: string;
}

// A REACT branch node (slice 9c): a signal→signal router carrying one CEL boolean (`when`) that
// gates every action downstream of it. `when` is evaluated over the DERIVED-event vocabulary
// (value / hasValue / series) — NOT the resolved-event map — because a guard runs after detection.
// The lowering folds it onto each downstream action's guard; the compiler cost-gates it.
export type BranchConfig = { name?: string; when: string };

// The config carried on a node is opaque per-type JSON; the compiler validates it. We keep it
// loosely typed here (Record) because the editor mutates fields incrementally, and narrow at
// the config-panel boundary.
export type NodeConfig = Record<string, unknown>;

export interface CanvasNode {
  id: string;
  type: NodeType;
  config: NodeConfig;
  ui?: { x: number; y: number };
}

export interface CanvasEdge {
  from: string; // "nodeId:port"
  to: string; // "nodeId:port"
}

export interface CanvasDefinition {
  schemaVersion: number;
  nodes: CanvasNode[];
  edges: CanvasEdge[];
}

// endpoint composes a "nodeId:port" edge endpoint (matches the Go last-colon parse).
export const endpoint = (nodeId: string, port: string): string => `${nodeId}:${port}`;

// portTypeOf resolves a node's port type for a direction, or null if the port is unknown —
// the client-side connect guard (the server re-validates authoritatively).
export function portTypeOf(nodeType: NodeType, port: string, output: boolean): PortType | null {
  const spec = NODE_CATALOG[nodeType];
  if (!spec) return null;
  const m = output ? spec.out : spec.in;
  return m[port] ?? null;
}

// defaultConfig returns a fresh, minimally-valid-ish config for a newly-dropped node. It is a
// starting point the author fills in; the compiler reports what is still missing.
export function defaultConfig(type: NodeType, profileToken: string): NodeConfig {
  switch (type) {
    case 'source':
      return { scope: { kind: 'profile', profileToken } } satisfies SourceConfig as NodeConfig;
    case 'threshold':
      return { name: '', when: { metric: '', op: 'gt', threshold: { kind: 'literal', value: 0 } } };
    case 'duration':
      return { name: '', when: { metric: '', op: 'gt', threshold: { kind: 'literal', value: 0 } }, holdMs: 60000 };
    case 'absence':
      return { name: '', timeoutMs: 300000 };
    case 'aggregate':
      return { name: '', agg: 'avg', windowMode: 'tumbling', metric: '', windowMs: 60000, op: 'gt', threshold: 0 };
    case 'deltaRate':
      return { name: '', metric: '', op: 'gt', threshold: 0 };
    case 'repeating':
      return { name: '', count: 3, windowMs: 60000 };
    case 'correlation':
      return { name: '', anchorType: '', count: 3, windowMs: 60000 };
    case 'branch':
      return { when: '' } satisfies BranchConfig as NodeConfig;
    case 'action':
      return { action: 'raiseAlarm' };
  }
}
