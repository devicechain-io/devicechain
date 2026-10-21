// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// DashboardWorkspace — the view/edit host for a single dashboard in the console
// (ADR-039 amendment: authoring re-homes here from the standalone /dash app). It
// owns the working copy of the definition, the view/edit mode, and the dirty-aware
// save. View mode renders the read-only DashboardRenderer; edit mode renders the
// cell-snapped canvas + the widget config panel. Editing is gated on dashboard:write
// (the server enforces it too — this just hides a button the caller can't use).

import { useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ArrowLeft, ClipboardPaste, Download, FlaskConical, History, Redo2, Trash2, Undo2 } from 'lucide-react';
import { mayWrite } from '@/lib/scope';
import {
  addWidget,
  bindWidgetSlot,
  clearWidgetDatasource,
  copyWidget,
  createEntityLister,
  duplicateWidget,
  DEFAULT_CANVAS_DENSITY,
  effectiveBindings,
  fitToLimits,
  isDirty,
  migrateToSlots,
  parseDashboardDefinition,
  type DashboardDefinitionError,
  parseWidgetClipboard,
  pasteWidgets,
  pruneSlots,
  resolveConcrete,
  serializeDefinition,
  serializeWidgetClipboard,
  setCanvasGrid,
  setCanvasDensity,
  setBoardTimeRange,
  setCanvasSizing,
  setSlotScope,
  setTitle,
  UNKNOWN_WIDGET_TYPE,
  isResolvedWidget,
  updateWidget,
  widgetSlotName,
  withoutConversionMark,
  gridLengthPx,
  DEFAULT_TIME_RANGE_SECONDS,
  presetIdOf,
  spanLabel,
  SyntheticDataSource,
  SYNTHETIC_GENERATORS,
  TIME_RANGE_PRESETS,
  type CanvasDensity,
  type CanvasSizing,
  type ConcreteSelector,
  type DashboardDefinition,
  type DeviceResolver,
  type SlotBinding,
  type SyntheticGenerator,
  type UnboundPaste,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import {
  BoundDevicesNotice,
  DashboardRenderer,
  TimeRangeBar,
  describeBoundDevicesProblems,
  useBoundDevices,
  stripUnknownOptions,
  useBoardTimeRange,
  useDashboardHub,
  useResolvedBindings,
  useSlotCandidates,
} from '@devicechain/widgets';
import { ErrorState } from '@/components/ui/error-state';
import { PageShell } from '@/components/ui/page-shell';
import { CopyToken } from '@/components/ui/copy-token';
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
import { UnknownWidgetPanel, WidgetConfigPanel } from './WidgetConfigPanel';
import { VersionHistorySheet } from './VersionHistorySheet';
import { AddWidgetMenu } from './AddWidgetMenu';
import { publishLayoutBlockers, spanLimitsFor } from './layoutFindings';
import { WorkspaceConnection } from './WorkspaceConnection';
import { exportDefinitionJson } from './exportDefinition';
import { DashboardImportDrawer } from '../import/DashboardImportDrawer';
import { defaultWidgetRegistry, type EditorWidgetRegistry } from './widgetRegistry';
import { useEditHistory, type EditAction, type EditLabel } from './useEditHistory';
import { editorShortcut, isEditorSurfaceTarget } from './editorShortcuts';
import { readWidgetClipboard, writeWidgetClipboard } from './widgetClipboard';

type SaveState = { kind: 'clean' } | { kind: 'saving' } | { kind: 'error'; message: string };

// useDebouncedValue returns `value`, but only advances the returned value `ms` after
// `key` stops changing (rapid changes reset the timer). The initial value applies
// immediately (no first-load delay). Used in edit mode to coalesce rapid slot-binding
// edits (typing an anchor) into one re-subscription of the affected widgets.
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

// CanvasSettings — the grid-level knobs: the column count, the smallest cell the board renders
// at before it reflows, the gutter between cells and the padding around the board (both px,
// scaled by the density), and how the board sizes into its container (fill / fixed width /
// fixed height). A compact bar next to the title. A gutter or padding authored as a spacing
// token or per axis/side shows here as its px value; typing one writes a single px value.
function CanvasSettings({
  definition,
  onChange: commit,
}: {
  definition: DashboardDefinition;
  // `field` names the setting, so typing into one number box is one undo step.
  onChange: (next: DashboardDefinition, field: string) => void;
}) {
  const { t } = useTranslation('dashboards');
  const { grid, sizing } = definition.canvas;
  const gutter = gridLengthPx(typeof grid.gutter === 'object' ? grid.gutter.x : grid.gutter);
  const padding = gridLengthPx(typeof grid.padding === 'object' ? grid.padding.left : grid.padding);
  const boardSpan = definition.timeRange?.defaultSeconds ?? DEFAULT_TIME_RANGE_SECONDS;
  const mode: 'fill' | 'width' | 'height' = sizing === 'fill' ? 'fill' : 'width' in sizing ? 'width' : 'height';
  const fixedPx = sizing === 'fill' ? 1200 : 'width' in sizing ? sizing.width : sizing.height;

  const setMode = (next: 'fill' | 'width' | 'height') => {
    const value: CanvasSizing =
      next === 'fill' ? 'fill' : next === 'width' ? { width: fixedPx } : { height: fixedPx };
    commit(setCanvasSizing(definition, value), 'sizing');
  };
  const setFixedPx = (px: number) =>
    commit(setCanvasSizing(definition, mode === 'width' ? { width: px } : { height: px }), 'sizing-px');

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <NumberField
        label={t('workspaceColumns')}
        value={grid.columns}
        min={1}
        onChange={(v) => commit(setCanvasGrid(definition, { columns: v }), 'columns')}
      />
      <NumberField
        label={t('workspaceMinCellSize')}
        value={grid.minCellSize}
        min={1}
        onChange={(v) => commit(setCanvasGrid(definition, { minCellSize: v }), 'minCellSize')}
      />
      <NumberField
        label={t('workspaceGutter')}
        value={gutter}
        min={0}
        onChange={(v) => commit(setCanvasGrid(definition, { gutter: v }), 'gutter')}
      />
      <NumberField
        label={t('workspacePadding')}
        value={padding}
        min={0}
        onChange={(v) => commit(setCanvasGrid(definition, { padding: v }), 'padding')}
      />
      <div className="flex items-center gap-2">
        <label className="text-sm text-muted-foreground">{t('workspaceSize')}</label>
        <Combobox
          options={[
            // 'fill' | 'width' | 'height' are the CanvasSizing mode discriminants, not
            // user text — the visible copy is the `label` alongside each.
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'fill', label: t('workspaceSizeFill') },
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'width', label: t('workspaceSizeWidth') },
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'height', label: t('workspaceSizeHeight') },
          ]}
          value={mode}
          onChange={(v) => setMode(v as 'fill' | 'width' | 'height')}
          allowClear={false}
          className="h-9 w-36"
        />
        {mode !== 'fill' && (
          <NumberField label={t('workspacePx')} value={fixedPx} min={1} onChange={setFixedPx} />
        )}
      </div>
      <div className="flex items-center gap-2">
        <label className="text-sm text-muted-foreground">{t('workspaceTimeRange')}</label>
        <Combobox
          options={[
            // A default that is none of the presets (set by import) is still the board's span: it
            // is listed under its own label rather than silently reading as another preset.
            ...(presetIdOf(boardSpan) === undefined ? [{ value: String(boardSpan), label: spanLabel(boardSpan) }] : []),
            ...TIME_RANGE_PRESETS.map((p) => ({ value: String(p.seconds), label: p.id })),
          ]}
          value={String(boardSpan)}
          onChange={(v) => commit(setBoardTimeRange(definition, Number(v)), 'time-range')}
          allowClear={false}
          className="h-9 w-24"
        />
      </div>
      <div className="flex items-center gap-2">
        <label className="text-sm text-muted-foreground">{t('workspaceDensity')}</label>
        <Combobox
          options={[
            // The values are the CanvasDensity discriminants, not user text.
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'compact', label: t('workspaceDensityCompact') },
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'comfortable', label: t('workspaceDensityComfortable') },
            // eslint-disable-next-line i18next/no-literal-string
            { value: 'wall', label: t('workspaceDensityWall') },
          ]}
          value={definition.canvas.density ?? DEFAULT_CANVAS_DENSITY}
          onChange={(v) => commit(setCanvasDensity(definition, v as CanvasDensity), 'density')}
          allowClear={false}
          className="h-9 w-36"
        />
      </div>
    </div>
  );
}

