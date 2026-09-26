// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The canvas's open-time fidelity check, measured through the real editor.
//
// 🔴 THE CANVAS USED TO THROW AWAY "I CANNOT SHOW THIS RULE" AND THEN LET YOU SAVE OVER IT.
// A rule of a type it had no node for opened as a lone Source with no explanation; a rule
// with settings it did not model opened without them; a rule whose definition had been changed
// outside the canvas opened from its stale saved graph. In every case a canvas Save wrote the
// reduced rule back. Each test below asserts the VALUE that was wrong — Save enabled, the notice
// absent, the saved definition missing a field — not merely that something rendered.
//
// The compiler is faked by `lower`, a deliberately small stand-in for the server's graph
// lowering that covers the node shapes these tests lay out. It answers every call the same way
// for the same graph, so the open-time check and the editor's own debounced compile agree —
// which is how the real, pure compileCanvas behaves.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// jsdom has no ResizeObserver, and the graph VIEW is not what this file measures. The node and
// edge state hooks and the provider stay real.
vi.mock('@xyflow/react', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ReactFlow: () => null,
  Background: () => null,
  Controls: () => null,
}));
vi.mock('@/lib/api/event-processing', () => ({ compileCanvas: vi.fn(), previewRule: vi.fn() }));
vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDetectionRule: vi.fn(async () => undefined),
  updateDetectionRule: vi.fn(async () => undefined),
}));
vi.mock('@/lib/api/connectors', () => ({ listConnectors: vi.fn(async () => []) }));
vi.mock('@/auth/AuthProvider', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useAuth: () => ({ tenant: null, identity: null, authorities: [], can: () => true }),
}));

import { compileCanvas } from '@/lib/api/event-processing';
import { updateDetectionRule, type DetectionRule } from '@/lib/api/device-management';
import { CanvasEditor } from './CanvasEditor';

const compileMock = vi.mocked(compileCanvas);
const updateMock = vi.mocked(updateDetectionRule);

afterEach(cleanup);
beforeEach(() => vi.clearAllMocks());

// ── The fake compiler ───────────────────────────────────────────────────────

type Obj = Record<string, unknown>;
interface GNode {
  id: string;
  type: string;
  config: Obj;
}

// goDuration renders whole milliseconds the way Go's time.Duration.String does for the values
// these tests use ("10m0s", "1m30s", "500ms").
function goDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  const s = (ms % 60_000) / 1000;
  return `${h ? `${h}h` : ''}${h || m ? `${m}m` : ''}${s}s`;
}

// lower is the stand-in for the server: condition meta and leaf, a hold, and raiseAlarm /
// sendCommand actions ordered by node id. Like the server it always writes `name` and `when`.
function lower(graphJson: string): { ok: boolean; definition: string | null } {
  const g = JSON.parse(graphJson) as { nodes: GNode[] };
  const cond = g.nodes.find((n) => !['source', 'action', 'branch', 'compute'].includes(n.type));
  if (!cond) return { ok: false, definition: null };
  const c = cond.config;
  const rule: Obj = { name: c.name ?? '', type: cond.type };
  if (c.description) rule.description = c.description;
  if (c.severity) rule.severity = c.severity;
  const when: Obj = {};
  const leaf = c.when as { metric?: string; op?: string; threshold?: { value?: number } } | undefined;
  if (leaf?.metric) when.metric = leaf.metric;
  if (leaf?.op) when.op = leaf.op;
  if (typeof leaf?.threshold?.value === 'number') when.threshold = leaf.threshold.value;
  rule.when = when;
  if (typeof c.holdMs === 'number' && c.holdMs > 0) rule.hold = goDuration(c.holdMs);
  const actions = g.nodes
    .filter((n) => n.type === 'action')
    .sort((a, b) => (a.id < b.id ? -1 : 1))
    .map((n) => {
      const a = n.config;
      if (a.action === 'sendCommand') return { type: 'sendCommand', sendCommand: { command: a.command } };
      const ra: Obj = {};
      if (a.alarmKey) ra.alarmKey = a.alarmKey;
      if (a.alarmKeyTemplate) ra.alarmKeyTemplate = a.alarmKeyTemplate;
      return { type: 'raiseAlarm', raiseAlarm: ra };
    });
  if (actions.length) rule.actions = actions;
  return { ok: true, definition: JSON.stringify(rule) };
}

function answer(graph: string) {
  const r = lower(graph);
  return { ...r, estimatedCost: null, diagnostics: [] } as unknown as Awaited<ReturnType<typeof compileCanvas>>;
}

const compilesFaithfully = () => compileMock.mockImplementation(async (graph) => answer(graph));

// ── Harness ─────────────────────────────────────────────────────────────────

const rule = (definition: string, extra: Partial<DetectionRule> = {}): DetectionRule =>
  ({
    token: 'r1',
    name: 'Hot',
    description: null,
    definition,
    authoringGraph: null,
    enabled: true,
    metadata: null,
    entityGroupToken: null,
    entityGroupVersion: null,
    ...extra,
  }) as unknown as DetectionRule;

