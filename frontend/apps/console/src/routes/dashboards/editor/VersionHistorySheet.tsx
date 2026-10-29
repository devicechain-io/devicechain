// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// VersionHistorySheet — the dashboard versioning surface (ADR-039 PR G). A drawer
// that lets an author freeze the current (saved) draft into an immutable version
// and roll the draft back to any published version. Publishing snapshots the
// SERVER draft, so it's disabled while there are unsaved edits — the author saves
// first. Rollback replaces the draft server-side; the workspace re-baselines from
// the returned definition (onRolledBack) rather than reloading the page.
//
// Publishing also refuses a board whose widgets carry options the renderer does not
// read. 🔴 THE REFUSAL IS publishDashboard's, NOT THIS COMPONENT'S — everything here
// is the explanation: which widget, which option, and (for a leftover key, the one
// issue no config-panel control can reach) a way to remove it. A published version is
// immutable and is what an embedder mounts, so shipping one whose gauge has no scale
// or whose command button names no command is the one moment worth being strict at;
// the draft it was built from stays unrestricted, the same way a device profile's
// draft is inert until it is published.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Radio, RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { FormField } from '@/components/ui/form-field';
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet';
import {
  DataTable,
  DataTableHead,
  DataTableHeaderCell,
  DataTableBody,
  DataTableRow,
  DataTableCell,
} from '@/components/ui/data-table';
import { EmptyState } from '@/components/ui/empty-state';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { Textarea } from '@/components/ui/textarea';
import { formatTime } from '@/lib/utils';
import { validateDefinitionOptions } from '@devicechain/widgets';
import { OPTION_ISSUE_MESSAGE_KEYS, UNRESOLVED_REASON_KEYS, widgetLabel } from './optionIssues';
import { LAYOUT_FINDING_KEYS, publishLayoutBlockers } from './layoutFindings';
import {
  parseDashboardDefinition,
  unresolvedWidgets,
  type DashboardDefinition,
  type UnresolvedWidgetReport,
} from '@devicechain/dashboards';
import { defaultWidgetRegistry, type EditorWidgetRegistry } from './widgetRegistry';
import { resolveText, useMessageLookup } from './optionWording';
import {
  activateDashboardVersion,
  getDashboardVersionDefinition,
  getPublishedVersion,
  listDashboardVersions,
  publishDashboard,
  rollbackDashboard,
  CONFLICT_MARKER,
  type DashboardVersion,
} from '@/lib/api/dashboards';
import '@/i18n/lazy/dashboards';

const VERSION_PAGE = 100;
// dashboard-management clamps a page to its maximum page size.
const VERSION_PAGE_MAX = 1000;

// What reading an older version for unresolved widgets found: the list, or that the check
// could not run. Kept distinct so a failed read is never shown as a clean result.
type VersionCheck = { checked: true; unresolved: UnresolvedWidgetReport[] } | { checked: false };

export interface VersionHistorySheetProps {
  token: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The definition that will be frozen: the last SAVED one, paired with the
  // expectedUpdatedAt below. Forwarded to publishDashboard, which validates it.
  saved: DashboardDefinition;
  // Publishing snapshots the saved server draft; block it while the editor has
  // unsaved edits so the author isn't surprised by an older snapshot.
  dirty: boolean;
  // A save in flight means the server draft (and its updatedAt) is mid-change —
  // block publish/rollback until it settles so they don't race it.
  saving: boolean;
  canWrite: boolean;
  // The editor's optimistic-concurrency baseline, forwarded to publish so it fails
  // if the server draft moved on since (another writer) rather than freezing it.
  expectedUpdatedAt: string | null;
  // Called after a successful rollback with the new draft so the workspace can
  // re-baseline its working/saved copies (and updatedAt) without a page reload.
  onRolledBack: (result: { definition: string; updatedAt: string | null }) => void;
  // Strips every option key no widget reads from the WORKING copy, leaving the author
  // an unsaved edit to review and save (why that repair exists at all: see
  // stripUnknownOptions in @devicechain/widgets). The workspace owns the definition, so
  // the repair happens there; this drawer only offers it.
  onStripUnknownOptions: () => void;
  // Clamps every widget into its size limits and back onto the canvas in the WORKING copy
  // (fitToLimits), leaving an unsaved edit to review and save, as for the option repair above.
  onFitToLimits: () => void;
  // The registry `saved` was parsed with: the catalog its option schemas are judged against.
  // Defaults to the deployment's.
  registry?: EditorWidgetRegistry;
}