// NumberField — a labelled compact integer input that only commits finite values (a
// transient empty/NaN input keeps the last good value rather than writing a 0/NaN box).
function NumberField({
  label,
  value,
  min,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  onChange: (v: number) => void;
}) {
  return (
    <div className="flex items-center gap-2">
      <label className="whitespace-nowrap text-sm text-muted-foreground">{label}</label>
      <Input
        type="number"
        min={min}
        value={value}
        onChange={(e) => {
          // An empty field is a transient edit state, not a value — committing it
          // would read `Number('') === 0` and (for a min-0 field like Gap) snap 0 back
          // into the box the user just cleared. Only commit a real in-range number.
          if (e.target.value === '') return;
          const v = Number(e.target.value);
          if (Number.isFinite(v) && v >= min) onChange(v);
        }}
        className="h-9 w-20"
      />
    </div>
  );
}

// isSlotScoped reports whether a widget's slot is context-driven (scoped) — the config
// panel then shows its datasource read-only and applyDatasource ignores edits to it.
function isSlotScoped(def: DashboardDefinition, slotName: string | undefined): boolean {
  return !!(
    slotName &&
    def.slots &&
    Object.prototype.hasOwnProperty.call(def.slots, slotName) &&
    def.slots[slotName].scope
  );
}

// applyDatasource maps a config-panel edit (a slot-free device/anchor view, or None)
// back to slot storage: bind the widget to the slot for that entity (creating/reusing
// it), or clear it — then prune any slot no widget references anymore.
function applyDatasource(
  def: DashboardDefinition,
  widgetId: string,
  ds: ConcreteSelector | undefined,
): DashboardDefinition {
  // A widget bound through a SCOPED slot is context-driven: its entity is derived by the
  // cascade, not chosen here (scope authoring is a later slice). Ignore datasource edits
  // for it so a panel interaction — including the partial-selector path below, which
  // would sever the slot reference before bindWidgetSlot's scope-guard runs — can't
  // silently destroy the hierarchy. The config panel shows this datasource read-only, so
  // this is defense in depth, not the only guard.
  const current = def.widgets.find((w) => w.id === widgetId)?.datasource;
  const currentSlot = current?.kind === 'slot' ? current.slot : undefined;
  if (
    currentSlot &&
    def.slots &&
    Object.prototype.hasOwnProperty.call(def.slots, currentSlot) &&
    def.slots[currentSlot].scope
  ) {
    return def;
  }
  if (!ds) return pruneSlots(clearWidgetDatasource(def, widgetId));
  // Only slot a COMPLETE binding (an entity has been chosen). While a selector is being
  // filled in (e.g. an anchor with no target picked yet), keep it as a concrete
  // selector — so the partial config isn't lost on save/reload, and so we don't churn
  // the slot manifest / rebuild the hub on every keystroke of the relationship field.
  // It becomes a default-bound slot as soon as it's complete.
  const token = ds.kind === 'device' ? ds.deviceToken : ds.anchor.targetToken;
  if (!token) {
    const widgets = def.widgets.map((w) =>
      w.id === widgetId && w.type !== UNKNOWN_WIDGET_TYPE ? { ...w, datasource: ds } : w,
    );
    return pruneSlots({ ...def, widgets });
  }
  const binding: SlotBinding =
    ds.kind === 'device'
      ? { kind: 'device', deviceToken: ds.deviceToken }
      : { kind: 'anchor', anchor: ds.anchor };
  // `ds.location` names the location series the widget reads (a map's), and must cross
  // into the slot selector with the measurement names: slotting moves the ENTITY onto a
  // slot, and must not change WHAT the widget reads from it.
  return pruneSlots(bindWidgetSlot(def, widgetId, binding, ds.measurements, ds.location));
}