const open = (entity?: DetectionRule) => render(<CanvasEditor profileToken="p" entity={entity} onDone={() => {}} />);
const saveBtn = () => screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement;
// Waits out the debounced compile, so "Save disabled" is measured AFTER the point where the old
// editor enabled it — not during a window in which it was disabled anyway.
const settled = () => waitFor(() => expect(screen.getByText('Compiles')).toBeTruthy());
// Waits for a compile to finish either way (a lone Source does not compile).
const compiled = () => waitFor(() => expect(screen.queryByText('Compiles') ?? screen.queryByText('Not valid yet')).toBeTruthy());
// addNode is an edit through the palette; the fake compiler lowers any graph with a condition.
const addNode = (label: string) => fireEvent.click(screen.getByRole('button', { name: label }));
// The graph of every compile the editor asked for, parsed.
const compiledGraphs = () => compileMock.mock.calls.map((c) => JSON.parse(c[0]) as { nodes: GNode[] });
const savedRequest = () => updateMock.mock.calls[0][1] as Obj;

const threshold = (value: number, extra: Obj = {}) => ({
  name: 'Hot',
  type: 'threshold',
  severity: 'major',
  when: { metric: 'tempC', op: 'gt', threshold: value },
  actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'hot' } }],
  ...extra,
});

// A canvas graph for `threshold(value)`, as the canvas itself would have saved it.
const thresholdGraph = (value: number, meta: Obj = {}) => ({
  schemaVersion: 1,
  nodes: [
    { id: 'source', type: 'source', config: { scope: { kind: 'profile', profileToken: 'p' } }, ui: { x: 0, y: 0 } },
    {
      id: 'threshold-0001',
      type: 'threshold',
      config: { name: 'Hot', severity: 'major', when: { metric: 'tempC', op: 'gt', threshold: { kind: 'literal', value } }, ...meta },
      ui: { x: 0, y: 0 },
    },
    { id: 'action-0002', type: 'action', config: { action: 'raiseAlarm', alarmKey: 'hot' }, ui: { x: 0, y: 0 } },
  ],
  edges: [
    { from: 'source:out', to: 'threshold-0001:in' },
    { from: 'threshold-0001:signal', to: 'action-0002:in' },
  ],
});

const canvasAuthored = (graph: object) => {
  const g = JSON.stringify(graph);
  return rule(lower(g).definition as string, { authoringGraph: g });
};

// ── The defects ─────────────────────────────────────────────────────────────

describe('opening a stored rule the canvas cannot show in full', () => {
  it('refuses a rule of a type it has no node for, and says why', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify({ name: 'r', type: 'frobnicate' })));
    await compiled();

    const alert = screen.getByRole('alert');
    expect(alert.textContent).toMatch(/frobnicate\) cannot be shown on the canvas/);
    expect(alert.textContent).toMatch(/saving is turned off/);

    // Building a rule that compiles over the lone Source must not re-open the way to a save
    // that replaces the stored rule.
    addNode('Threshold');
    await settled();
    expect(saveBtn().disabled).toBe(true);
  });

  it('refuses a rule carrying a field the canvas does not model', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(30, { futureKnob: 'a field from a later release' }))));
    await settled();

    expect(screen.getByRole('alert').textContent).toMatch(/settings the canvas cannot show/);
    expect(saveBtn().disabled).toBe(true);
  });

  it('refuses a rule whose unknown action it had to leave off the canvas', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(30, { actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'hot' } }, { type: 'future' }] }))));
    await settled();

    expect(screen.getByRole('alert').textContent).toMatch(/settings the canvas cannot show/);
    expect(saveBtn().disabled).toBe(true);
  });

  it('refuses when the laid-out rule does not compile', async () => {
    compileMock.mockImplementation(async () => ({
      ok: false,
      definition: null,
      estimatedCost: null,
      diagnostics: [{ nodeId: 'condition', severity: 'error', message: 'x' }],
    }) as unknown as Awaited<ReturnType<typeof compileCanvas>>);
    open(rule(JSON.stringify(threshold(30))));

    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/does not compile, so the canvas cannot confirm/));
    expect(saveBtn().disabled).toBe(true);
  });

  it('reports an unreachable compiler, turns saving off, and checks again on request', async () => {
    compileMock.mockRejectedValueOnce(new Error('down')).mockImplementation(async (graph) => answer(graph));
    open(rule(JSON.stringify(threshold(30))));

    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/could not reach the compiler/));
    await settled();
    expect(saveBtn().disabled).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: 'Check again' }));
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    await settled();
    expect(saveBtn().disabled).toBe(false);
  });
});

