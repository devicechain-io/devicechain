// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EXPERIMENT: outcome objects. A page over event-management's evaluateOutcome: each outcome is a
// target over one measurement, judged per area, with an error budget and the spread between the
// worst area and the median. Off unless the instance enables it; says so when it is not.
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import '@/i18n/lazy/outcomes';
import { errorCode, GraphQLRequestError } from '@devicechain/client';
import { useQuery } from '@/lib/hooks/use-query';
import { evaluateOutcome, type OutcomeEvaluation } from '@/lib/api/outcomes';
import { PageShell } from '@/components/ui/page-shell';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { LoadingState } from '@/components/ui/loading-state';
import { EmptyState } from '@/components/ui/empty-state';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { DEFAULT_AREAS, SEED_OUTCOMES, parseAreas, type SeedOutcome } from './seed';

type Judged =
  | { kind: 'ok'; evaluation: OutcomeEvaluation }
  | { kind: 'off' }
  | { kind: 'error'; message: string };

const STORAGE_KEY = 'dc.outcomes.areas.v1';

function loadAreas(): string {
  try {
    return window.localStorage.getItem(STORAGE_KEY) ?? DEFAULT_AREAS.join(', ');
  } catch {
    return DEFAULT_AREAS.join(', ');
  }
}

export function statusVariant(status: string): 'success' | 'warning' | 'destructive' | 'outline' {
  switch (status) {
    case 'OK':
      return 'success';
    case 'AT_RISK':
    case 'NO_DATA':
      return 'warning';
    case 'BREACHED':
      return 'destructive';
    default:
      return 'outline';
  }
}

export function pct(v: number | null | undefined, digits = 1): string {
  return v == null ? '—' : `${(v * 100).toFixed(digits)}%`;
}

async function judge(seed: SeedOutcome, areas: string[]): Promise<Judged> {
  try {
    return { kind: 'ok', evaluation: await evaluateOutcome({ ...seed.definition, areas }) };
  } catch (err) {
    if (errorCode(err) === 'UNAVAILABLE') return { kind: 'off' };
    return { kind: 'error', message: err instanceof GraphQLRequestError ? err.message : 'Failed to load data.' };
  }
}

export default function OutcomesPage() {
  const { t } = useTranslation('outcomes');
  const [text, setText] = useState(loadAreas);
  const [applied, setApplied] = useState(() => parseAreas(loadAreas()));
  // Counts presses of Evaluate: an evaluation is judged against the clock, so pressing it again with
  // the same areas must ask again rather than be taken for no change.
  const [run, setRun] = useState(0);

  const results = useQuery(
    () => Promise.all(SEED_OUTCOMES.map((s) => judge(s, applied))),
    [applied.join('\u0000'), run],
  );

  const apply = () => {
    try {
      window.localStorage.setItem(STORAGE_KEY, text);
    } catch {
      /* a viewer convenience only */
    }
    setApplied(parseAreas(text));
    setRun((n) => n + 1);
  };

  const off = results.data?.every((r) => r.kind === 'off');

  return (
    <PageShell
      title={t('title')}
      description={
        <span className="flex items-center gap-2">
          <Badge variant="warning">{t('experimental')}</Badge>
          <span>{t('description')}</span>
        </span>
      }
    >
      <div className="mb-6 flex max-w-3xl items-end gap-2">
        <label className="flex-1 text-sm">
          <span className="mb-1 block text-muted-foreground">{t('areasLabel')}</span>
          <Input
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && apply()}
            aria-label={t('areasLabel')}
          />
        </label>
        <Button type="button" variant="outline" onClick={apply}>
          {t('evaluate')}
        </Button>
      </div>

      {applied.length === 0 ? (
        <EmptyState description={t('noAreas')} />
      ) : results.loading && !results.data ? (
        <LoadingState description={t('loading')} />
      ) : off ? (
        <EmptyState description={t('notEnabled')} />
      ) : (
        <div className="space-y-6" data-shot="outcomes">
          {SEED_OUTCOMES.map((seed, i) => (
            <OutcomeCard key={seed.definition.token} seed={seed} judged={results.data?.[i]} />
          ))}
        </div>
      )}
    </PageShell>
  );
}