// A widget's name as an author recognises it: its title option, else its type.
function widgetName(widget: WidgetInstance | undefined): string {
  if (!widget) return '';
  const title = widget.options?.title;
  return typeof title === 'string' && title.trim() !== '' ? title.trim() : widget.type;
}

// The undo step a settings edit belongs to: the widget, and which of its settings changed. Typing
// into one field changes the same setting on every keystroke, so the keystrokes are one step;
// moving to another field opens a new one.
function settingsKey(before: WidgetInstance | undefined, after: WidgetInstance): string {
  const changed: string[] = [];
  const a = before?.options ?? {};
  const b = after.options ?? {};
  for (const k of new Set([...Object.keys(a), ...Object.keys(b)])) {
    if (JSON.stringify(a[k]) !== JSON.stringify(b[k])) changed.push(`options.${k}`);
  }
  for (const k of ['frame', 'datasource', 'widget'] as const) {
    if (JSON.stringify(before?.[k]) !== JSON.stringify(after[k])) changed.push(k);
  }
  return `settings:${after.id}:${changed.sort().join(',')}`;
}

// The notice for each reason a paste left a widget unbound.
const PASTE_UNBOUND_MESSAGE: Record<UnboundPaste['reason'], string> = {
  missing: 'pasteUnboundMissing',
  different: 'pasteUnboundDifferent',
  tenant: 'pasteUnboundTenant',
};

// The slot a pasted widget still waits on, while it does: it references the slot the paste gave it
// and nothing has been bound to that slot since. Read from the working copy, so the explanation
// goes as soon as the author binds the widget, deletes it, or undoes the paste, and comes back
// with a redo.
function stillUnbound(def: DashboardDefinition, n: UnboundPaste): boolean {
  const w = def.widgets.find((x) => x.id === n.widgetId);
  if (!w) return false;
  const refers = (w.datasource?.kind === 'slot' && w.datasource.slot === n.slot) || w.options?.selectionTarget === n.slot;
  const slot = def.slots && Object.prototype.hasOwnProperty.call(def.slots, n.slot) ? def.slots[n.slot] : undefined;
  return refers && slot !== undefined && slot.defaultBinding === undefined;
}

