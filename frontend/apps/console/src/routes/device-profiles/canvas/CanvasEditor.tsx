// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The visual automation canvas editor (ADR-053 slice 9b). It authors a DetectionRule as a
// node graph: a Source, one condition node, and its REACT actions, wired by typed ports. It is
// the ceiling to the DetectionRuleForm's floor — both produce the SAME DetectionRule draft
// (Definition + the AuthoringGraph sidecar) and ride the same profile draft/publish/rollback
// host. The browser never authors the rules.Rule definition: the graph is compiled
// server-side (compileCanvas), which returns the definition to store and the diagnostics to
// show on nodes. A form-authored rule opens here via the pure reverse round-trip.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  addEdge,
  useEdgesState,
  useNodesState,
  type Connection,
  type Edge,
  type Node,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import { TokenField } from '@/components/ui/token-field';
import { ErrorBanner } from '@/components/ui/error-banner';
import { normalizeToken } from '@devicechain/client';
import { errMessage } from '@/routes/common';
import {
  createDetectionRule,
  updateDetectionRule,
  type DetectionRule,
  type DetectionRuleCreateRequest,
} from '@/lib/api/device-management';
import { compileCanvas, type CanvasCompileResult, type NodeTraceStep } from '@/lib/api/event-processing';
import {
  CONDITION_TYPES,
  NODE_CATALOG,
  defaultConfig,
  portTypeOf,
  type CanvasDefinition,
  type NodeConfig,
  type NodeType,
} from './model';
import { buildCanvasDefinition, graphFromDefinition } from './roundtrip';
import { CanvasNodeView, type CanvasNodeData } from './nodes';
import { NodeInspector } from './inspector';
import { PreviewPanel } from './PreviewPanel';

const nodeTypes = { dc: CanvasNodeView };

// splitEndpoint parses a "nodeId:port" endpoint on its last colon (matches the Go/model form).
function splitEndpoint(s: string): [string, string] {
  const i = s.lastIndexOf(':');
  return i < 0 ? [s, ''] : [s.slice(0, i), s.slice(i + 1)];
}

// toReactFlow maps a CanvasDefinition to the editor's node/edge state.
function toReactFlow(def: CanvasDefinition): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = def.nodes.map((n) => ({
    id: n.id,
    type: 'dc',
    position: { x: n.ui?.x ?? 0, y: n.ui?.y ?? 0 },
    data: { nodeType: n.type, config: n.config } satisfies CanvasNodeData,
  }));
  const edges: Edge[] = def.edges.map((e, i) => {
    const [source, sourceHandle] = splitEndpoint(e.from);
    const [target, targetHandle] = splitEndpoint(e.to);
    return { id: `e-${i}-${source}-${target}`, source, sourceHandle, target, targetHandle };
  });
  return { nodes, edges };
}

// fromReactFlow maps the editor state back to a CanvasDefinition (with layout coords).
function fromReactFlow(nodes: Node[], edges: Edge[]): CanvasDefinition {
  return buildCanvasDefinition(
    nodes.map((n) => {
      const d = n.data as CanvasNodeData;
      return { id: n.id, type: d.nodeType, config: d.config, ui: { x: Math.round(n.position.x), y: Math.round(n.position.y) } };
    }),
    edges.map((e) => ({ from: `${e.source}:${e.sourceHandle ?? ''}`, to: `${e.target}:${e.targetHandle ?? ''}` })),
  );
}

// structuralKey is the compile-relevant fingerprint (node types + configs + wiring) — it
// EXCLUDES layout coords and diagnostics, so dragging a node or painting a diagnostic never
// triggers a recompile, only a real edit does.
function structuralKey(nodes: Node[], edges: Edge[]): string {
  return JSON.stringify({
    n: nodes.map((n) => ({ id: n.id, t: (n.data as CanvasNodeData).nodeType, c: (n.data as CanvasNodeData).config })),
    e: edges.map((e) => `${e.source}:${e.sourceHandle}->${e.target}:${e.targetHandle}`).sort(),
  });
}

// initialGraph resolves the starting graph: the stored AuthoringGraph if the rule was
// canvas-authored, else the reverse round-trip of its definition, else a lone Source for a new
// rule.
function initialGraph(entity: DetectionRule | undefined, profileToken: string): CanvasDefinition {
  if (entity?.authoringGraph) {
    try {
      const parsed = JSON.parse(entity.authoringGraph) as CanvasDefinition;
      // Require both arrays — an API-authored sidecar that satisfies the backend's
      // "JSON object" guard but lacks edges would otherwise crash toReactFlow (L3).
      if (parsed && Array.isArray(parsed.nodes) && Array.isArray(parsed.edges)) return parsed;
    } catch {
      // fall through to synthesis
    }
  }
  if (entity?.definition) {
    const { graph } = graphFromDefinition(entity.definition, profileToken);
    if (graph) return graph;
  }
  return {
    schemaVersion: 1,
    nodes: [{ id: 'source', type: 'source', config: { scope: { kind: 'profile', profileToken } }, ui: { x: 40, y: 160 } }],
    edges: [],
  };
}