describe('a rule changed outside the canvas', () => {
  // The saved graph says 30; the definition was later set to 40 through the API.
  it('is laid out again from the rule, so a save keeps the change instead of reverting it', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(40)), { authoringGraph: JSON.stringify(thresholdGraph(30)) }));
    await settled();

    expect(screen.getByRole('status').textContent).toMatch(/laid it out again from the rule itself/);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(saveBtn().disabled).toBe(false);

    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    expect(JSON.parse(savedRequest().definition as string).when.threshold).toBe(40);
  });

  // The other direction: a field REMOVED from the definition, still present on the stale graph.
  // A one-directional check calls this faithful, and the save puts the field back.
  it('does not restore a field that was removed from the definition', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(30)), { authoringGraph: JSON.stringify(thresholdGraph(30, { description: 'stale' })) }));
    await settled();

    expect(screen.getByRole('status').textContent).toMatch(/laid it out again from the rule itself/);
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    expect(JSON.parse(savedRequest().definition as string).description).toBeUndefined();
  });

  it('refuses when the changed rule is one the canvas cannot lay out in full', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(40, { futureKnob: 'x' })), { authoringGraph: JSON.stringify(thresholdGraph(30)) }));
    await settled();

    expect(screen.getByRole('alert').textContent).toMatch(/no longer matches it/);
    expect(saveBtn().disabled).toBe(true);
  });
});

describe('a canvas-authored rule whose saved graph no longer compiles', () => {
  // The whole authored graph is on screen, so nothing is hidden; and the Form cannot author
  // every node the canvas can. Locking it here would leave it editable nowhere in the console.
  it('says so, and lets a fix be saved', async () => {
    const graph = thresholdGraph(30);
    const entity = canvasAuthored(graph);
    compileMock
      .mockResolvedValueOnce({ ok: false, definition: null, estimatedCost: null, diagnostics: [] } as unknown as Awaited<ReturnType<typeof compileCanvas>>)
      .mockImplementation(async (g) => answer(g));
    open(entity);

    await waitFor(() => expect(screen.getByRole('status').textContent).toMatch(/does not compile as it stands/));
    expect(saveBtn().disabled).toBe(true); // it does not compile yet

    addNode('Branch'); // any edit re-arms the compile
    await settled();
    expect(saveBtn().disabled).toBe(false);
  });
});

// ── Connectivity ────────────────────────────────────────────────────────────

describe('connectivity rules on the canvas', () => {
  it('opens a stored connectivity rule as a connectivity node, and lets it be saved', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify({ name: 'offline', type: 'connectivity', severity: 'critical', actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'offline' } }] })));
    await compiled();

    expect(compiledGraphs().some((g) => g.nodes.some((n) => n.type === 'connectivity'))).toBe(true);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(saveBtn().disabled).toBe(false);
  });

  it('offers a Connectivity node in the palette', () => {
    compilesFaithfully();
    open();
    expect(screen.getByRole('button', { name: 'Connectivity' })).toBeTruthy();
  });
});

// ── Counterweights: a notice that fired on every open would satisfy every test above ──

describe('opening a rule the canvas CAN show in full', () => {
  it('stays quiet on a healthy rule and lets it be saved', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify(threshold(30))));
    await settled();

    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
    expect(saveBtn().disabled).toBe(false);
  });

  it('keeps an alarm-key template, which the canvas carries, through a save', async () => {
    compilesFaithfully();
    const tmpl = '"zone-" + series';
    open(rule(JSON.stringify(threshold(30, { actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKeyTemplate: tmpl } }] }))));
    await settled();

    expect(screen.queryByRole('alert')).toBeNull();
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    expect(JSON.parse(savedRequest().definition as string).actions[0].raiseAlarm.alarmKeyTemplate).toBe(tmpl);
  });

  // The server re-marshals durations canonically ("10m" comes back "10m0s"). Comparing the
  // strings would call every API-authored duration rule lossy.
  it('treats a duration spelled differently as the same duration', async () => {
    compilesFaithfully();
    open(rule(JSON.stringify({ name: 'Hot', type: 'duration', when: { metric: 'tempC', op: 'gt', threshold: 30 }, hold: '10m' })));
    await settled();

    expect(screen.queryByRole('alert')).toBeNull();
    expect(saveBtn().disabled).toBe(false);
  });

  it('stays quiet on a canvas-authored rule', async () => {
    compilesFaithfully();
    open(canvasAuthored(thresholdGraph(30)));
    await settled();

    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
    expect(saveBtn().disabled).toBe(false);
  });

  it('checks nothing when creating a rule', async () => {
    compilesFaithfully();
    open();
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'new-rule' } });
    addNode('Threshold');
    await settled();

    expect(screen.queryByRole('alert')).toBeNull();
    expect((screen.getByRole('button', { name: 'Create rule' }) as HTMLButtonElement).disabled).toBe(false);
  });
});

// ── Entity columns the definition does not carry ──────────────────────────────

describe('saving from the canvas', () => {
  // The rule's name and description are columns as well as definition fields. An API-created
  // rule can have a column name and no definition name; the canvas shows the definition's
  // (empty) one, and sending that back as null used to clear the column.
  it('leaves the name and description columns alone when they were not edited', async () => {
    compilesFaithfully();
    const def = { type: 'threshold', severity: 'major', when: { metric: 'tempC', op: 'gt', threshold: 30 } };
    open(rule(JSON.stringify(def), { name: 'Column name', description: 'Column description' }));
    await settled();

    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    const req = savedRequest();
    expect({ name: req.name, description: req.description, hasName: 'name' in req, hasDescription: 'description' in req }).toEqual({
      name: undefined,
      description: undefined,
      hasName: false,
      hasDescription: false,
    });
  });
});