export function VersionHistorySheet({
  token,
  open,
  onOpenChange,
  saved,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
  onStripUnknownOptions,
  onFitToLimits,
  registry = defaultWidgetRegistry(),
}: VersionHistorySheetProps) {
  const { t } = useTranslation(['dashboards', 'common']);
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader className="mb-6">
          <SheetTitle>{t('versionHistoryTitle')}</SheetTitle>
          <SheetDescription>{t('versionHistoryDescription')}</SheetDescription>
        </SheetHeader>
        {/* Rendered inside SheetContent, which Radix unmounts when closed, so the
            version list is fetched fresh each time the drawer opens. */}
        {open && (
          <VersionHistoryBody
            token={token}
            saved={saved}
            dirty={dirty}
            saving={saving}
            canWrite={canWrite}
            expectedUpdatedAt={expectedUpdatedAt}
            onRolledBack={onRolledBack}
            onStripUnknownOptions={onStripUnknownOptions}
            onFitToLimits={onFitToLimits}
            registry={registry}
            onClose={() => onOpenChange(false)}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function VersionHistoryBody({
  token,
  saved,
  dirty,
  saving,
  canWrite,
  expectedUpdatedAt,
  onRolledBack,
  onStripUnknownOptions,
  onFitToLimits,
  registry,
  onClose,
}: {
  token: string;
  saved: DashboardDefinition;
  dirty: boolean;
  saving: boolean;
  canWrite: boolean;
  expectedUpdatedAt: string | null;
  onRolledBack: (result: { definition: string; updatedAt: string | null }) => void;
  onStripUnknownOptions: () => void;
  onFitToLimits: () => void;
  registry: EditorWidgetRegistry;
  onClose: () => void;
}) {
  const { t } = useTranslation(['dashboards', 'common']);
  const { toast } = useToast();
  const confirm = useConfirm();
  const lookup = useMessageLookup();

  const [refreshKey, setRefreshKey] = useState(0);
  // The server pages the history (newest first, at most VERSION_PAGE_MAX per request), so
  // the list asks for a growing prefix and offers "load more" while the last page came back
  // full. At the server's maximum it says so rather than implying that is everything.
  const [limit, setLimit] = useState(VERSION_PAGE);
  const { data, loading, error } = useQuery(
    () => listDashboardVersions(token, limit),
    [token, refreshKey, limit],
  );
  const maybeMore = !!data && data.length >= limit;
  // The version viewers are served right now; refreshed with the list.
  const { data: liveVersion } = useQuery(() => getPublishedVersion(token), [token, refreshKey]);

  const [label, setLabel] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);

  // The option issues on the document that would be frozen — the SAVED definition,
  // never the working copy. While the editor is dirty those differ, and reporting the
  // working copy's issues would tell an author their board is fine (or broken) about a
  // document publish will not touch.
  const issues = useMemo(() => validateDefinitionOptions(saved, { catalog: registry.catalog }), [saved, registry]);
  // Widgets this console cannot resolve to an installed implementation. They block a NEW
  // version (a published version is immutable and viewers are served it) but nothing else:
  // the draft keeps their stored JSON, and saves, layout edits and loading all still work.
  const unresolved = useMemo(() => unresolvedWidgets(saved), [saved]);
  const strippable = issues.some((i) => i.code === 'unknown');
  // Placements outside what their widget can show, or off the canvas. Judged on the SAVED
  // definition for the same reason as the option issues.
  const layoutBlockers = useMemo(() => publishLayoutBlockers(saved, registry), [saved, registry]);

  const publish = async () => {
    setBusy(true);
    try {
      const { version } = await publishDashboard(token, {
        definition: saved,
        catalog: registry.catalog,
        registry,
        label,
        description,
        expectedUpdatedAt,
      });
      toast(t('versionPublished', { version }));
      setLabel('');
      setDescription('');
      setRefreshKey((k) => k + 1);
    } catch (err) {
      const raw = errMessage(err);
      // Only CONFLICT is re-worded. publishDashboard's own option refusal cannot reach
      // here from this drawer — the button is disabled while `issues` is non-empty, and
      // both checks run the same pure function over the same object — so if it ever
      // does, the raw message (which names the widget and the option) is exactly what
      // should be shown rather than a friendlier sentence that discards it.
      toast(raw.includes(CONFLICT_MARKER) ? t('versionPublishConflict') : raw, 'error');
    } finally {
      setBusy(false);
    }
  };

  // Widgets in an OLDER version this console cannot resolve. Re-serving or rolling back to it
  // is not blocked (only a NEW version is: the stored JSON is kept and a console that has the
  // package renders it), but the author is told before they confirm.
  //
  // The check is advisory, so a version this drawer cannot read (the request fails, the version
  // is gone, the stored JSON does not parse) does not stop the action -- the server is the
  // authority on whether it may happen. But "could not check" is NOT reported as "nothing to
  // report": the author is told the check did not run. It is fetched fresh on each click, not
  // cached, so it always describes the version as stored now.
  const checkVersion = async (v: DashboardVersion): Promise<VersionCheck> => {
    setBusy(true);
    try {
      const raw = await getDashboardVersionDefinition(token, v.version);
      if (raw === null) return { checked: false };
      const def = parseDashboardDefinition(JSON.parse(raw), { catalog: registry.catalog });
      return { checked: true, unresolved: unresolvedWidgets(def) };
    } catch (err) {
      console.error('could not read dashboard version to check its widgets', err);
      return { checked: false };
    } finally {
      setBusy(false);
    }
  };

  // An unresolved widget is named by its package's title when this console has the package (a
  // settings-version or SDK mismatch), else by its id; its stored type follows as data.
  const unresolvedName = (u: UnresolvedWidgetReport) => {
    const descriptor = u.type ? registry.descriptor(u.type) : undefined;
    return (descriptor && resolveText(lookup, descriptor.display.titleId, descriptor.display.title)) || u.widgetId;
  };

  // The confirm dialog's description renders inside a paragraph, so this is built from
  // block spans rather than a list.
  const versionNotice = (check: VersionCheck) => {
    if (!check.checked) {
      return (
        <span className="mt-2 block text-foreground" data-testid="version-unresolved-unchecked">
          {t('versionUnresolvedUnchecked')}
        </span>
      );
    }
    if (check.unresolved.length === 0) return null;
    return (
      <span className="mt-2 block text-foreground" data-testid="version-unresolved-warning">
        {t('versionUnresolvedWarning', { count: check.unresolved.length })}
        {check.unresolved.map((u) => (
          <span key={u.widgetId} className="block">
            {unresolvedName(u)}
            {/* The stored type is data (an id), not prose. */}
            <span className="ml-1">({u.type || u.widgetId})</span>
            <span className="ml-1">— {t(UNRESOLVED_REASON_KEYS[u.reason.code])}</span>
          </span>
        ))}
      </span>
    );
  };

  const activate = async (v: DashboardVersion) => {
    const check = await checkVersion(v);
    // Making a version live had no confirmation before this check existed, and a version with
    // nothing to report still has none.
    const notice = versionNotice(check);
    if (
      notice !== null &&
      !(await confirm({
        title: t('versionActivateConfirmTitle', { version: v.version }),
        description: notice,
        confirmLabel: t('versionActivate'),
      }))
    )
      return;
    setBusy(true);
    try {
      await activateDashboardVersion(token, v.version);
      toast(t('versionActivated', { version: v.version }));
      setRefreshKey((k) => k + 1);
    } catch (err) {
      toast(errMessage(err), 'error');
    } finally {
      setBusy(false);
    }
  };

  const rollback = async (v: DashboardVersion) => {
    const check = await checkVersion(v);
    if (
      !(await confirm({
        title: t('versionRollbackConfirmTitle', { version: v.version }),
        description: (
          <>
            {t('versionRollbackConfirmDescription')}
            {versionNotice(check)}
          </>
        ),
        confirmLabel: t('versionRollback'),
      }))
    )
      return;
    setBusy(true);
    try {
      const result = await rollbackDashboard(token, v.version, expectedUpdatedAt);
      onRolledBack(result);
      toast(t('versionRolledBack', { version: v.version }));
      onClose();
    } catch (err) {
      const raw = errMessage(err);
      toast(raw.includes(CONFLICT_MARKER) ? t('versionRollbackConflict') : raw, 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      {canWrite && (
        <div className="space-y-3 rounded-md border border-border p-4">
          <div className="text-sm font-semibold">{t('versionPublishHeading')}</div>
          <FormField label={t('versionLabelField')} description={t('versionLabelHint')}>
            <Input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t('versionLabelPlaceholder')}
            />
          </FormField>
          <FormField label={t('common:colDescription')}>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('versionDescriptionPlaceholder')}
            />
          </FormField>
          <p className="text-sm text-muted-foreground">{t('versionPublishGoesLive')}</p>
          {dirty ? (
            <p className="text-sm text-muted-foreground">{t('versionSaveBeforePublish')}</p>
          ) : null}
          {unresolved.length > 0 && (
            <div
              className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-3"
              data-testid="publish-blocked-unresolved"
            >
              <p className="text-sm font-medium">{t('versionUnresolvedBlocked', { count: unresolved.length })}</p>
              <ul className="space-y-1 text-sm text-muted-foreground">
                {unresolved.map((u) => (
                  <li key={u.widgetId}>
                    <span className="text-foreground">{widgetLabel(saved, u.widgetId)}</span>
                    {/* The stored type is data (an id), not prose. */}
                    <span className="ml-1">({u.type || u.widgetId})</span>
                    <span className="ml-1">— {t(UNRESOLVED_REASON_KEYS[u.reason.code])}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {layoutBlockers.length > 0 && (
            <div
              className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-3"
              data-testid="publish-blocked-layout"
            >
              <p className="text-sm font-medium">{t('versionLayoutBlocked', { count: layoutBlockers.length })}</p>
              <ul className="space-y-1 text-sm text-muted-foreground">
                {layoutBlockers.map((f) => (
                  <li key={`${f.widgetId}:${f.code}:${f.breakpoint ?? ''}`}>
                    <span className="text-foreground">{widgetLabel(saved, f.widgetId)}</span>
                    <span className="ml-1">— {t(LAYOUT_FINDING_KEYS[f.code])}</span>
                  </li>
                ))}
              </ul>
              {/* Offered only against a clean editor, for the same reason as the option repair. */}
              {!dirty && (
                <Button variant="outline" size="sm" onClick={onFitToLimits} disabled={busy}>
                  {t('layoutFitToLimits')}
                </Button>
              )}
            </div>
          )}
          {issues.length > 0 && (
            <div className="space-y-2 rounded-md border border-destructive/40 bg-destructive/5 p-3">
              <p className="text-sm font-medium">
                {t('versionPublishBlocked', { count: issues.length })}
              </p>
              <ul className="space-y-1 text-sm text-muted-foreground">
                {issues.map((issue) => (
                  // Keyed by widget + option + code: one widget can carry several
                  // issues, and the same option can be both missing and out of range
                  // on different widgets of the same type.
                  <li key={`${issue.widgetId}:${issue.key}:${issue.code}`}>
                    <span className="text-foreground">{widgetLabel(saved, issue.widgetId)}</span>
                    <span className="ml-1">({issue.widgetType})</span>
                    {/* The CODE is translated, not the schema's English message — see
                        optionIssues.ts. `key` is an option name, not user text. */}
                    <span className="ml-1">
                      — {t(OPTION_ISSUE_MESSAGE_KEYS[issue.code], { key: issue.key })}
                    </span>
                  </li>
                ))}
              </ul>
              {/* Offered only against a clean editor. The list above describes `saved`,
                  so once the repair (or any other edit) is pending, it describes a
                  document that is no longer current — and leaving the button armed
                  beside issues it has already fixed invites a second click that can
                  only report there is nothing to do. The dirty hint above takes over. */}
              {strippable && !dirty && (
                <Button variant="outline" size="sm" onClick={onStripUnknownOptions} disabled={busy}>
                  {t('versionStripUnknownOptions')}
                </Button>
              )}
            </div>
          )}
          <Button
            size="sm"
            onClick={publish}
            loading={busy}
            disabled={dirty || saving || issues.length > 0 || unresolved.length > 0 || layoutBlockers.length > 0}
          >
            {t('versionPublishButton')}
          </Button>
        </div>
      )}

      {/* A size container: the history table below lays itself out by the room the drawer
          gives it, not by the window (the drawer is the full width only on a phone). */}
      <div className="@container">
        <div className="mb-2 text-sm font-semibold">{t('versionPublishedHeading')}</div>
        {loading ? (
          <LoadingState description={t('versionLoading')} />
        ) : error ? (
          <ErrorState description={error} />
        ) : !data || data.length === 0 ? (
          <EmptyState description={t('versionEmpty')} />
        ) : (
          // Two layouts, one set of elements. With room (@lg and up) it is a four-column table
          // whose actions wrap under each other when the row runs short. Without it — a phone,
          // where four columns cannot fit the actions at any wrapping — each row reflows into
          // lines: the version, then when and by whom, then the actions. The table clips (its
          // rounded frame), so a row held wider than the drawer used to cut Roll back off at
          // the right edge; now nothing in a row is wider than the drawer.
          <DataTable>
            <DataTableHead className="@max-lg:hidden">
              <DataTableHeaderCell>{t('versionColumnVersion')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('versionColumnPublished')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('versionColumnBy')}</DataTableHeaderCell>
              <DataTableHeaderCell> </DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {data.map((v) => (
                <DataTableRow key={v.version} className="@max-lg:flex @max-lg:flex-wrap @max-lg:gap-x-3 @max-lg:px-4 @max-lg:py-3">
                  {/* The cell breaks anywhere for the author's label, which can be one long word.
                      The live badge opts out: a short word broken inside itself ("Activ" over
                      "a") reads as two, and with no label in any row to widen the column, a
                      table short of room squeezed it until the badge broke. */}
                  <DataTableCell className="font-medium text-foreground [overflow-wrap:anywhere] @max-lg:basis-full @max-lg:p-0">
                    <span className="font-mono">{v.version}</span>
                    {v.label ? <span className="ml-2 text-muted-foreground">{v.label}</span> : null}
                    {v.version === liveVersion ? (
                      <span className="ml-2 inline-flex items-center gap-1 whitespace-nowrap rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
                        <Radio size={11} /> {t('versionLive')}
                      </span>
                    ) : null}
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground @max-lg:p-0 @max-lg:pt-1">
                    {formatTime(v.publishedAt)}
                  </DataTableCell>
                  {/* An address is one unbroken word; let it break anywhere rather than hold the
                      table wider than the drawer, which pushed the actions past its edge. */}
                  <DataTableCell
                    className="text-muted-foreground [overflow-wrap:anywhere] @max-lg:min-w-0 @max-lg:p-0 @max-lg:pt-1"
                    data-shot-pii
                  >
                    {v.publishedBy || '—'}
                  </DataTableCell>
                  <DataTableCell className="text-right @max-lg:basis-full @max-lg:p-0 @max-lg:pt-2">
                    {canWrite && (
                      <div className="flex flex-wrap justify-end gap-2 @max-lg:justify-start [&>button]:shrink-0">
                        {v.version !== liveVersion && (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => activate(v)}
                            disabled={busy}
                          >
                            <Radio size={13} /> {t('versionActivate')}
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => rollback(v)}
                          disabled={busy || saving}
                        >
                          <RotateCcw size={13} /> {t('versionRollback')}
                        </Button>
                      </div>
                    )}
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
        {maybeMore &&
          (limit < VERSION_PAGE_MAX ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setLimit((l) => Math.min(l + VERSION_PAGE, VERSION_PAGE_MAX))}
            >
              {t('versionLoadMore')}
            </Button>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t('versionOlderNotShown', { count: VERSION_PAGE_MAX })}
            </p>
          ))}
      </div>
    </div>
  );
}
