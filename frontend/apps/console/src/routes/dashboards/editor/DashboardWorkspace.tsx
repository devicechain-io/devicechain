// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardWorkspace — the view/edit host for a single dashboard in the console
// (ADR-039 amendment: authoring re-homes here from the standalone /dash app). It
// owns the working copy of the definition, the view/edit mode, and the dirty-aware
// save. View mode renders the read-only DashboardRenderer; edit mode renders the
// react-rnd canvas + the widget config panel. Editing is gated on dashboard:write
// (the server enforces it too — this just hides a button the caller can't use).

import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ArrowLeft, Download, FlaskConical, History, Plus, Trash2 } from 'lucide-react';
import { hasAuthority } from '@devicechain/client';
import {
  addWidget,
  bindWidgetSlot,
  clearWidgetDatasource,
  DashboardHub,
  effectiveBindings,
  isDirty,
  migrateToSlots,
  parseDashboardDefinition,
  pruneSlots,
  resolveConcrete,
  serializeDefinition,
  setTitle,
  stripDefaultBindings,
  updateWidget,
  widgetSlotName,
  SyntheticDataSource,
  SYNTHETIC_GENERATORS,
  WIDGET_TYPES,
  type ConcreteSelector,
  type DashboardDefinition,
  type DeviceResolver,
  type SlotBinding,
  type SyntheticGenerator,
  type WidgetType,
} from '@devicechain/dashboards';
import { DashboardRenderer } from '@devicechain/widgets';
import { PageShell } from '@/components/ui/page-shell';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Combobox } from '@/components/ui/combobox';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useAuth } from '@/auth/AuthProvider';
import { updateDashboard, deleteDashboard, CONFLICT_MARKER } from '@/lib/api/dashboards';
import { errMessage } from '@/routes/common';
import { DashboardCanvas } from './DashboardCanvas';
import { WidgetConfigPanel } from './WidgetConfigPanel';
import { VersionHistorySheet } from './VersionHistorySheet';

type SaveState = { kind: 'clean' } | { kind: 'saving' } | { kind: 'error'; message: string };

// useDebouncedValue returns `value`, but only advances the returned value `ms` after
// `key` stops changing (rapid changes reset the timer). The initial value applies
// immediately (no first-load delay). Used to coalesce rapid slot-binding edits before
// the expensive hub reconstruction.
function useDebouncedValue<T>(value: T, key: string, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  const latest = useRef(value);
  latest.current = value;
  useEffect(() => {
    const t = setTimeout(() => setDebounced(latest.current), ms);
    return () => clearTimeout(t);
  }, [key, ms]);
  return debounced;
}

// applyDatasource maps a config-panel edit (a slot-free device/anchor view, or None)
// back to slot storage: bind the widget to the slot for that entity (creating/reusing
// it), or clear it — then prune any slot no widget references anymore.
function applyDatasource(
  def: DashboardDefinition,
  widgetId: string,
  ds: ConcreteSelector | undefined,
): DashboardDefinition {
  if (!ds) return pruneSlots(clearWidgetDatasource(def, widgetId));
  // Only slot a COMPLETE binding (an entity has been chosen). While a selector is being
  // filled in (e.g. an anchor with no target picked yet), keep it as a concrete
  // selector — so the partial config isn't lost on save/reload, and so we don't churn
  // the slot manifest / rebuild the hub on every keystroke of the relationship field.
  // It becomes a default-bound slot as soon as it's complete.
  const token = ds.kind === 'device' ? ds.deviceToken : ds.anchor.targetToken;
  if (!token) {
    const widgets = def.widgets.map((w) => (w.id === widgetId ? { ...w, datasource: ds } : w));
    return pruneSlots({ ...def, widgets });
  }
  const binding: SlotBinding =
    ds.kind === 'device'
      ? { kind: 'device', deviceToken: ds.deviceToken }
      : { kind: 'anchor', anchor: ds.anchor };
  return pruneSlots(bindWidgetSlot(def, widgetId, binding, ds.measurements));
}

