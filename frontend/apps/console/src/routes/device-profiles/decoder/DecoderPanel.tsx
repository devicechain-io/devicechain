// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Decoder tab of a device profile: the rules that turn a binary frame, a generic webhook's JSON
// or a network server's decoded object into measurements.
//
// Like every other tab here this edits the DRAFT. Devices are decoded by the decoder frozen into
// the active published version, so a change takes effect when the profile is published (Versions
// tab). The badge in the header says whether the draft differs from what is live.
//
// 🔴 THE STALE-DATA TRAP. `useQuery` keeps the previous answer while a new key loads. This tab must
// never show profile A's decoder under profile B, so the editor is mounted ONLY from `current` (the
// answer to this profile's question, never a sticky previous one), keyed by the profile, and
// double-checked against the token the answer carries. An editor initialises its state once from
// its props; keying it is what makes it a different editor for a different profile.
//
// Permissions: reading and testing need device:read; editing, saving, clearing and testing over a
// stored sample need device:write. A read-only caller gets the editor disabled (one <fieldset>)
// and no save, clear or sample controls.

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { hasAuthority, errorCode } from '@devicechain/client';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SectionPanel } from '@/components/ui/section-panel';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { IconButton } from '@/components/ui/icon-button';
import { useToast } from '@/components/ui/toast';
import { useConfirm } from '@/components/ui/confirm-dialog';
import { useAuth } from '@/auth/AuthProvider';
import { useQuery } from '@/lib/hooks/use-query';
import { errMessage } from '@/routes/common';
import { listMetricDefinitions } from '@/lib/api/device-management';
import {
  clearProfileDecoder,
  getProfileDecoders,
  setProfileDecoder,
  testPayloadDecoder,
  type StoredDecoder,
} from '@/lib/api/payload-decoder';
import { FieldTable } from './FieldTable';
import { FunctionReference } from './FunctionReference';
import { TestPanel, type TestRun } from './TestPanel';
import { VectorsPanel, type VectorOutcome } from './VectorsPanel';
import {
  INPUT_KINDS,
  LIMITS,
  buildDocument,
  canonical,
  emptyState,
  expressionAt,
  newId,
  parseDocument,
  pathsIn,
  type EditorState,
  type InputKind,
  type VectorState,
} from './model';
import '@/i18n/lazy/deviceProfiles';

const DEVICE_WRITE = 'device:write';

/** How the draft stands against the decoder in the active published version. */
export type DraftStatus = 'none' | 'matches' | 'differs' | 'unpublished' | 'draftCleared';

export function draftStatus(draftDoc: string | null, active: StoredDecoder | null | undefined, activeVersion: number | null): DraftStatus {
  const activeDoc = active?.document ?? null;
  if (draftDoc === null && activeDoc === null) return 'none';
  if (draftDoc === null) return 'draftCleared';
  if (activeVersion === null || activeDoc === null) return 'unpublished';
  return canonical(draftDoc) === canonical(activeDoc) ? 'matches' : 'differs';
}

export type Refusal =
  | { kind: 'invalid'; message: string; paths: string[] }
  | { kind: 'unsupported'; message: string }
  | { kind: 'rateLimited'; message: string }
  | { kind: 'other'; message: string };

/** Maps a refused save to what the tab shows. Branches on the wire code, never on the message text. */
export function mapRefusal(err: unknown): Refusal {
  const message = errMessage(err);
  switch (errorCode(err)) {
    case 'INVALID_DECODER':
      return { kind: 'invalid', message, paths: pathsIn(message) };
    case 'UNSUPPORTED_DECODER_KIND':
      return { kind: 'unsupported', message };
    case 'RATE_LIMITED':
      return { kind: 'rateLimited', message };
    default:
      return { kind: 'other', message };
  }
}

interface PanelProps {
  profileToken: string;
  /** The profile's active published version; a publish changes it, which re-reads both sides of the badge. */
  activeVersion: number | null;
  /** Load a stored failure sample into the test panel (device:write only). */
  initialSampleId?: string;
}

export function DecoderPanel(props: PanelProps) {
  const { claims } = useAuth();
  return <DecoderPanelView {...props} canWrite={hasAuthority(claims, DEVICE_WRITE)} />;
}

