// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The node inspector (ADR-053 slice 9b): the config-editing panel for the selected canvas
// node. Its fields are per-type projections of the same rules.Rule field groups the form
// builder edits (the canvas is the ceiling, the form the floor — both target one schema). It
// mutates the opaque node config; the server-authoritative compileCanvas is what validates it,
// so this panel is permissive and the diagnostics land on the node.

import { type ReactNode } from 'react';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { Textarea } from '@/routes/common';
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
          <option value="ms">ms</option>
          <option value="s">sec</option>
          <option value="m">min</option>
          <option value="h">hr</option>
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
      <FormField label="Condition" htmlFor="leaf-mode">
        <Select id="leaf-mode" value={mode} onChange={setMode}>
          {allowNone && <option value="none">Match every event</option>}
          <option value="structured">Comparison</option>
          <option value="cel">Advanced (CEL)</option>
        </Select>
      </FormField>
      {mode === 'structured' && (
        <div className="grid grid-cols-[1fr_auto] gap-2">
          <FormField label="Metric" htmlFor="leaf-metric">
            <Input id="leaf-metric" value={strVal(l.metric)} onChange={(e) => onChange({ ...l, metric: e.target.value })} placeholder="tempC" />
          </FormField>
          <FormField label="Op" htmlFor="leaf-op">
            <Select id="leaf-op" value={strVal(l.op) || 'gt'} onChange={(op) => onChange({ ...l, op })}>
              {opOptions}
            </Select>
          </FormField>
          <FormField label="Bound" htmlFor="leaf-boundkind">
            <Select
              id="leaf-boundkind"
              value={boundKind}
              onChange={(k) => onChange({ ...l, threshold: k === 'attribute' ? { kind: 'attribute', attribute: strVal(bound.attribute) } : { kind: 'literal', value: numVal(bound.value) } })}
            >
              <option value="literal">Literal</option>
              <option value="attribute">Device attribute</option>
            </Select>
          </FormField>
          {boundKind === 'literal' ? (
            <FormField label="Value" htmlFor="leaf-value">
              <Input id="leaf-value" type="number" value={numVal(bound.value)} onChange={(e) => onChange({ ...l, threshold: { kind: 'literal', value: Number(e.target.value) } })} />
            </FormField>
          ) : (
            <FormField label="Attribute" htmlFor="leaf-attr">
              <Input id="leaf-attr" value={strVal(bound.attribute)} onChange={(e) => onChange({ ...l, threshold: { kind: 'attribute', attribute: e.target.value } })} placeholder="tempLimit" />
            </FormField>
          )}
        </div>
      )}
      {mode === 'cel' && (
        <FormField label="CEL expression" htmlFor="leaf-cel" description="An advanced predicate over the event vocabulary. Cost-gated at compile.">
          <Textarea id="leaf-cel" value={strVal(l.cel)} onChange={(e) => onChange({ cel: e.target.value })} placeholder='m["tempC"].value > 30' />
        </FormField>
      )}
    </div>
  );
}