function OutcomeCard({ seed, judged }: { seed: SeedOutcome; judged: Judged | undefined }) {
  const { t } = useTranslation('outcomes');
  const d = seed.definition;
  const rule = t('rule', {
    aggregation: t(`aggregation_${d.aggregation}`),
    metric: d.metric,
    comparator: t(`comparator_${d.comparator}`),
    threshold: d.threshold,
    objective: pct(d.objective, 0),
    minutes: Math.round(d.windowSeconds / 60),
    interval: Math.round(d.intervalSeconds / 60),
  });
  return (
    <section className="rounded-lg border border-border bg-card p-4" aria-label={t(seed.nameKey)}>
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">{t(seed.nameKey)}</h2>
        {judged?.kind === 'ok' && (
          <Badge variant={statusVariant(judged.evaluation.status)}>{t(`status_${judged.evaluation.status}`)}</Badge>
        )}
      </div>
      <p className="mt-1 text-sm text-muted-foreground">{rule}</p>
      {judged?.kind === 'error' && <p className="mt-3 text-sm text-destructive">{judged.message}</p>}
      {judged?.kind === 'ok' && <Evaluation ev={judged.evaluation} />}
    </section>
  );
}

function Evaluation({ ev }: { ev: OutcomeEvaluation }) {
  const { t } = useTranslation('outcomes');
  const e = ev.equity;
  return (
    <>
      <div className="mt-4 grid gap-3 sm:grid-cols-3" data-shot="equity">
        <Stat label={t('pooled')} value={pct(e.pooled)} hint={t('pooledHint')} />
        <Stat
          label={t('worstArea')}
          value={e.spreadUndefined ? '—' : `${e.worstArea} · ${pct(e.worst)}`}
          hint={e.spreadUndefined ? t('spreadUndefined') : t('medianHint', { median: pct(e.median) })}
        />
        <Stat
          label={t('spread')}
          value={e.spreadUndefined ? '—' : pct(e.spread)}
          hint={e.breached ? t('spreadBreached') : e.complete ? undefined : t('incomplete', { count: e.excludedAreas })}
          tone={e.breached ? 'bad' : undefined}
        />
      </div>
      <div className="mt-4">
        <DataTable>
          <DataTableHead>
            <DataTableHeaderCell>{t('colArea')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colStatus')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colCompliance')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colBudget')}</DataTableHeaderCell>
            <DataTableHeaderCell>{t('colBuckets')}</DataTableHeaderCell>
          </DataTableHead>
          <DataTableBody>
            {ev.areas.map((a) => (
              <DataTableRow key={a.area}>
                <DataTableCell>{a.area}</DataTableCell>
                <DataTableCell>
                  <Badge variant={statusVariant(a.status)}>{t(`status_${a.status}`)}</Badge>
                </DataTableCell>
                <DataTableCell>
                  <div className="flex items-center gap-2">
                    <div className="h-2 w-28 overflow-hidden rounded bg-muted" aria-hidden>
                      <div
                        className={a.status === 'BREACHED' ? 'h-full bg-destructive-fill' : 'h-full bg-success-fill'}
                        style={{ width: `${Math.max(0, Math.min(1, a.compliance)) * 100}%` }}
                      />
                    </div>
                    <span className="tabular-nums">{pct(a.compliance)}</span>
                  </div>
                </DataTableCell>
                <DataTableCell className="tabular-nums">
                  {a.status === 'NO_DATA' ? '—' : pct(a.budgetRemaining, 0)}
                </DataTableCell>
                <DataTableCell className="tabular-nums text-muted-foreground">
                  {t('buckets', { good: a.goodBuckets, bad: a.badBuckets, empty: a.emptyBuckets })}
                </DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      </div>
    </>
  );
}

function Stat({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: 'bad' }) {
  return (
    <div className="rounded-md border border-border p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={tone === 'bad' ? 'text-lg font-semibold text-destructive' : 'text-lg font-semibold'}>{value}</div>
      {hint && <div className="mt-0.5 text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}