/**
 * The tab with the caller's permission given rather than read from the session, so a harness without
 * a sign-in (the layout gate) can draw it. The app mounts `DecoderPanel`, which derives it.
 */
export function DecoderPanelView({ profileToken, activeVersion, initialSampleId, canWrite }: PanelProps & { canWrite: boolean }) {
  const { t } = useTranslation('deviceProfiles');
  const deps = [profileToken, activeVersion];
  const decoders = useQuery(() => getProfileDecoders(profileToken), deps);
  const defs = useQuery(() => listMetricDefinitions(profileToken), deps);

  // `current`, not `data`: see the header comment.
  const d = decoders.current;
  const m = defs.current;
  if (d === undefined || m === undefined) {
    const error = decoders.error ?? defs.error;
    return error ? <ErrorState description={error} /> : <LoadingState description={t('decLoading')} />;
  }
  if (d === null || d.token !== profileToken) return <ErrorState description={t('decNotFound')} />;

  const metrics = m.filter((x) => !x.calculated && x.dataType !== 'STRING').map((x) => x.metricKey);
  return (
    <DecoderEditor
      key={`${profileToken}:${activeVersion ?? 'draft'}`}
      profileToken={profileToken}
      activeVersion={activeVersion}
      draft={d.decoder ?? null}
      active={d.activeProfileVersion?.decoder ?? null}
      metrics={metrics}
      canWrite={canWrite}
      initialSampleId={initialSampleId}
    />
  );
}

function DraftBadge({ status, version }: { status: DraftStatus; version: number | null }) {
  const { t } = useTranslation('deviceProfiles');
  if (status === 'none') return <Badge variant="outline">{t('decBadgeNone')}</Badge>;
  const variant = status === 'matches' ? 'success' : 'warning';
  return (
    <Badge variant={variant} data-status={status}>
      {t(`decBadge.${status}`, { version })}
    </Badge>
  );
}