// meta edits the shared rule identity (name/description/severity) carried on a condition node.
function MetaFields({ config, set }: { config: NodeConfig; set: (patch: NodeConfig) => void }) {
  return (
    <>
      <FormField label="Name" htmlFor="cfg-name">
        <Input id="cfg-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value })} placeholder="Freezer warming" />
      </FormField>
      <FormField label="Severity" htmlFor="cfg-severity" description="Required when this rule raises an alarm.">
        <Select id="cfg-severity" value={strVal(config.severity)} onChange={(v) => set({ severity: v || undefined })}>
          <option value="">— none —</option>
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
  const set = (patch: NodeConfig) => onChange({ ...config, ...patch });
  const setLeaf = (leaf: unknown) => onChange({ ...config, when: leaf });

  switch (type) {
    case 'source':
      return (
        <p className="text-sm text-muted-foreground">
          Scoped to profile <span className="font-mono">{strVal(asRecord(config.scope).profileToken)}</span>. Every rule reads this profile's telemetry.
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
          <DurationField label="Sustained for" id="cfg-hold" ms={numVal(config.holdMs)} onChange={(ms) => set({ holdMs: ms })} />
        </div>
      );
    case 'absence':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <DurationField label="Silent for" id="cfg-timeout" ms={numVal(config.timeoutMs)} onChange={(ms) => set({ timeoutMs: ms })} />
        </div>
      );
    case 'aggregate': {
      const mode = strVal(config.windowMode) || 'tumbling';
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <div className="grid grid-cols-2 gap-2">
            <FormField label="Aggregate" htmlFor="cfg-agg">
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
            <FormField label="Window" htmlFor="cfg-mode">
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
            <FormField label="Value metric" htmlFor="cfg-metric">
              <Input id="cfg-metric" value={strVal(config.metric)} onChange={(e) => set({ metric: e.target.value })} placeholder="tempC" />
            </FormField>
          )}
          {(mode === 'tumbling' || mode === 'sliding') && (
            <DurationField label="Window" id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          )}
          {mode === 'session' && <DurationField label="Session gap" id="cfg-gap" ms={numVal(config.gapMs)} onChange={(ms) => set({ gapMs: ms })} />}
          {mode === 'count' && (
            <FormField label="Window size (events)" htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
          )}
          <div className="grid grid-cols-2 gap-2">
            <FormField label="Op" htmlFor="cfg-op">
              <Select id="cfg-op" value={strVal(config.op) || 'gt'} onChange={(v) => set({ op: v })}>
                {orderedOpOptions}
              </Select>
            </FormField>
            <FormField label="Threshold" htmlFor="cfg-threshold">
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
          <FormField label="Value metric" htmlFor="cfg-metric">
            <Input id="cfg-metric" value={strVal(config.metric)} onChange={(e) => set({ metric: e.target.value })} placeholder="tempC" />
          </FormField>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={!!config.rate} onChange={(e) => set({ rate: e.target.checked || undefined })} />
            Per-second rate
          </label>
          <div className="grid grid-cols-2 gap-2">
            <FormField label="Op" htmlFor="cfg-op">
              <Select id="cfg-op" value={strVal(config.op) || 'gt'} onChange={(v) => set({ op: v })}>
                {orderedOpOptions}
              </Select>
            </FormField>
            <FormField label="Threshold" htmlFor="cfg-threshold">
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
            <FormField label="Count" htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
            <DurationField label="Within" id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          </div>
        </div>
      );
    case 'correlation':
      return (
        <div className="space-y-4">
          <MetaFields config={config} set={set} />
          <FormField label="Anchor type" htmlFor="cfg-anchor" description="The area/anchor devices roll up to (e.g. zone).">
            <Input id="cfg-anchor" value={strVal(config.anchorType)} onChange={(e) => set({ anchorType: e.target.value })} placeholder="zone" />
          </FormField>
          <div className="grid grid-cols-2 gap-2">
            <FormField label="Distinct devices" htmlFor="cfg-count">
              <Input id="cfg-count" type="number" min={1} value={numVal(config.count)} onChange={(e) => set({ count: Number(e.target.value) })} />
            </FormField>
            <DurationField label="Within" id="cfg-window" ms={numVal(config.windowMs)} onChange={(ms) => set({ windowMs: ms })} />
          </div>
        </div>
      );
    case 'branch':
      return (
        <div className="space-y-4">
          <FormField label="Label" htmlFor="cfg-branch-name" description="An optional name for this route (authoring only).">
            <Input id="cfg-branch-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value || undefined })} placeholder="only when severe" />
          </FormField>
          <FormField
            label="Only when (CEL)"
            htmlFor="cfg-branch-when"
            description="A boolean over the detection: value, hasValue, series. Downstream actions run only when it holds. Cost-gated at compile."
          >
            <Textarea id="cfg-branch-when" value={strVal(config.when)} onChange={(e) => set({ when: e.target.value })} placeholder="value > 100.0" />
          </FormField>
          <p className="rounded-md border border-dashed px-2 py-1.5 text-xs text-muted-foreground">
            Use decimal literals (<span className="font-mono">value &gt; 100.0</span>, not <span className="font-mono">100</span>). <span className="font-mono">value</span> is the
            triggering reading — it is absent for conditions that carry no scalar (absence, duration, and metric-less or raw-CEL leaves), so guard those
            with <span className="font-mono">hasValue</span> (e.g. <span className="font-mono">hasValue &amp;&amp; value &gt; 100.0</span>). The branch never blocks an alarm from
            clearing.
          </p>
        </div>
      );
    case 'action': {
      const kind = strVal(config.action) || 'raiseAlarm';
      return (
        <div className="space-y-4">
          <FormField label="Action" htmlFor="cfg-action">
            <Select id="cfg-action" value={kind} onChange={(v) => onChange({ action: v })}>
              <option value="raiseAlarm">Raise alarm</option>
              <option value="sendCommand">Send command</option>
            </Select>
          </FormField>
          {kind === 'raiseAlarm' ? (
            <FormField label="Alarm key" htmlFor="cfg-alarmkey" description="Repeated firings escalate one alarm keyed on this. Empty ⇒ the rule's token.">
              <Input id="cfg-alarmkey" value={strVal(config.alarmKey)} onChange={(e) => set({ alarmKey: e.target.value || undefined })} placeholder="freezer-warm" />
            </FormField>
          ) : (
            <>
              <FormField label="Command" htmlFor="cfg-command">
                <Input id="cfg-command" value={strVal(config.command)} onChange={(e) => set({ command: e.target.value })} placeholder="cool" />
              </FormField>
              <FormField label="Payload (JSON)" htmlFor="cfg-payload" description="Optional static argument object.">
                <Textarea id="cfg-payload" value={strVal(config.payload)} onChange={(e) => set({ payload: e.target.value || undefined })} placeholder='{"level":2}' />
              </FormField>
            </>
          )}
        </div>
      );
    }
    case 'compute':
      return (
        <div className="space-y-4">
          <FormField
            label="Name"
            htmlFor="cfg-compute-name"
            description="The identifier the condition or branch references this value by. Letters, digits, underscore; not starting with a digit."
          >
            <Input id="cfg-compute-name" value={strVal(config.name)} onChange={(e) => set({ name: e.target.value })} placeholder="tempF" />
          </FormField>
          <FormField label="Value (CEL)" htmlFor="cfg-compute-expr" description="A single expression the compiler folds into the predicate it feeds. Cost-gated at compile.">
            <Textarea id="cfg-compute-expr" value={strVal(config.expr)} onChange={(e) => set({ expr: e.target.value })} placeholder={'m["tempC"] * 1.8 + 32.0'} />
          </FormField>
          <p className="rounded-md border border-dashed px-2 py-1.5 text-xs text-muted-foreground">
            Wire this into a condition&apos;s or branch&apos;s <span className="font-mono">value</span> port, then reference it by name in that node&apos;s CEL (e.g.{' '}
            <span className="font-mono">tempF &gt; 100.0</span>). It reads the vocabulary of whatever it feeds — the event&apos;s{' '}
            <span className="font-mono">m</span>/<span className="font-mono">attr</span> for a condition leaf, the detection&apos;s{' '}
            <span className="font-mono">value</span>/<span className="font-mono">series</span> for a branch. Use decimal literals; a computed value can only feed a{' '}
            <span className="font-mono">CEL</span> predicate, not a structured metric·op·threshold leaf.
          </p>
        </div>
      );
  }
}