// 🔴 THE EDITOR NO LONGER TAKES A `description`, AND THE ABSENCE IS THE FIX.
//
// It never edited one. It took the prop only so a save could send it back verbatim,
// because updateDashboard was a full replace and a field left out was ERASED. Under
// partial updates a field left out is left alone, so the safe thing to do with a value
// this screen does not own is not to mention it — carrying it through would mean a save
// writing a description read at mount over one someone else edited since.
export function DashboardWorkspace({
  token,
  name,
  updatedAt,
  loaded,
  resolver,
  registry = defaultWidgetRegistry(),
}: {
  token: string;
  name: string | null;
  updatedAt: string | null;
  loaded: DashboardDefinition;
  resolver: DeviceResolver;
  // The widgets this console can author. Defaults to the built-ins; a host composing extra
  // widgets passes the registry it composed, and the menu and settings follow it.
  registry?: EditorWidgetRegistry;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const navigate = useNavigate();
  const { toast } = useToast();
  const confirm = useConfirm();
  const { claims } = useAuth();
  // A member limited to part of the tenant is served only the published version, so they
  // view even when their role names dashboard:write.
  const canEdit = mayWrite(claims, 'dashboard:write');

  const [mode, setMode] = useState<'view' | 'edit'>('view');
  // The working copy, as an undo/redo history (see useEditHistory for when it resets).
  const [working, history] = useEditHistory(loaded);
  // What the last undo, redo, duplicate, copy or paste did, for a screen reader.
  const [announcement, setAnnouncement] = useState('');
  // Widgets pasted without their binding, and why: shown until each is bound (see stillUnbound).
  const [pasteNotices, setPasteNotices] = useState<(UnboundPaste & { name: string })[]>([]);
  // The viewer's time range and pause. Held here (not in the renderer) so the control sits in the
  // page header; it starts at the board's own default span and follows the author changing it.
  const [timeRange, timeRangeControls] = useBoardTimeRange(working.timeRange?.defaultSeconds);
  // The view-driven selection overlay (ADR-039 selection amendment): an alarm-originator
  // drill (and, in PR2b, a context-selector) accumulates slot→binding picks here.
  // Cleared on mode switch so the author sees their own bindings, not a stale drill.
  //
  // It also follows the address: a `?bind.<slot>=<device>` link opens the board with that device
  // bound (checked against the caller's own scope first), and a pick rewrites the address by
  // REPLACING the history entry. See useBoundDevices for the reasoning.
  const location = useLocation();
  const address = useMemo(
    () => ({
      search: location.search,
      replaceSearch: (search: string) => navigate({ search: search ? `?${search}` : '' }, { replace: true }),
    }),
    [location.search, navigate],
  );
  const {
    selection,
    select,
    reset: resetSelection,
    phase: linkPhase,
    problems: linkProblems,
    dismiss: dismissLink,
    retry: retryLink,
  } = useBoundDevices({ definition: working, resolver, address, enabled: mode === 'view' });
  const [saved, setSaved] = useState<DashboardDefinition>(loaded);
  const [saveState, setSaveState] = useState<SaveState>({ kind: 'clean' });
  // Selection is owned here (not in the canvas) so the config panel and the canvas
  // stay in sync; leaving edit mode clears it.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // The optimistic-concurrency baseline: the updatedAt the editor last observed.
  // Advanced after every save/rollback so a subsequent save doesn't self-conflict.
  const [expectedUpdatedAt, setExpectedUpdatedAt] = useState<string | null>(updatedAt);
  const [historyOpen, setHistoryOpen] = useState(false);
  // A rollback landed on a board saved by a newer release: nothing here may draw or save it.
  const [newerBoard, setNewerBoard] = useState(false);
  // Clone hands the board on screen to the import flow: a new token, checked, saved as a draft.
  const [cloneOpen, setCloneOpen] = useState(false);
  // Preview mode swaps the live hub for a client-side synthetic source so the author
  // can validate layout/scales/thresholds before any device reports. It's a view
  // concern only — it never touches the definition, so it can't affect dirty/save.
  const [preview, setPreview] = useState(false);
  const [generator, setGenerator] = useState<SyntheticGenerator>('sine');
  const synthetic = useMemo(() => new SyntheticDataSource({ generator }), [generator]);
  useEffect(() => () => synthetic.disposeAll(), [synthetic]);

  // The live slot manifest. base = each slot's default binding (the console adds no host
  // override — that's the /dash embedder's job, PR I-3); useResolvedBindings runs the
  // scoped-slot cascade over it, resolving each scoped slot from its parent + the
  // selection overlay and returning the settled manifest (a scope-free dashboard passes
  // straight through, no async). The RENDERER resolves every widget's slot through it, so
  // a drill selection re-subscribes only the widgets bound to the re-pointed slot.
  const base = useMemo(() => effectiveBindings(working), [working]);
  const bindings = useResolvedBindings(working, base, selection, resolver);
  const bindingsKey = useMemo(() => JSON.stringify(bindings), [bindings]);
  // The context/entity-selector candidate provider (ADR-039 selection amendment): a
  // scoped slot's members come from the resolver, a root slot's from the tenant entity
  // lister. Rebuilt with the resolved bindings so a parent switch refreshes a member
  // picker. Passed to the view-mode renderer only (edit mode's canvas wires no selection).
  const entityLister = useMemo(() => createEntityLister(), []);
  const candidates = useSlotCandidates(working, bindings, resolver, entityLister);
  // EDIT MODE ONLY: debounce the manifest the canvas resolves through, so typing an anchor
  // relationship in the config panel re-subscribes the affected widgets once rather than
  // per keystroke. View mode takes `bindings` undebounced: a selection must move the board
  // at once, and it no longer rebuilds anything that a debounce would protect.
  const editBindings = useDebouncedValue(bindings, bindingsKey, 250);
  // ONE live hub per (resolver, authorities). It holds no bindings, so neither a selection
  // nor a binding edit rebuilds it. It carries the viewer's authorities so action widgets
  // (alarm ack/clear) can gate their controls; the server enforces alarm:write regardless.
  const liveHub = useDashboardHub(resolver, claims?.authorities);
  const dataHub = preview ? synthetic : liveHub;

  const dirty = isDirty(working, saved);
  // The board was stored in the previous grid format and its layout was mapped onto the current
  // one approximately when it loaded. Saving writes the current format, which is what retires the
  // notice, so a converted board can be saved even before anything else is edited.
  const converted = working.convertedFrom !== undefined;
  // The charts whose sample-count window had no span to become: they now follow the board's time
  // range. Named by title (else id) so the author knows which ones to look at.
  const convertedCharts = (working.convertedWindows ?? []).map((id) => {
    const w = working.widgets.find((x) => x.id === id);
    const title = w && w.type !== UNKNOWN_WIDGET_TYPE ? (w.options as { title?: unknown } | undefined)?.title : undefined;
    return typeof title === 'string' && title !== '' ? title : id;
  });
  const selected = working.widgets.find((w) => w.id === selectedId) ?? null;
  const displayName = working.title || name;
  const heading = displayName || token;

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
        title: t('workspaceDiscardTitle'),
        description: t('workspaceDiscardDescription'),
        confirmLabel: t('workspaceDiscardConfirm'),
      }))
    )
      return;
    navigate(to);
  };

  const addWidgetOfType = (descriptor: WidgetDescriptor) => {
    const { definition, id } = addWidget(working, descriptor);
    history.commit(definition, { action: 'add', name: widgetName(definition.widgets.find((w) => w.id === id)) });
    setSelectedId(id); // open the new widget's config panel
  };

  // ---- undo, redo, duplicate, copy and paste ------------------------------------------

  const describeEdit = (label: EditLabel) => t(`historyEdit_${label.action satisfies EditAction}`, { name: label.name ?? '' });
  const undo = () => {
    const label = history.undo();
    if (label) setAnnouncement(t('historyUndid', { edit: describeEdit(label) }));
  };
  const redo = () => {
    const label = history.redo();
    if (label) setAnnouncement(t('historyRedid', { edit: describeEdit(label) }));
  };

  const duplicate = (id: string) => {
    const result = duplicateWidget(working, id);
    if (!result) return;
    const name = widgetName(working.widgets.find((w) => w.id === id));
    history.commit(result.definition, { action: 'duplicate', name });
    setSelectedId(result.id);
    setAnnouncement(t('announceDuplicated', { name }));
  };

  // Where a copy is made and a paste lands: this board, in the tenant the console is acting in.
  // The tenant is read from the token at the moment of the copy or paste, never cached: a tab that
  // changes tenant keeps its clipboard, and the stamp is what stops a widget copied in one tenant
  // from keeping its bindings in another (see pasteWidgets).
  const here = { tenant: claims?.tenant ?? '', board: token };

  // The clipboard text for widget `id`, recording this board and tenant as where it came from.
  const clipboardText = (id: string): string | undefined => {
    const clip = copyWidget(working, id, here);
    return clip && serializeWidgetClipboard(clip);
  };
  const copied = (id: string, text: string) => {
    writeWidgetClipboard(text);
    const name = widgetName(working.widgets.find((w) => w.id === id));
    setAnnouncement(t('announceCopied', { name }));
    toast(t('workspaceWidgetCopied', { name }));
  };
  // The toolbar's Copy: the tab's clipboard always, the system clipboard where the browser allows
  // it (a non-secure context has none), so a paste in another tab can read it too.
  const copyFromToolbar = (id: string) => {
    const text = clipboardText(id);
    if (!text) return;
    copied(id, text);
    void navigator.clipboard?.writeText(text).catch(() => undefined);
  };

  // Pastes clipboard text. False when the text is not a widget clipboard at all. A copy made by a
  // newer release is refused, and said so, rather than read as this format.
  const pasteText = (text: string): boolean => {
    let parsed: ReturnType<typeof parseWidgetClipboard>;
    try {
      parsed = parseWidgetClipboard(text, { catalog: registry.catalog });
    } catch (err) {
      if ((err as DashboardDefinitionError | null)?.code !== 'schemaTooNew') throw err;
      toast(t('workspacePasteTooNew'), 'error');
      return true;
    }
    if (!parsed) return false;
    for (const r of parsed.refused) toast(t('workspacePasteRefused', { type: r.type, reason: r.reason }), 'error');
    if (parsed.clipboard.widgets.length === 0) return true;
    const result = pasteWidgets(working, parsed.clipboard, here);
    const name = widgetName(result.definition.widgets.find((w) => w.id === result.ids[0]));
    history.commit(result.definition, { action: 'paste', name });
    setSelectedId(result.ids[0]);
    if (result.unbound.length > 0) {
      const named = result.unbound.map((n) => ({
        ...n,
        name: widgetName(result.definition.widgets.find((w) => w.id === n.widgetId)),
      }));
      setPasteNotices((prev) => [...prev, ...named]);
    }
    setAnnouncement(t('announcePasted', { name }));
    return true;
  };
  const pasteFromToolbar = () => {
    const text = readWidgetClipboard();
    if (!text || !pasteText(text)) toast(t('workspacePasteEmpty'), 'error');
  };
  const notices = pasteNotices.filter((n) => stillUnbound(working, n));

  // The latest of everything the document-level listeners below call, so they are bound once per
  // edit session rather than on every render.
  const live = useRef({ undo, redo, duplicate, clipboardText, copied, pasteText, selectedId });
  live.current = { undo, redo, duplicate, clipboardText, copied, pasteText, selectedId };
  const { seal } = history;

  useEffect(() => {
    if (mode !== 'edit') return;
    const onKey = (e: KeyboardEvent) => {
      const shortcut = editorShortcut(e);
      if (!shortcut) return;
      const now = live.current;
      if (shortcut === 'duplicate') {
        if (!now.selectedId) return;
        e.preventDefault(); // the browser's bookmark shortcut
        now.duplicate(now.selectedId);
        return;
      }
      e.preventDefault();
      if (shortcut === 'undo') now.undo();
      else now.redo();
    };
    // Copy and paste ride the browser's own events, so the system clipboard is read and written
    // without a permission prompt. Text copied or pasted in a field, or a text selection on the
    // page, is left to the browser.
    const onCopy = (e: ClipboardEvent) => {
      const now = live.current;
      if (!now.selectedId || !isEditorSurfaceTarget(e.target)) return;
      if (!(window.getSelection?.()?.isCollapsed ?? true)) return;
      const text = now.clipboardText(now.selectedId);
      if (!text) return;
      e.clipboardData?.setData('text/plain', text);
      e.preventDefault();
      now.copied(now.selectedId, text);
    };
    const onPaste = (e: ClipboardEvent) => {
      if (!isEditorSurfaceTarget(e.target)) return;
      const text = e.clipboardData?.getData('text/plain') ?? '';
      // Text that is not a widget is not ours to paste; an empty system clipboard falls back to
      // the widget last copied in this tab.
      const source = text !== '' ? text : readWidgetClipboard();
      if (source && live.current.pasteText(source)) e.preventDefault();
    };
    // Leaving a field ends its typing step: coming back to it later is a new undo step.
    const onFocusOut = () => seal();
    window.addEventListener('keydown', onKey);
    document.addEventListener('copy', onCopy);
    document.addEventListener('paste', onPaste);
    document.addEventListener('focusout', onFocusOut);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.removeEventListener('copy', onCopy);
      document.removeEventListener('paste', onPaste);
      document.removeEventListener('focusout', onFocusOut);
    };
  }, [mode, seal]);

  // A different widget selected starts a new typing step.
  useEffect(() => seal(), [selectedId, seal]);

  const toggleMode = () => {
    setSelectedId(null);
    resetSelection(); // drop any drill selection so the other mode starts from the defaults
    setMode(mode === 'edit' ? 'view' : 'edit');
  };

  const save = () => {
    const snapshot = working; // persist exactly what we serialize; later edits stay dirty
    setSaveState({ kind: 'saving' });
    // name tracks the (editable) title so a save never orphans the list label. The
    // description is deliberately NOT named: the editor does not own it, and an omitted
    // field is left exactly as stored. expectedUpdatedAt is the optimistic-concurrency
    // precondition — a stale one fails with CONFLICT.
    updateDashboard(token, {
      name: snapshot.title || null,
      definition: serializeDefinition(snapshot),
      expectedUpdatedAt,
    })
      .then((result) => {
        // The save wrote the current format: neither copy is a conversion any more.
        setSaved(withoutConversionMark(snapshot));
        // In every state the history holds, or an undo would bring the conversion notice back.
        history.rebase(withoutConversionMark);
        setExpectedUpdatedAt(result.updatedAt); // advance the baseline for the next save
        setSaveState({ kind: 'clean' });
        toast(t('workspaceSaved'));
      })
      .catch((err: unknown) => {
        const raw = errMessage(err);
        const message = raw.includes(CONFLICT_MARKER) ? t('workspaceConflictMessage') : raw;
        setSaveState({ kind: 'error', message });
      });
  };

  // Remove every option key no widget of that type reads. Offered from the publish
  // drawer, because a leftover key is the one issue blocking publish that the config
  // panel cannot fix — stripUnknownOptions in @devicechain/widgets carries the full
  // reasoning.
  //
  // It lands as an ordinary unsaved edit rather than writing straight through: this
  // DELETES authored values, so the author gets the same review-then-save (or discard)
  // they get for any other change, and the same conflict precondition on the way out.
  const stripUnknown = () => {
    const next = stripUnknownOptions(working, { catalog: registry.catalog });
    // Identity is the no-op signal (see stripUnknownOptions). Not a dirty-state concern:
    // isDirty compares serializations, so an equal-but-new object would not read as an
    // edit either way — this just distinguishes "nothing to remove" from "removed".
    if (next === working) {
      toast(t('workspaceNoUnknownOptions'));
      return;
    }
    history.commit(next, { action: 'strip' });
    toast(t('workspaceUnknownOptionsRemoved'));
  };

  // Clamp every widget into the sizes it supports and bring any off the canvas back onto it. Like
  // the repair above it lands as an ordinary unsaved edit, so the author reviews it before saving.
  // `fitToLimits` returns the same object when nothing needed fixing.
  const fitLimits = () => {
    const next = fitToLimits(working, spanLimitsFor(registry));
    if (next === working) {
      toast(t('layoutAlreadyFits'));
      return;
    }
    history.commit(next, { action: 'fit' });
    toast(t('layoutFitted'));
  };
  // Whether the board has anything the publish would refuse on layout: offers the repair in the
  // toolbar as soon as it exists, not only once the publish drawer is open.
  const layoutBlocked = useMemo(() => publishLayoutBlockers(working, registry).length > 0, [working, registry]);

  // After a rollback the server draft IS the chosen version; re-baseline working +
  // saved to it (seeding the title from the name like the initial load) so the
  // editor reflects it and isn't spuriously dirty, and advance the concurrency
  // baseline to the returned updatedAt.
  const onRolledBack = (result: { definition: string; updatedAt: string | null }) => {
    try {
      let def = parseDashboardDefinition(JSON.parse(result.definition), { catalog: registry.catalog });
      // Seed the title from the CURRENT effective name (the last-saved title, which
      // tracks renames) rather than the mount-time `name` prop — otherwise rolling
      // back to a title-less version and re-saving would silently revert a rename.
      const currentName = saved.title || name || '';
      if (def.title === '' && currentName) def = setTitle(def, currentName);
      def = migrateToSlots(def); // keep the editor slot-based (a version may be concrete)
      // Replaced from outside the editor: the history before it no longer leads here.
      history.reset(def);
      setPasteNotices([]);
      setSaved(def);
      setSelectedId(null);
      setExpectedUpdatedAt(result.updatedAt);
      setSaveState({ kind: 'clean' });
    } catch (err) {
      // The server's draft is now a board this console cannot read. Keeping the editor open on
      // the old copy would offer to save it back over the newer one, so it is closed instead.
      if ((err as DashboardDefinitionError | null)?.code === 'schemaTooNew') {
        setNewerBoard(true);
        return;
      }
      toast(t('workspaceRollbackParseError'), 'error');
    }
  };

  const remove = async () => {
    if (
      !(await confirm({
        title: t('deleteTitle'),
        description: t('deleteConfirm', { token }),
        confirmLabel: t('delete'),
      }))
    )
      return;
    try {
      await deleteDashboard(token);
      toast(t('deleted', { token }));
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
      <span className="text-sm text-muted-foreground">{t('workspaceUnsavedChanges')}</span>
    ) : null;

  const saveButton = (
    <Button size="sm" onClick={save} disabled={!(dirty || converted) || saveState.kind === 'saving'}>
      {saveState.kind === 'saving' ? t('workspaceSaving') : t('common:save')}
    </Button>
  );

  const historyButton = (
    <Button variant="outline" size="sm" onClick={() => setHistoryOpen(true)} data-shot="dashboard-history">
      <History size={14} /> {t('workspaceHistory')}
    </Button>
  );

  // Export the current definition. A plain export keeps the slot default bindings (renders
  // as-is); "as template" strips them so an importer must supply a binding per slot.
  const exportJson = (template: boolean) => exportDefinitionJson(working, template);
  const copyExport = async (template: boolean) => {
    // navigator.clipboard is undefined on a non-secure context (plain http over a LAN
    // IP) — the optional chain would resolve silently and falsely claim "copied".
    if (!navigator.clipboard) {
      toast(t('workspaceClipboardUnavailable'), 'error');
      return;
    }
    try {
      await navigator.clipboard.writeText(exportJson(template));
      toast(template ? t('workspaceTemplateCopied') : t('workspaceDefinitionCopied'));
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
        <Button variant="outline" size="sm" data-shot="dashboard-export">
          <Download size={14} /> {t('workspaceExport')}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={downloadExport}>{t('workspaceDownloadJson')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void copyExport(false)}>{t('workspaceCopyJson')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void copyExport(true)}>{t('workspaceCopyTemplate')}</DropdownMenuItem>
        <DropdownMenuItem disabled={!canEdit} onSelect={() => setCloneOpen(true)}>
          {t('workspaceClone')}
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
          options={SYNTHETIC_GENERATORS.map((g) => ({ value: g, label: t(`syntheticGenerator_${g}`) }))}
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
        data-shot="dashboard-preview"
        onClick={() => setPreview((p) => !p)}
        title={t('workspacePreviewHint')}
      >
        <FlaskConical size={14} /> {t('workspacePreview')}
      </Button>
    </div>
  );

  const viewActions = (
    <div className="flex flex-wrap items-center justify-end gap-2">
      <TimeRangeBar
        state={timeRange}
        controls={timeRangeControls}
        labels={{
          range: t('timeRangeLabel'),
          pause: t('timeRangePause'),
          resume: t('timeRangeResume'),
          paused: t('timeRangePaused'),
          pausedAt: (time) => t('timeRangePausedAt', { time }),
          actionsLive: t('timeRangeActionsLive'),
        }}
      />
      {!preview && <WorkspaceConnection hub={liveHub} />}
      {statusEl}
      {/* Reachable so edits carried into view mode can be persisted without
          re-entering the editor. */}
      {dirty && saveButton}
      {previewControl}
      {exportMenu}
      {historyButton}
      <Button variant="outline" size="sm" onClick={toggleMode} disabled={!canEdit} data-shot="dashboard-edit">
        {t('workspaceEdit')}
      </Button>
      <Button variant="destructive" size="sm" onClick={remove} disabled={!canEdit}>
        <Trash2 size={14} /> {t('delete')}
      </Button>
    </div>
  );

  const editActions = (
    <div className="flex items-center gap-2">
      {!preview && <WorkspaceConnection hub={liveHub} />}
      {statusEl}
      <div role="group" aria-label={t('workspaceHistoryControls')} className="flex gap-1">
        <Button
          variant="outline"
          size="sm"
          onClick={undo}
          disabled={!history.canUndo}
          aria-label={t('workspaceUndo')}
          title={t('workspaceUndoHint')}
          data-testid="undo"
        >
          <Undo2 size={14} />
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={redo}
          disabled={!history.canRedo}
          aria-label={t('workspaceRedo')}
          title={t('workspaceRedoHint')}
          data-testid="redo"
        >
          <Redo2 size={14} />
        </Button>
      </div>
      <AddWidgetMenu registry={registry} onAdd={addWidgetOfType} />
      <Button variant="outline" size="sm" onClick={pasteFromToolbar} title={t('workspacePasteHint')} data-testid="paste">
        <ClipboardPaste size={14} /> {t('workspacePaste')}
      </Button>
      {layoutBlocked && (
        <Button
          variant="outline"
          size="sm"
          onClick={fitLimits}
          title={t('layoutFitToLimitsHint')}
          data-testid="fit-to-limits"
        >
          {t('layoutFitToLimits')}
        </Button>
      )}
      {previewControl}
      {exportMenu}
      {historyButton}
      {saveButton}
      <Button variant="outline" size="sm" onClick={toggleMode}>
        {t('workspaceDone')}
      </Button>
    </div>
  );

  if (newerBoard) {
    return (
      <PageShell title={heading} banner="dashboard">
        <ErrorState description={t('schemaTooNew')} />
      </PageShell>
    );
  }

  return (
    <>
    <PageShell
      title={heading}
      titleAdornment={displayName ? <CopyToken value={token} /> : undefined}
      banner="dashboard"
      description={
        <div className="mt-1">
          <Button
            variant="quiet"
            size="inline"
            className="gap-1 text-sm"
            onClick={() => void leaveGuarded('/dashboards')}
          >
            <ArrowLeft size={14} /> {t('title')}
          </Button>
        </div>
      }
      action={mode === 'edit' ? editActions : viewActions}
      // Both modes are full-bleed: the dashboard surface manages its own single
      // scroll region. A non-full-bleed body (padding + its own overflow-auto)
      // around a fixed-height renderer that ALSO scrolls produced a spurious
      // nested scrollbar even when the content fit.
      subHeader={
        mode === 'edit' ? (
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2 px-6 py-2">
            <div className="flex items-center gap-3">
              <label htmlFor="dash-title" className="text-sm text-muted-foreground">
                {t('workspaceTitleLabel')}
              </label>
              <Input
                id="dash-title"
                value={working.title}
                onChange={(e) => history.commit(setTitle(working, e.target.value), { action: 'title' }, 'title')}
                placeholder={t('workspaceTitlePlaceholder')}
                className="max-w-sm"
              />
            </div>
            <CanvasSettings
              definition={working}
              onChange={(next, field) => history.commit(next, { action: 'canvas' }, `canvas:${field}`)}
            />
            {notices.length > 0 && (
              <div
                role="status"
                data-testid="paste-unbound"
                className="flex w-full items-start justify-between gap-3 rounded-md border border-border bg-muted px-3 py-2 text-sm"
              >
                <div>
                  {notices.map((n) => (
                    <p key={`${n.widgetId}:${n.slot}`}>
                      {t(PASTE_UNBOUND_MESSAGE[n.reason], {
                        name: n.name,
                        slot: n.from,
                      })}
                    </p>
                  ))}
                </div>
                <Button variant="quiet" size="inline" onClick={() => setPasteNotices([])}>
                  {t('pasteUnboundDismiss')}
                </Button>
              </div>
            )}
            {converted && (
              <div
                role="status"
                data-shot="dashboard-converted"
                className="w-full rounded-md border border-border bg-muted px-3 py-2 text-sm"
              >
                {working.convertedFrom !== undefined && working.convertedFrom < 2 && (
                  <p>{t('workspaceConvertedNotice')}</p>
                )}
                {convertedCharts.length > 0 && (
                  <p>{t('workspaceConvertedWindowsNotice', { count: convertedCharts.length, charts: convertedCharts.join(', ') })}</p>
                )}
              </div>
            )}
          </div>
        ) : undefined
      }
      fullBleed
    >
      {mode === 'edit' ? (
        <div className="flex h-full min-h-0">
          <div className="sr-only" role="status" aria-live="polite" data-testid="editor-announcer">
            {announcement}
          </div>
          <div className="min-w-0 flex-1">
            <DashboardCanvas
              definition={working}
              // Prune slots orphaned by a widget delete (canvas edits go through here);
              // a no-op for move/resize since those keep every widget's slot reference.
              onChange={(next, edit) =>
                history.commit(pruneSlots(next), {
                  action: edit.action,
                  name: widgetName(working.widgets.find((w) => w.id === edit.widgetId)),
                })
              }
              onDuplicate={duplicate}
              onCopy={copyFromToolbar}
              hub={dataHub}
              registry={registry}
              actions={dataHub}
              bindings={editBindings}
              selectedId={selectedId}
              onSelect={setSelectedId}
            />
          </div>
          {selected?.type === UNKNOWN_WIDGET_TYPE && (
            <UnknownWidgetPanel key={selected.id} widget={selected} onClose={() => setSelectedId(null)} />
          )}
          {selected && isResolvedWidget(selected) && (
            <WidgetConfigPanel
              // Remount per widget so the config panel's local input buffers (e.g.
              // the measurements text field) don't carry one widget's keystrokes
              // onto the next when both take the same datasource branch.
              key={selected.id}
              widget={selected}
              registry={registry}
              datasource={resolveConcrete(working, selected)}
              slotName={widgetSlotName(selected)}
              slotScoped={isSlotScoped(working, widgetSlotName(selected))}
              slots={working.slots}
              onChange={(next) =>
                history.commit(
                  updateWidget(working, next.id, next),
                  { action: 'options', name: widgetName(selected) },
                  settingsKey(selected, next),
                )
              }
              onDatasource={(ds) =>
                history.commit(
                  applyDatasource(working, selected.id, ds),
                  { action: 'datasource', name: widgetName(selected) },
                  `datasource:${selected.id}`,
                )
              }
              // Scope the selected widget's slot (parent + strategy), or clear it. setSlotScope
              // validates (parent exists/anchor/no cycle) and no-ops an invalid change. Prune
              // afterwards is unnecessary — scope never orphans a slot.
              onScope={(scope) => {
                const slot = widgetSlotName(selected);
                if (slot) history.commit((w) => setSlotScope(w, slot, scope), { action: 'scope', name: widgetName(selected) });
              }}
              onClose={() => setSelectedId(null)}
            />
          )}
        </div>
      ) : (
        // Fill the page-shell's flex body (full-bleed) so the renderer's own
        // overflow:auto is the single scroll region — no bounded/fixed height that
        // double-scrolls against an outer container.
        <div className="h-full min-h-0 p-4">
          {/* A deep link that cannot be honoured replaces the board: showing the board's default
              device under a link that names another would be showing the wrong device. */}
          {linkPhase === 'checking' ? (
            <p className="p-4 text-sm text-muted-foreground" role="status">
              {t('linkChecking')}
            </p>
          ) : linkPhase === 'refused' && linkProblems ? (
            <BoundDevicesNotice
              title={t('linkBlockedTitle')}
              lines={describeBoundDevicesProblems(linkProblems, {
                unknownSlot: (slot) => t('linkUnknownSlot', { slot }),
                notADeviceSlot: (slot) => t('linkNotDeviceSlot', { slot }),
                invalidToken: (slot, token) => t('linkInvalidToken', { slot, token }),
                missing: (slot, token) => t('linkMissing', { slot, token }),
                failed: (slot, token) => t('linkFailed', { slot, token }),
              })}
              dismissLabel={t('linkOpenWithout')}
              onDismiss={dismissLink}
              retryLabel={linkProblems.failed.length > 0 ? t('linkRetry') : undefined}
              onRetry={retryLink}
            />
          ) : (
          <DashboardRenderer
            definition={working}
            hub={dataHub}
            registry={registry}
            actions={dataHub}
            seedHistory={!preview}
            timeRange={timeRange}
            bindings={bindings}
            // No drill in preview: synthetic alarm rows carry fabricated originator
            // tokens, and a drill would poison the live selection overlay with a token
            // that's not a real member (blanking the widgets after preview is turned off).
            select={preview ? undefined : select}
            // Selector candidates likewise off in preview — synthetic data has no real
            // entities to offer, and a pick would poison the live overlay.
            candidates={preview ? undefined : candidates}
          />
          )}
        </div>
      )}
    </PageShell>
    {cloneOpen && (
      <DashboardImportDrawer
        open
        onOpenChange={setCloneOpen}
        title={t('workspaceClone')}
        // Compact, as a save writes it: the pretty-printed export form can be larger than
        // the size limit for a board that is well inside it.
        source={{ text: serializeDefinition(working), name: t('cloneName', { name: heading }) }}
        onCreated={(created) => {
          toast(t('created', { token: created }));
          setCloneOpen(false);
          navigate(`/dashboards/${encodeURIComponent(created)}`);
        }}
      />
    )}
    <VersionHistorySheet
      token={token}
      open={historyOpen}
      onOpenChange={setHistoryOpen}
      // `saved`, not `working`: publish freezes the server draft, which is what
      // `saved` mirrors (and what expectedUpdatedAt is the baseline for).
      saved={saved}
      dirty={dirty}
      saving={saveState.kind === 'saving'}
      canWrite={canEdit}
      expectedUpdatedAt={expectedUpdatedAt}
      onRolledBack={onRolledBack}
      onStripUnknownOptions={stripUnknown}
      onFitToLimits={fitLimits}
      registry={registry}
    />
    </>
  );
}