function DecoderEditor(props: {
  profileToken: string;
  activeVersion: number | null;
  draft: StoredDecoder | null;
  active: StoredDecoder | null;
  metrics: string[];
  canWrite: boolean;
  initialSampleId?: string;
}) {
  const { profileToken, activeVersion, draft, active, metrics, canWrite, initialSampleId } = props;
  const { t } = useTranslation('deviceProfiles');
  const { toast } = useToast();
  const confirm = useConfirm();

  // What is saved as the draft now (starts as what was read; a save or clear moves it).
  const [saved, setSaved] = useState<StoredDecoder | null>(draft);
  const parsed = useMemo(() => (draft ? parseDocument(draft.document) : null), [draft]);
  const [st, setSt] = useState<EditorState>(() => (parsed?.ok ? parsed.state : emptyState()));
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<Refusal | null>(null);
  const [run, setRun] = useState<TestRun | null>(null);
  const [vectorRun, setVectorRun] = useState<{ text: string; outcomes: VectorOutcome[]; compileError: string | null } | null>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  const built = useMemo(() => buildDocument(st), [st]);
  const edit = (next: EditorState) => {
    setSt(next);
    setDirty(true);
    setRefusal(null);
  };

  // Run the saved-with-the-decoder vectors as the form changes: debounced, because each run compiles.
  const vectorCount = st.vectors.length;
  useEffect(() => {
    const text = built.text;
    if (!text || vectorCount === 0) {
      setVectorRun(null);
      return;
    }
    let cancelled = false;
    const h = setTimeout(() => {
      testPayloadDecoder({ profileToken, document: text })
        .then((r) => {
          if (cancelled) return;
          setVectorRun({ text, outcomes: r.vectors, compileError: r.error ? r.error.message : null });
        })
        .catch(() => {
          if (!cancelled) setVectorRun(null);
        });
    }, 800);
    return () => {
      cancelled = true;
      clearTimeout(h);
    };
  }, [built.text, vectorCount, profileToken]);

  // A refused save moves focus to the message, so a keyboard or screen-reader user lands on it.
  useEffect(() => {
    if (refusal) alertRef.current?.focus();
  }, [refusal]);

  const status = draftStatus(saved?.document ?? null, active, activeVersion);
  const testError = run?.result.error ?? null;
  const named = useMemo(() => {
    const out = [...(refusal?.kind === 'invalid' ? refusal.paths : [])];
    if (testError?.path && run && run.documentText === built.text) out.push(testError.path);
    return out;
  }, [refusal, testError, run, built.text]);

  async function save() {
    if (!built.text) return;
    setBusy(true);
    setRefusal(null);
    try {
      const stored = await setProfileDecoder(profileToken, built.text);
      setSaved(stored);
      setDirty(false);
      toast(t('decSaved'));
    } catch (err) {
      setRefusal(mapRefusal(err));
    } finally {
      setBusy(false);
    }
  }

  async function clear() {
    const ok = await confirm({
      title: t('decClearTitle'),
      description: t('decClearConfirm'),
      confirmLabel: t('decClear'),
    });
    if (!ok) return;
    setBusy(true);
    setRefusal(null);
    try {
      await clearProfileDecoder(profileToken);
      setSaved(null);
      setSt(emptyState());
      setDirty(false);
      setRun(null);
      toast(t('decCleared'));
    } catch (err) {
      setRefusal(mapRefusal(err));
    } finally {
      setBusy(false);
    }
  }

  const header = (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="space-y-2">
        <p className="max-w-prose text-sm text-muted-foreground">{t('decDescription')}</p>
        <div className="flex flex-wrap items-center gap-2">
          <DraftBadge status={status} version={activeVersion} />
          {saved && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {t('decFigures', { max: saved.maxReadings, cost: saved.estimatedCost })}
            </span>
          )}
          {dirty && <Badge variant="outline">{t('decUnsaved')}</Badge>}
        </div>
      </div>
      {canWrite && (
        <div className="flex flex-wrap gap-2">
          <Button onClick={save} loading={busy} disabled={busy || !dirty || !built.text}>
            {t('decSave')}
          </Button>
          <Button variant="outline" onClick={clear} disabled={busy || saved === null}>
            <Trash2 size={14} /> {t('decClear')}
          </Button>
        </div>
      )}
    </div>
  );

  // A decoder the form cannot edit: shown as stored, and (with device:write) clearable.
  if (parsed && !parsed.ok) {
    return (
      <div className="space-y-4">
        {header}
        <div role="alert" className="rounded-md border border-warning/40 bg-warning/10 p-3 text-sm">
          {parsed.reason === 'unsupportedKind' ? t('decUnsupportedKind', { kind: parsed.kind }) : t('decMalformed')}
        </div>
        <pre className="max-h-96 overflow-auto rounded-md border p-3 font-mono text-xs">{draft?.document}</pre>
      </div>
    );
  }

  const issueCount = built.issues.length;
  const setInput = (input: InputKind) =>
    edit({
      ...st,
      input,
      // Table rows follow the input: a frame decoder reads bytes, the others read keys.
      sections: st.sections.map((s) => ({
        ...s,
        rows: s.rows.map((r) => (r.kind === 'field' ? { ...r, field: { ...r.field, source: input === 'FRAME' ? 'frame' : 'json' } } : r)),
      })),
    });

  const outcomes = vectorRun && vectorRun.text === built.text ? vectorRun.outcomes : null;
  const errorExpression = testError?.path ? expressionAt(built.document, testError.path) : null;

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
      <div className="min-w-0 space-y-6">
        {header}

        <div ref={alertRef} tabIndex={-1} aria-live="assertive" className="outline-none">
          {refusal && (
            <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
              <p className="font-medium">
                {refusal.kind === 'unsupported' ? t('decRefusedUnsupported') : refusal.kind === 'invalid' ? t('decRefusedInvalid') : t('decRefusedOther')}
              </p>
              <p className="mt-1 break-words">{refusal.message}</p>
              {refusal.kind === 'invalid' && refusal.paths.length > 0 && (
                <p className="mt-1 text-xs">{t('decRefusedWhere', { paths: refusal.paths.join(', ') })}</p>
              )}
            </div>
          )}
        </div>

        {issueCount > 0 && (
          <p className="text-sm text-muted-foreground" data-testid="issue-count">
            {t('decIssuesToFix', { count: issueCount })}
          </p>
        )}

        {/* One fieldset disables every editing control for a read-only caller; the test panel sits outside it. */}
        <fieldset disabled={!canWrite || busy} className="m-0 min-w-0 space-y-6 border-0 p-0">
          <legend className="sr-only">{t('decEditorLegend')}</legend>
          <SectionPanel title={t('decInputTitle')} description={t('decInputDescription')}>
            <SegmentedControl
              ariaLabel={t('decInputTitle')}
              tone="inset"
              size="sm"
              className="flex-wrap"
              value={st.input}
              onValueChange={setInput}
              options={INPUT_KINDS.map((k) => ({ value: k, label: t(`decInput.${k}`) }))}
            />
          </SectionPanel>

          <SectionPanel
            title={t('decFieldsTitle')}
            description={t('decFieldsDescription')}
            action={
              <span className="text-sm tabular-nums text-muted-foreground" data-testid="max-readings">
                {t('decMaxReadings', { count: built.maxReadings, max: LIMITS.readings })}
              </span>
            }
          >
            <FieldTable state={st} onChange={edit} metrics={metrics} issues={built.issues} named={named} />
          </SectionPanel>

          <SectionPanel title={t('decLetsTitle')} description={t('decLetsDescription')} collapsible defaultCollapsed={st.lets.length === 0}>
            <div className="space-y-3">
              {st.lets.map((l, i) => (
                <div key={l.id} data-row-path={`let[${i}]`} className="flex items-end gap-2">
                  <div className="w-40">
                    <label htmlFor={`let-name-${l.id}`} className="mb-1 block text-xs font-medium text-muted-foreground">{t('decLetName')}</label>
                    <Input id={`let-name-${l.id}`} className="font-mono" value={l.name} onChange={(e) => edit({ ...st, lets: st.lets.map((x) => (x.id === l.id ? { ...x, name: e.target.value } : x)) })} />
                  </div>
                  <div className="flex-1">
                    <label htmlFor={`let-expr-${l.id}`} className="mb-1 block text-xs font-medium text-muted-foreground">{t('decExpression')}</label>
                    <Input id={`let-expr-${l.id}`} className="font-mono" value={l.expr} onChange={(e) => edit({ ...st, lets: st.lets.map((x) => (x.id === l.id ? { ...x, expr: e.target.value } : x)) })} />
                  </div>
                  <IconButton label={t('decRemoveLet', { n: i + 1 })} onClick={() => edit({ ...st, lets: st.lets.filter((x) => x.id !== l.id) })}>
                    <Trash2 size={14} />
                  </IconButton>
                  {built.issues.filter((x) => x.path.startsWith(`let[${i}]`)).map((x, n) => (
                    <span key={n} className="sr-only">{t(`decIssue.${x.code}`, x.params)}</span>
                  ))}
                </div>
              ))}
              <Button size="sm" variant="outline" disabled={st.lets.length >= LIMITS.lets} onClick={() => edit({ ...st, lets: [...st.lets, { id: newId(), name: '', expr: '' }] })}>
                <Plus size={14} /> {t('decAddLet')}
              </Button>
            </div>
          </SectionPanel>

          <SectionPanel title={t('decVectorsTitle')}>
            <VectorsPanel
              vectors={st.vectors}
              onChange={(vectors: VectorState[]) => edit({ ...st, vectors })}
              outcomes={outcomes}
              compileError={vectorRun && vectorRun.text === built.text ? vectorRun.compileError : null}
              issues={built.issues}
              named={named}
              inputKind={st.input}
            />
          </SectionPanel>
        </fieldset>

        <SectionPanel title={t('decTestTitle')} description={t('decTestDescription')}>
          <TestPanel
            profileToken={profileToken}
            documentText={built.text}
            inputKind={st.input}
            canWriteSamples={canWrite}
            maxReadings={built.maxReadings}
            run={run}
            onRun={(r) => {
              setRun(r);
              setVectorRun({ text: r.documentText, outcomes: r.result.vectors, compileError: r.result.error && (r.result.error.reason === 'compile_failed' || r.result.error.reason === 'env_unsupported') ? r.result.error.message : null });
            }}
            onSaveVector={(v) => {
              if (!canWrite) return;
              edit({ ...st, vectors: [...st.vectors, { ...v, name: t('decNewVectorName', { n: st.vectors.length + 1 }) }] });
              toast(t('decVectorAdded'));
            }}
            initialSampleId={initialSampleId}
            errorExpression={errorExpression}
          />
        </SectionPanel>
      </div>
      <div className="lg:sticky lg:top-4 lg:self-start">
        <FunctionReference />
      </div>
    </div>
  );
}
