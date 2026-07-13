// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The custom @xyflow/react node for the automation canvas (ADR-053 slice 9b). A single node
// component renders every catalog type: a titled card with typed connection handles (one per
// declared port), a one-line config summary, and a compile-diagnostic state. The typed handles
// are what the editor's isValidConnection reads to forbid a cross-typed wire before the server
// re-checks it.

import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import {
  NODE_CATALOG,
  OP_SYMBOL,
  type CompareOp,
  type NodeType,
  type PortType,
} from './model';

// The data an editor node carries: its canvas type, opaque config, the latest compile diagnostic (an
// error message from compileCanvas, anchored to this node), and — while a firing is selected in the
// preview panel — that firing's trace disposition for this node (slice 9e overlay).
export interface CanvasNodeData {
  nodeType: NodeType;
  config: Record<string, unknown>;
  diagnostic?: string;
  traceDisposition?: string;
  traceDetail?: string;
  [key: string]: unknown;
}

// TRACE_STYLE maps a node's per-firing disposition (slice 9e) to its overlay badge label + colors.
// The palette reads at a glance: green = the signal flowed (passed/raised/sent/cleared/delivered),
// muted = it stopped or was inert (blocked/skipped/inert), and a distinct emerald for a resolve.
const TRACE_STYLE: Record<string, { label: string; badge: string; border: string }> = {
  delivered: { label: 'delivered', badge: 'bg-blue-500/15 text-blue-700 dark:text-blue-400', border: 'border-blue-500' },
  raised: { label: 'raised', badge: 'bg-destructive/15 text-destructive', border: 'border-destructive' },
  resolved: { label: 'resolved', badge: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400', border: 'border-emerald-500' },
  passed: { label: 'passed', badge: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400', border: 'border-emerald-500' },
  blocked: { label: 'blocked', badge: 'bg-amber-500/15 text-amber-700 dark:text-amber-400', border: 'border-amber-500' },
  skipped: { label: 'skipped', badge: 'bg-muted text-muted-foreground', border: 'border-muted-foreground/40' },
  sent: { label: 'sent', badge: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400', border: 'border-emerald-500' },
  cleared: { label: 'cleared', badge: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400', border: 'border-emerald-500' },
  inert: { label: 'inert', badge: 'bg-muted text-muted-foreground', border: 'border-muted-foreground/40' },
};

const PORT_COLOR: Record<PortType, string> = {
  stream: '#3b82f6', // blue — the event stream (DETECT)
  signal: '#f59e0b', // amber — a detection signal (DETECT→REACT)
  value: '#10b981', // green — a computed value (reserved)
};

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const num = (v: unknown): string => (typeof v === 'number' ? String(v) : '');

// boundSummary renders a leaf's right-hand side (literal value or device attribute).
function boundSummary(threshold: unknown): string {
  if (threshold && typeof threshold === 'object') {
    const b = threshold as { kind?: string; value?: number; attribute?: string };
    if (b.kind === 'literal') return String(b.value ?? '');
    if (b.kind === 'attribute') return `@${b.attribute ?? ''}`;
  }
  return '';
}

// leafSummary renders a when-leaf: structured `metric op bound`, raw CEL, or empty.
function leafSummary(when: unknown): string {
  if (!when || typeof when !== 'object') return 'every event';
  const w = when as { metric?: string; op?: string; threshold?: unknown; cel?: string };
  if (w.cel) return `CEL: ${w.cel}`;
  if (!w.metric) return 'every event';
  const op = OP_SYMBOL[(w.op as CompareOp) ?? 'gt'] ?? w.op ?? '';
  return `${w.metric} ${op} ${boundSummary(w.threshold)}`.trim();
}

const ms = (v: unknown): string => {
  const n = typeof v === 'number' ? v : 0;
  if (n === 0) return '0';
  if (n % 3_600_000 === 0) return `${n / 3_600_000}h`;
  if (n % 60_000 === 0) return `${n / 60_000}m`;
  if (n % 1000 === 0) return `${n / 1000}s`;
  return `${n}ms`;
};

// summarize is the one-line description shown on a node card — a fast read of what it detects.
export function summarize(type: NodeType, config: Record<string, unknown>): string {
  const c = config;
  switch (type) {
    case 'source': {
      const scope = c.scope as { profileToken?: string } | undefined;
      return scope?.profileToken ? `profile: ${scope.profileToken}` : 'no scope';
    }
    case 'threshold':
      return leafSummary(c.when);
    case 'duration':
      return `${leafSummary(c.when)} · for ${ms(c.holdMs)}`;
    case 'absence':
      return `silent for ${ms(c.timeoutMs)}`;
    case 'aggregate': {
      const metric = c.agg === 'count' ? '' : `(${str(c.metric)})`;
      const win = c.windowMode === 'session' ? `gap ${ms(c.gapMs)}` : c.windowMode === 'count' ? `${num(c.count)} evt` : ms(c.windowMs);
      return `${str(c.agg)}${metric} ${OP_SYMBOL[(c.op as CompareOp) ?? 'gt'] ?? ''} ${num(c.threshold)} · ${str(c.windowMode)} ${win}`;
    }
    case 'deltaRate':
      return `Δ${str(c.metric)}${c.rate ? '/s' : ''} ${OP_SYMBOL[(c.op as CompareOp) ?? 'gt'] ?? ''} ${num(c.threshold)}`;
    case 'repeating':
      return `${num(c.count)}× ${leafSummary(c.when)} in ${ms(c.windowMs)}`;
    case 'correlation':
      return `${num(c.count)} devices in ${str(c.anchorType)} · ${ms(c.windowMs)}`;
    case 'branch':
      return str(c.when) ? `if ${str(c.when)}` : 'if …';
    case 'action':
      return c.action === 'sendCommand' ? `send ${str(c.command) || '…'}` : `raise alarm ${str(c.alarmKey)}`.trim();
  }
}

// CanvasNodeView renders one node with its typed handles and diagnostic state.
export const CanvasNodeView = memo(function CanvasNodeView({ data, selected }: NodeProps) {
  const d = data as CanvasNodeData;
  const spec = NODE_CATALOG[d.nodeType];
  const name = str(d.config.name);
  const hasError = !!d.diagnostic;
  // The slice-9e trace overlay: when a firing is selected in the preview panel, this node carries its
  // disposition for that firing. An error border still wins (a broken node is more urgent than a
  // trace), but the trace border otherwise takes precedence over plain selection.
  const traceStyle = d.traceDisposition ? TRACE_STYLE[d.traceDisposition] : undefined;

  return (
    <div
      className={[
        'min-w-44 max-w-64 rounded-md border bg-card px-3 py-2 text-card-foreground shadow-sm transition-colors',
        hasError ? 'border-destructive' : traceStyle ? traceStyle.border : selected ? 'border-primary' : 'border-border',
      ].join(' ')}
      title={d.traceDetail ?? d.diagnostic ?? undefined}
    >
      {/* Target (input) handles on the left. */}
      {Object.entries(spec.in).map(([port, ptype], i) => (
        <Handle
          key={`in-${port}`}
          type="target"
          position={Position.Left}
          id={port}
          style={{ top: 24 + i * 16, width: 10, height: 10, background: PORT_COLOR[ptype], border: '1px solid var(--background)' }}
        />
      ))}
      {/* Source (output) handles on the right. */}
      {Object.entries(spec.out).map(([port, ptype], i) => (
        <Handle
          key={`out-${port}`}
          type="source"
          position={Position.Right}
          id={port}
          style={{ top: 24 + i * 16, width: 10, height: 10, background: PORT_COLOR[ptype], border: '1px solid var(--background)' }}
        />
      ))}

      <div className="flex items-center justify-between gap-2">
        <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">{spec.label}</span>
        {traceStyle ? (
          <span className={['rounded px-1 py-0.5 text-[9px] font-semibold uppercase tracking-wide', traceStyle.badge].join(' ')}>{traceStyle.label}</span>
        ) : (
          hasError && <span className="h-1.5 w-1.5 rounded-full bg-destructive" />
        )}
      </div>
      {name && <div className="truncate text-sm font-medium">{name}</div>}
      <div className="mt-0.5 truncate text-xs text-muted-foreground">{summarize(d.nodeType, d.config)}</div>
    </div>
  );
});