// conditionMeta pulls the rule-level name/description off the single condition node, for the
// DetectionRule entity fields (the definition carries its own copy; they must agree).
function conditionMeta(nodes: Node[]): { name?: string; description?: string } {
  const cond = nodes.find((n) => NODE_CATALOG[(n.data as CanvasNodeData).nodeType]?.category === 'condition');
  const cfg = (cond?.data as CanvasNodeData | undefined)?.config ?? {};
  return {
    name: typeof cfg.name === 'string' && cfg.name.trim() ? cfg.name.trim() : undefined,
    description: typeof cfg.description === 'string' && cfg.description.trim() ? cfg.description.trim() : undefined,
  };
}

function CanvasEditorInner({ profileToken, entity, onDone }: { profileToken: string; entity?: DetectionRule; onDone: (message: string) => void }) {
  const editing = entity != null;
  const seed = useMemo(() => toReactFlow(initialGraph(entity, profileToken)), [entity, profileToken]);
  const [nodes, setNodes, onNodesChange] = useNodesState(seed.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(seed.edges);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // The per-node trace of the firing an author selected in the preview panel (slice 9e); null = no
  // overlay. Painted onto the nodes below.
  const [trace, setTrace] = useState<NodeTraceStep[] | null>(null);

  const [token, setToken] = useState(entity?.token ?? '');
  const [enabled, setEnabled] = useState(entity?.enabled ?? true);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // forKey stamps the structural key the result was compiled FOR, so Save can require the
  // result to match the CURRENT graph — otherwise a stale definition (from a compile that is
  // still in flight, or a still-good previous result kept during a re-check) could be stored
  // alongside a newer graph, the exact definition/graph divergence the design prevents (H1).
  const [compile, setCompile] = useState<{ status: 'idle' | 'checking' | 'done'; result: CanvasCompileResult | null; forKey: string | null }>({
    status: 'idle',
    result: null,
    forKey: null,
  });

  const idSeq = useRef(1);
  const newId = (type: NodeType): string => {
    // Zero-pad the sequence so ids sort in creation order lexicographically — the server
    // orders a rule's actions by sorting node ids, so `action-0012` must sort after
    // `action-0003`, not before `action-2` (L4).
    const mk = () => `${type}-${String(idSeq.current++).padStart(4, '0')}`;
    let id = mk();
    const existing = new Set(nodes.map((n) => n.id));
    while (existing.has(id)) id = mk();
    return id;
  };

  // The current canvas document (with layout), kept in a ref so the debounced compile reads
  // the latest without being a dependency (only a structural change re-arms the timer).
  const canvasDef = useMemo(() => fromReactFlow(nodes, edges), [nodes, edges]);
  const canvasDefRef = useRef(canvasDef);
  canvasDefRef.current = canvasDef;
  const key = useMemo(() => structuralKey(nodes, edges), [nodes, edges]);

  // Debounced server-authoritative compile: paints diagnostics on nodes and captures the
  // compiled definition. A transport failure degrades to no-feedback (like the form's inline
  // check) rather than blocking authoring.
  useEffect(() => {
    let cancelled = false;
    setCompile((c) => ({ status: 'checking', result: c.result, forKey: c.forKey }));
    const timer = setTimeout(async () => {
      try {
        const res = await Promise.race([
          compileCanvas(JSON.stringify(canvasDefRef.current), profileToken),
          new Promise<never>((_, rej) => setTimeout(() => rej(new Error('timeout')), 10000)),
        ]);
        if (cancelled) return;
        setCompile({ status: 'done', result: res, forKey: key });
        const byNode = new Map<string, string>();
        for (const d of res.diagnostics) if (d.nodeId) byNode.set(d.nodeId, d.message);
        setNodes((ns) =>
          ns.map((n) => {
            const diagnostic = byNode.get(n.id);
            const d = n.data as CanvasNodeData;
            return d.diagnostic === diagnostic ? n : { ...n, data: { ...d, diagnostic } };
          }),
        );
      } catch {
        if (!cancelled) setCompile({ status: 'idle', result: null, forKey: key });
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [key, profileToken, setNodes]);

  // Paint the selected firing's per-node trace (slice 9e) onto the canvas: each node carries its
  // disposition for that firing (source delivered · condition raised/resolved · branch passed/blocked ·
  // action raised/sent/cleared/skipped/inert), for an at-a-glance "what did THIS event do" overlay.
  // Clears when trace is null (the panel deselected, ran again, or the graph was edited). The trace
  // fields are excluded from structuralKey, so painting never arms a recompile — same discipline as
  // the diagnostic paint above.
  useEffect(() => {
    const byNode = new Map<string, { disposition: string; detail?: string }>();
    if (trace) for (const s of trace) byNode.set(s.nodeId, { disposition: s.disposition, detail: s.detail ?? undefined });
    setNodes((ns) =>
      ns.map((n) => {
        const t = byNode.get(n.id);
        const d = n.data as CanvasNodeData;
        if (d.traceDisposition === t?.disposition && d.traceDetail === t?.detail) return n;
        return { ...n, data: { ...d, traceDisposition: t?.disposition, traceDetail: t?.detail } };
      }),
    );
  }, [trace, setNodes]);

  const isValidConnection = useCallback(
    (conn: Connection | Edge): boolean => {
      const src = nodes.find((n) => n.id === conn.source);
      const tgt = nodes.find((n) => n.id === conn.target);
      if (!src || !tgt || !conn.sourceHandle || !conn.targetHandle) return false;
      // Reject a self-loop up front (the branch is the first node with same-typed in+out ports, so
      // branch:out→branch:in would otherwise pass the port-type check). The server's detectCycle
      // rejects any cycle authoritatively; this just spares the round-trip for the locally-obvious case.
      if (conn.source === conn.target) return false;
      const st = portTypeOf((src.data as CanvasNodeData).nodeType, conn.sourceHandle, true);
      const tt = portTypeOf((tgt.data as CanvasNodeData).nodeType, conn.targetHandle, false);
      return st !== null && st === tt;
    },
    [nodes],
  );

  const onConnect = useCallback(
    (conn: Connection) => setEdges((eds) => addEdge({ ...conn, id: `e-${conn.source}-${conn.target}-${conn.sourceHandle}` }, eds)),
    [setEdges],
  );

  const addNode = (type: NodeType) => {
    const id = newId(type);
    const spread = nodes.length;
    // Lay a new node in its lane by category: source · condition · branch · action, left→right,
    // matching the DETECT→REACT flow (branch sits between the condition and the action it gates).
    const cat = NODE_CATALOG[type].category;
    const x = cat === 'source' ? 40 : cat === 'compute' ? 180 : cat === 'condition' ? 320 : cat === 'branch' ? 500 : 700;
    setNodes((ns) => [
      ...ns,
      {
        id,
        type: 'dc',
        position: { x, y: 60 + spread * 30 },
        data: { nodeType: type, config: defaultConfig(type, profileToken) } satisfies CanvasNodeData,
      },
    ]);
    setSelectedId(id);
  };

  const removeSelected = () => {
    if (!selectedId) return;
    setEdges((eds) => eds.filter((e) => e.source !== selectedId && e.target !== selectedId));
    setNodes((ns) => ns.filter((n) => n.id !== selectedId));
    setSelectedId(null);
  };

  const updateConfig = (id: string, config: NodeConfig) =>
    setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, data: { ...(n.data as CanvasNodeData), config } } : n)));

  const selected = nodes.find((n) => n.id === selectedId);
  const result = compile.result;
  // The result must be the FINISHED compile of the CURRENT graph (forKey === key) — not a
  // still-good previous result kept during a re-check, and not an in-flight one — so Save can
  // never store a definition that doesn't match what is on the canvas (H1).
  const fresh = compile.status === 'done' && compile.forKey === key;
  const canSave = fresh && !!result?.ok && (editing || token.trim().length > 0) && !busy;

  const save = async () => {
    if (!fresh || !result?.ok || !result.definition) return;
    setFormError(null);
    setBusy(true);
    try {
      const meta = conditionMeta(nodes);
      const request: DetectionRuleCreateRequest = {
        token: editing ? entity.token : token.trim(),
        deviceProfileToken: profileToken,
        name: meta.name,
        description: meta.description,
        definition: result.definition,
        authoringGraph: JSON.stringify(fromReactFlow(nodes, edges)),
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

  // Graph-level diagnostics (no node to pin them to) surface in the side panel.
  const graphErrors = (result?.diagnostics ?? []).filter((d) => !d.nodeId);

  return (
    <div className="flex flex-col gap-4">
      {formError && <ErrorBanner message={formError} onDismiss={() => setFormError(null)} />}

      <div className="flex flex-wrap items-end justify-between gap-3">
        <FormField label="Token" htmlFor="canvas-token" description={editing ? 'The rule id; it cannot change.' : undefined}>
          {editing ? (
            <Input id="canvas-token" value={token} disabled />
          ) : (
            <TokenField id="canvas-token" entityType={normalizeToken('detection rule')} value={token} onChange={setToken} seed={conditionMeta(nodes).name ?? ''} placeholder="freezer-warm" />
          )}
        </FormField>
        <label className="flex items-center gap-2 pb-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
        <div className="flex items-center gap-3 pb-1">
          <CompileStatus status={compile.status} result={result} />
          <Button onClick={save} loading={busy} disabled={!canSave}>
            {editing ? 'Save changes' : 'Create rule'}
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 p-2">
        <span className="text-xs font-medium text-muted-foreground">Add:</span>
        <Button variant="outline" size="sm" onClick={() => addNode('source')}>
          <Plus size={14} /> Source
        </Button>
        {CONDITION_TYPES.map((t) => (
          <Button key={t} variant="outline" size="sm" onClick={() => addNode(t)}>
            {NODE_CATALOG[t].label}
          </Button>
        ))}
        <Button variant="outline" size="sm" onClick={() => addNode('compute')}>
          <Plus size={14} /> Compute
        </Button>
        <Button variant="outline" size="sm" onClick={() => addNode('branch')}>
          <Plus size={14} /> Branch
        </Button>
        <Button variant="outline" size="sm" onClick={() => addNode('action')}>
          <Plus size={14} /> Action
        </Button>
      </div>

      <div className="flex gap-4" style={{ height: 520 }}>
        <div className="flex-1 overflow-hidden rounded-md border">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            isValidConnection={isValidConnection}
            onNodeClick={(_, n) => setSelectedId(n.id)}
            onPaneClick={() => setSelectedId(null)}
            onNodesDelete={(deleted) => {
              if (deleted.some((n) => n.id === selectedId)) setSelectedId(null);
            }}
            fitView
            proOptions={{ hideAttribution: true }}
          >
            <Background />
            <Controls />
          </ReactFlow>
        </div>

        <aside className="w-80 shrink-0 overflow-y-auto rounded-md border p-3">
          {selected ? (
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <h4 className="text-sm font-semibold">{NODE_CATALOG[(selected.data as CanvasNodeData).nodeType].label}</h4>
                {(selected.data as CanvasNodeData).nodeType !== 'source' && (
                  <Button variant="ghost" size="sm" onClick={removeSelected} title="Remove node">
                    <Trash2 size={14} />
                  </Button>
                )}
              </div>
              {(selected.data as CanvasNodeData).diagnostic && (
                <p className="rounded-md border border-destructive/50 bg-destructive/10 px-2 py-1.5 text-xs text-destructive">
                  {(selected.data as CanvasNodeData).diagnostic}
                </p>
              )}
              <NodeInspector
                type={(selected.data as CanvasNodeData).nodeType}
                config={(selected.data as CanvasNodeData).config}
                onChange={(config) => updateConfig(selected.id, config)}
              />
            </div>
          ) : (
            <div className="space-y-3 text-sm text-muted-foreground">
              <p>Select a node to edit it, or add one from the palette. Wire a source into a condition, and the condition's signal into an action.</p>
              {graphErrors.length > 0 && (
                <ul className="space-y-1">
                  {graphErrors.map((d, i) => (
                    <li key={i} className="rounded-md border border-destructive/50 bg-destructive/10 px-2 py-1.5 text-xs text-destructive">
                      {d.message}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </aside>
      </div>

      {/* Replay-preview: run the current draft against history (ADR-053 slice 9d). Gated on a fresh,
          successful compile — the same forKey===key freshness Save uses, so the previewed graph is
          exactly what is on the canvas; structuralKey lets the panel invalidate a stale result when
          the graph is edited. The reason string drives an accurate disabled tooltip. */}
      <PreviewPanel
        graph={JSON.stringify(canvasDef)}
        profileToken={profileToken}
        structuralKey={key}
        notReadyReason={!fresh ? 'Waiting for the compiler…' : !result?.ok ? 'Fix the compile errors before previewing' : null}
        onTrace={setTrace}
      />
    </div>
  );
}

function CompileStatus({ status, result }: { status: 'idle' | 'checking' | 'done'; result: CanvasCompileResult | null }) {
  if (status === 'checking') return <span className="text-xs text-muted-foreground">Checking…</span>;
  if (!result) return null;
  if (result.ok) {
    return (
      <span className="text-xs text-success">
        Compiles{typeof result.estimatedCost === 'number' ? ` · cost ${result.estimatedCost}` : ''}
      </span>
    );
  }
  return <span className="text-xs text-destructive">Not valid yet</span>;
}

// CanvasEditor is the exported entry — it provides the @xyflow/react context the editor needs.
export function CanvasEditor(props: { profileToken: string; entity?: DetectionRule; onDone: (message: string) => void }) {
  return (
    <ReactFlowProvider>
      <CanvasEditorInner {...props} />
    </ReactFlowProvider>
  );
}