export function DashboardWorkspace({
  token,
  name,
  description,
  updatedAt,
  loaded,
  resolver,
}: {
  token: string;
  name: string | null;
  description: string | null;
  updatedAt: string | null;
  loaded: DashboardDefinition;
  resolver: DeviceResolver;
}) {
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  const canEdit = hasAuthority(claims, 'dashboard:write');

  const [mode, setMode] = useState<'view' | 'edit'>('view');
  const [working, setWorking] = useState<DashboardDefinition>(loaded);
  const [saved, setSaved] = useState<DashboardDefinition>(loaded);
  const [saveState, setSaveState] = useState<SaveState>({ kind: 'clean' });
  // Selection is owned here (not in the canvas) so the config panel and the canvas
  // stay in sync; leaving edit mode clears it.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // The optimistic-concurrency baseline: the updatedAt the editor last observed.
  // Advanced after every save/rollback so a subsequent save doesn't self-conflict.
  const [expectedUpdatedAt, setExpectedUpdatedAt] = useState<string | null>(updatedAt);
  const [historyOpen, setHistoryOpen] = useState(false);
  // Preview mode swaps the live hub for a client-side synthetic source so the author
  // can validate layout/scales/thresholds before any device reports. It's a view
  // concern only — it never touches the definition, so it can't affect dirty/save.
  const [preview, setPreview] = useState(false);
  const [generator, setGenerator] = useState<SyntheticGenerator>('sine');
  const synthetic = useMemo(() => new SyntheticDataSource({ generator }), [generator]);
  useEffect(() => () => synthetic.disposeAll(), [synthetic]);

  // The live slot manifest: each slot's default binding (the console adds no host
  // override — that's the /dash embedder's job, PR I-3). Its JSON key changes ONLY
  // when a slot's binding changes (not on layout edits, which recompute an equal
  // manifest). The hub is CONSTRUCTED WITH these bindings and re-created when they
  // change, so widgets always subscribe against current bindings (constructing-with,
  // rather than a post-mount setBindings, avoids a child-effect-before-parent-effect
  // race). A new hub instance also makes useMeasurementStream resubscribe — that's
  // the live rebind. Torn down when replaced so streams don't leak.
  const bindings = useMemo(() => effectiveBindings(working), [working]);
  const bindingsKey = useMemo(() => JSON.stringify(bindings), [bindings]);
  // Debounce the manifest that drives the (expensive) hub reconstruction so rapid
  // binding edits (e.g. typing an anchor relationship) coalesce into ONE rebuild rather
  // than tearing down every widget's stream per keystroke.
  const hubBindings = useDebouncedValue(bindings, bindingsKey, 250);
  const hubKey = useMemo(() => JSON.stringify(hubBindings), [hubBindings]);
  // The hub carries the viewer's authorities so action widgets (alarm ack/clear) can
  // gate their controls; the server enforces alarm:write regardless.
  const authorities = claims?.authorities;
  const liveHub = useMemo(
    () => new DashboardHub({ resolver, bindings: hubBindings, authorities }),
    // resolver/bindings read via hubKey, authorities via its stringified value.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [resolver, hubKey, JSON.stringify(authorities ?? [])],
  );
  useEffect(() => () => liveHub.disposeAll(), [liveHub]);
  const dataHub = preview ? synthetic : liveHub;

  const dirty = isDirty(working, saved);
  const selected = working.widgets.find((w) => w.id === selectedId) ?? null;
  const heading = working.title || name || token;

  // Warn before a tab close/reload discards unsaved edits. Only armed while dirty.
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty]);

  // A stale save-error otherwise sticks until the next save attempt (replacing the
  // "unsaved changes" hint and going invisible in view mode). Clear it as soon as
  // the user edits again — a new working copy means the failed save is moot.
  useEffect(() => {
    setSaveState((s) => (s.kind === 'error' ? { kind: 'clean' } : s));
  }, [working]);

  // In-app navigation (BackLink / sidebar) unmounts this workspace and drops
  // unsaved edits — beforeunload only covers tab close. The console mounts a
  // BrowserRouter (no data router → no useBlocker), so guard the one back-nav this
  // component owns with a confirm. Sidebar nav mid-edit is the residual gap
  // (needs a data-router migration) — mitigated by keeping the unsaved indicator
  // and Save reachable in view mode below.
  const leaveGuarded = async (to: string) => {
    if (
      dirty &&
      !(await confirm({
        title: 'Discard unsaved changes?',
        description: 'Your unsaved dashboard edits will be lost.',
        confirmLabel: 'Discard',
      }))
    )
      return;
    navigate(to);
  };

  const addWidgetOfType = (type: WidgetType) => {
    const { definition, id } = addWidget(working, type);
    setWorking(definition);
    setSelectedId(id); // open the new widget's config panel
  };

  const toggleMode = () => {
    setSelectedId(null);
    setMode(mode === 'edit' ? 'view' : 'edit');
  };

  const save = () => {
    const snapshot = working; // persist exactly what we serialize; later edits stay dirty
    setSaveState({ kind: 'saving' });
    // name tracks the (editable) title so a save never orphans the list label;
    // description is preserved verbatim (the editor doesn't edit it). expectedUpdatedAt
    // is the optimistic-concurrency precondition — a stale one fails with CONFLICT.
    updateDashboard(token, {
      name: snapshot.title || null,
      description,
      definition: serializeDefinition(snapshot),
      expectedUpdatedAt,
    })
      .then((result) => {
        setSaved(snapshot);
        setExpectedUpdatedAt(result.updatedAt); // advance the baseline for the next save
        setSaveState({ kind: 'clean' });
        toast('Dashboard saved');
      })
      .catch((err: unknown) => {
        const raw = errMessage(err);
        const message = raw.includes(CONFLICT_MARKER)
          ? 'This dashboard changed elsewhere. Reload the page to get the latest, then reapply your edits.'
          : raw;
        setSaveState({ kind: 'error', message });
      });
  };

  // After a rollback the server draft IS the chosen version; re-baseline working +
  // saved to it (seeding the title from the name like the initial load) so the
  // editor reflects it and isn't spuriously dirty, and advance the concurrency
  // baseline to the returned updatedAt.
  const onRolledBack = (result: { definition: string; updatedAt: string | null }) => {
    try {
      let def = parseDashboardDefinition(JSON.parse(result.definition));
      // Seed the title from the CURRENT effective name (the last-saved title, which
      // tracks renames) rather than the mount-time `name` prop — otherwise rolling
      // back to a title-less version and re-saving would silently revert a rename.
      const currentName = saved.title || name || '';
      if (def.title === '' && currentName) def = setTitle(def, currentName);
      def = migrateToSlots(def); // keep the editor slot-based (a version may be concrete)
      setWorking(def);
      setSaved(def);
      setSelectedId(null);
      setExpectedUpdatedAt(result.updatedAt);
      setSaveState({ kind: 'clean' });
    } catch {
      toast('Rolled back, but the returned version could not be parsed. Reload the page.', 'error');
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: 'Delete dashboard',
        description: `Delete “${token}”? This cannot be undone.`,
        confirmLabel: 'Delete',
      }))
    )
      return;
    try {
      await deleteDashboard(token);
      toast(`Dashboard “${token}” deleted`);
      navigate('/dashboards');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };

  // Shown in BOTH modes so an unsaved edit is never disguised: after Done, view
  // mode still renders `working` (the dirty copy), so without this the user sees
  // their edits as if saved and loses them on nav (the review's top finding).
  const statusEl =
    saveState.kind === 'error' ? (
      <span className="text-sm text-destructive">{saveState.message}</span>
    ) : dirty ? (
      <span className="text-sm text-muted-foreground">Unsaved changes</span>
    ) : null;

  const saveButton = (
    <Button size="sm" onClick={save} disabled={!dirty || saveState.kind === 'saving'}>
      {saveState.kind === 'saving' ? 'Saving…' : 'Save'}
    </Button>
  );

  const historyButton = (
    <Button variant="outline" size="sm" onClick={() => setHistoryOpen(true)}>
      <History size={14} /> History
    </Button>
  );

  // Export the current definition (ADR-039). A plain export keeps the slot default
  // bindings (renders as-is); "as template" strips them so a host must supply a binding
  // manifest (the reference /dash viewer). Pretty-printed for a readable file/paste.
  const exportJson = (template: boolean) =>
    JSON.stringify(template ? stripDefaultBindings(working) : working, null, 2);
  const copyExport = async (template: boolean) => {
    // navigator.clipboard is undefined on a non-secure context (plain http over a LAN
    // IP) — the optional chain would resolve silently and falsely claim "copied".
    if (!navigator.clipboard) {
      toast('Clipboard unavailable here — use Download instead.', 'error');
      return;
    }
    try {
      await navigator.clipboard.writeText(exportJson(template));
      toast(template ? 'Template (unbound) copied' : 'Definition copied');
    } catch (err) {
      toast(errMessage(err), 'error');
    }
  };
  const downloadExport = () => {
    const blob = new Blob([exportJson(false)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${token}.dashboard.json`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const exportMenu = (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm">
          <Download size={14} /> Export
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={downloadExport}>Download JSON</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void copyExport(false)}>Copy JSON</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void copyExport(true)}>
          Copy as template (unbound)
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );

  // Preview toggle + (when on) a generator picker. Shown in both modes so an author
  // can lay out against synthetic data or review it.
  const previewControl = (
    <div className="flex items-center gap-2">
      {preview && (
        <Combobox
          options={SYNTHETIC_GENERATORS.map((g) => ({ value: g.value, label: g.label }))}
          value={generator}
          onChange={(v) => setGenerator(v as SyntheticGenerator)}
          allowClear={false}
          className="h-9 w-40"
        />
      )}
      <Button
        variant={preview ? 'default' : 'outline'}
        size="sm"
        aria-pressed={preview}
        onClick={() => setPreview((p) => !p)}
        title="Preview with synthetic data (no device required)"
      >
        <FlaskConical size={14} /> Preview
      </Button>
    </div>
  );

  const viewActions = (
    <div className="flex items-center gap-2">
      {statusEl}
      {/* Reachable so edits carried into view mode can be persisted without
          re-entering the editor. */}
      {dirty && saveButton}
      {previewControl}
      {exportMenu}
      {historyButton}
      <Button variant="outline" size="sm" onClick={toggleMode} disabled={!canEdit}>
        Edit
      </Button>
      <Button variant="destructive" size="sm" onClick={remove} disabled={!canEdit}>
        <Trash2 size={14} /> Delete
      </Button>
    </div>
  );

  const editActions = (
    <div className="flex items-center gap-2">
      {statusEl}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm">
            <Plus size={14} /> Add widget
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {WIDGET_TYPES.map((type) => (
            <DropdownMenuItem key={type} onSelect={() => addWidgetOfType(type)}>
              {type}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      {previewControl}
      {exportMenu}
      {historyButton}
      {saveButton}
      <Button variant="outline" size="sm" onClick={toggleMode}>
        Done
      </Button>
    </div>
  );

  return (
    <>
    <PageShell
      title={heading}
      banner="dashboard"
      description={
        <div className="mt-1">
          <button
            onClick={() => void leaveGuarded('/dashboards')}
            className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft size={14} /> Dashboards
          </button>
        </div>
      }
      action={mode === 'edit' ? editActions : viewActions}
      subHeader={
        mode === 'edit' ? (
          <div className="flex items-center gap-3 px-6 py-2">
            <label htmlFor="dash-title" className="text-sm text-muted-foreground">
              Title
            </label>
            <Input
              id="dash-title"
              value={working.title}
              onChange={(e) => setWorking(setTitle(working, e.target.value))}
              placeholder="Dashboard title"
              className="max-w-sm"
            />
          </div>
        ) : undefined
      }
      fullBleed={mode === 'edit'}
    >
      {mode === 'edit' ? (
        <div className="flex h-full min-h-0">
          <div className="min-w-0 flex-1">
            <DashboardCanvas
              definition={working}
              // Prune slots orphaned by a widget delete (canvas edits go through here);
              // a no-op for move/resize since those keep every widget's slot reference.
              onChange={(next) => setWorking(pruneSlots(next))}
              hub={dataHub}
              actions={dataHub}
              selectedId={selectedId}
              onSelect={setSelectedId}
            />
          </div>
          {selected && (
            <WidgetConfigPanel
              // Remount per widget so the config panel's local input buffers (e.g.
              // the measurements text field) don't carry one widget's keystrokes
              // onto the next when both take the same datasource branch.
              key={selected.id}
              widget={selected}
              datasource={resolveConcrete(working, selected)}
              slotName={widgetSlotName(selected)}
              onChange={(next) => setWorking(updateWidget(working, next.id, next))}
              onDatasource={(ds) => setWorking(applyDatasource(working, selected.id, ds))}
              onClose={() => setSelectedId(null)}
            />
          )}
        </div>
      ) : (
        // DashboardRenderer's root fills 100% width/height, so it needs a bounded
        // container to give it real height inside the page flow.
        <div style={{ position: 'relative', height: 'calc(100vh - 180px)', minHeight: 400 }}>
          <DashboardRenderer
            definition={working}
            hub={dataHub}
            actions={dataHub}
            seedHistory={!preview}
            bindings={hubBindings}
          />
        </div>
      )}
    </PageShell>
    <VersionHistorySheet
      token={token}
      open={historyOpen}
      onOpenChange={setHistoryOpen}
      dirty={dirty}
      saving={saveState.kind === 'saving'}
      canWrite={canEdit}
      expectedUpdatedAt={expectedUpdatedAt}
      onRolledBack={onRolledBack}
    />
    </>
  );
}
