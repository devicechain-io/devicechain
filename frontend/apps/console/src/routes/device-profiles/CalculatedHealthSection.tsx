// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// "Are my calculated metrics being computed?", on the Versions tab, for the ACTIVE version only:
// a superseded version resolves for no device, so a block on it would read "no readings" forever.
//
// Everything shown is counted from stored readings (see calcHealth.ts); nothing is sampled or
// estimated, and nothing here evaluates a formula. "Why?" opens the formula-history view, which
// evaluates the formula over one device's stored readings and tallies the reasons it produced
// nothing, so the question a status raises can be answered on a device that has the inputs.
//
// Staff only. A customer-scoped caller never sees it, because the counts are tenant-wide.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { HintText } from '@/components/ui/hint-text';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { FormDrawer } from '@/components/registry';
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeaderCell,
  DataTableRow,
} from '@/components/ui/data-table';
import { useQuery } from '@/lib/hooks/use-query';
import { FormulaHistory } from './FormulaHistory';
import { skipLabel } from './FormulaEditor';
import {
  HEALTH_WINDOWS,
  loadCalcHealth,
  showsSeparateInputsHint,
  statusOf,
  type HealthStatus,
  type HealthWindow,
  type MetricHealth,
  type WindowCounts,
} from './calcHealth';

const DEVICE_READ = 'device:read';
const EVENT_READ = 'event:read';

const SEPARATE_INPUTS_GUIDE_URL = 'https://docs.devicechain.io/user-guide/calculated-metrics#inputs-that-arrive-separately';

const STATUS_VARIANT: Record<HealthStatus, 'success' | 'destructive' | 'secondary' | 'warning'> = {
  computing: 'success',
  unconfirmed: 'warning',
  not_computing: 'destructive',
  no_input_readings: 'secondary',
};

export function CalculatedHealthSection({
  profileToken,
  activeVersion,
}: {
  profileToken: string;
  activeVersion: number | null;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { claims } = useAuth();
  const allowed = !isScoped(claims) && hasAuthority(claims, DEVICE_READ) && hasAuthority(claims, EVENT_READ);
  const [hours, setHours] = useState<HealthWindow>(24);
  const [why, setWhy] = useState<string | null>(null);

  const health = useQuery(
    () => (allowed && activeVersion != null ? loadCalcHealth(profileToken, new Date()) : Promise.resolve(null)),
    [allowed, profileToken, activeVersion],
  );

  if (!allowed || activeVersion == null) return null;
  // A profile whose active version calculates nothing has nothing to report.
  if (!health.loading && !health.error && health.data == null) return null;

  // Opening the formula view is an action on one metric's data: it reads `current`.
  const asked = health.current?.metrics.find((m) => m.key === why) ?? null;

  return (
    <section className="space-y-3 border-t pt-4" data-testid="calc-health-section">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="max-w-prose space-y-1">
          <h3 className="text-sm font-semibold">{t('calcHealthTitle')}</h3>
          <HintText size="md">{t('calcHealthIntro')}</HintText>
        </div>
        <SegmentedControl
          ariaLabel={t('calcHealthWindow')}
          tone="joined"
          value={String(hours)}
          onValueChange={(v) => setHours(Number(v) as HealthWindow)}
          options={HEALTH_WINDOWS.map((h) => ({ value: String(h), label: t('calcHealthWindowLabel', { hours: h }) }))}
        />
      </div>

      {health.loading && !health.data ? (
        <LoadingState description={t('calcHealthLoading')} />
      ) : health.error ? (
        <ErrorState description={health.error} />
      ) : health.data ? (
        <>
          <p className="text-xs text-muted-foreground" data-testid="calc-health-basis">
            {health.data.activeSince
              ? t('calcHealthSince', { version: health.data.version, when: health.data.activeSince.toLocaleString() })
              : t('calcHealthNoActivation', { version: health.data.version })}
          </p>
          <DataTable>
            <DataTableHead>
              <DataTableHeaderCell>{t('calcHealthColMetric')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('calcHealthColStatus')}</DataTableHeaderCell>
              <DataTableHeaderCell>{t('calcHealthColCounts')}</DataTableHeaderCell>
              <DataTableHeaderCell className="text-right">{t('calcHealthColWhy')}</DataTableHeaderCell>
            </DataTableHead>
            <DataTableBody>
              {health.data.metrics.map((m) => (
                <MetricRow key={m.key} metric={m} hours={hours} onWhy={() => setWhy(m.key)} />
              ))}
            </DataTableBody>
          </DataTable>
        </>
      ) : null}

      <FormDrawer
        open={asked != null}
        onOpenChange={(o) => {
          if (!o) setWhy(null);
        }}
        title={t('calcHealthWhyTitle', { key: asked?.key ?? why ?? '' })}
        description={t('calcHealthWhyDescription')}
      >
        {asked && asked.directInputs && (
          <FormulaHistory
            key={asked.key}
            profileToken={profileToken}
            expression={asked.expression}
            dataType={asked.dataType}
            minValue={asked.minValue == null ? '' : String(asked.minValue)}
            maxValue={asked.maxValue == null ? '' : String(asked.maxValue)}
            inputs={asked.directInputs}
            constants={asked.constants}
            skipLabel={(reason) => skipLabel(t, reason)}
            inputTypes={asked.inputTypes}
          />
        )}
      </FormDrawer>
    </section>
  );
}

function MetricRow({ metric, hours, onWhy }: { metric: MetricHealth; hours: HealthWindow; onWhy: () => void }) {
  const { t } = useTranslation('deviceProfiles');
  const c: WindowCounts | null = metric.windows[hours];
  const status = c ? statusOf(c, metric.nameShared) : 'unknown';
  const variant = status === 'unknown' ? 'secondary' : STATUS_VARIANT[status];
  return (
    <DataTableRow data-testid={`calc-health-row-${metric.key}`}>
      <DataTableCell>
        <span className="font-medium">{metric.key}</span>
        {metric.unit && <span className="ml-1 text-xs text-muted-foreground">{metric.unit}</span>}
      </DataTableCell>
      <DataTableCell>
        <Badge variant={variant} data-testid="calc-health-status" data-status={status}>
          {t(`calcHealthStatus.${status}`)}
        </Badge>
      </DataTableCell>
      <DataTableCell>
        {c ? (
          <div className="space-y-0.5 text-sm tabular-nums">
            <p>{t('calcHealthComputed', { count: c.computed })}</p>
            <p className="text-xs text-muted-foreground">
              {c.inputs.map((i) => `${i.key} ${i.count.toLocaleString()}`).join(' · ')}
            </p>
          </div>
        ) : (
          <HintText size="md">{t('calcHealthInputsUnknown')}</HintText>
        )}
        {showsSeparateInputsHint(status, metric.directInputs) && (
          <p className="mt-1 text-xs text-muted-foreground" data-testid="calc-health-separate">
            {t('calcHealthSeparateInputs')}{' '}
            <a className="underline" href={SEPARATE_INPUTS_GUIDE_URL} target="_blank" rel="noreferrer">
              {t('calcHealthSeparateInputsLink')}
            </a>
          </p>
        )}
        {metric.nameShared && (
          <p className="mt-1 text-xs text-warning" data-testid="calc-health-shared">
            {t('calcHealthShared', { key: metric.key })}
          </p>
        )}
      </DataTableCell>
      <DataTableCell className="text-right">
        <Button size="sm" variant="outline" onClick={onWhy} disabled={!metric.directInputs || metric.directInputs.length === 0}>
          {t('calcHealthWhy')}
        </Button>
      </DataTableCell>
    </DataTableRow>
  );
}
