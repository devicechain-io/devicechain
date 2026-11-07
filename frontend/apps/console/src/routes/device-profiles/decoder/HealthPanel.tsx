// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How the decoder has been doing: the last day's counts (every uplink it saw lands in exactly one of
// decoded, ignored, failed or suppressed) and the failing uplinks kept as samples, each loadable
// into the test panel. Both reads need device:write, so the tab mounts this only for such a caller.

import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { LoadingState } from '@/components/ui/loading-state';
import { ErrorState } from '@/components/ui/error-state';
import { useQuery } from '@/lib/hooks/use-query';
import { formatTime } from '@/lib/utils';
import { getPayloadDecoderHealth, listPayloadDecoderSamples } from '@/lib/api/payload-decoder';
import '@/i18n/lazy/deviceProfiles';

/** The span the strip reports, in minutes: one day. */
export const HEALTH_MINUTES = 1440;

/** The failed-events page filtered to decoder failures. */
export const DECODE_FAILED_LINK = '/ingest-failures?reason=DECODE_FAILED';

const FIGURES = ['decoded', 'ignored', 'failed', 'suppressed'] as const;

interface Props {
  profileToken: string;
  /** Bumped by the editor after a save, so the figures are read again. */
  refreshKey?: number;
  /** Load a kept failure into the test panel. */
  onLoadSample: (sampleId: string) => void;
}

export function HealthPanel({ profileToken, refreshKey = 0, onLoadSample }: Props) {
  const { t, i18n } = useTranslation('deviceProfiles');
  const [tick, setTick] = useState(0);
  const deps = [profileToken, refreshKey, tick];
  const health = useQuery(() => getPayloadDecoderHealth(profileToken, HEALTH_MINUTES), deps);
  const samples = useQuery(() => listPayloadDecoderSamples(profileToken), deps);

  const h = health.current;
  const s = samples.current;
  const error = health.error ?? samples.error;
  if (error && (h === undefined || s === undefined)) return <ErrorState description={error} />;
  if (h === undefined || s === undefined) return <LoadingState description={t('decHealthLoading')} />;

  const number = new Intl.NumberFormat(i18n.language);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('decHealthHint')}</p>
        <Button
          size="sm"
          variant="outline"
          onClick={() => setTick((n) => n + 1)}
        >
          <RefreshCw size={14} /> {t('decHealthRefresh')}
        </Button>
      </div>

      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4" data-testid="decoder-health">
        {FIGURES.map((k) => (
          <div key={k} className="rounded-md border p-3" data-figure={k}>
            <dt className="text-xs font-medium text-muted-foreground">{t(`decHealth.${k}`)}</dt>
            <dd className="mt-1 text-xl font-semibold tabular-nums">{number.format(h[k])}</dd>
            <p className="mt-1 text-xs text-muted-foreground">{t(`decHealthHint_${k}`)}</p>
          </div>
        ))}
      </dl>
      {h.suppressed > 0 && (
        <p role="status" className="rounded-md border border-warning/40 bg-warning/10 p-3 text-sm">
          {t('decHealthSuppressedNote')}
        </p>
      )}

      <div className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-sm font-semibold">{t('decFailuresTitle')}</h4>
          <Link className="text-sm underline" to={DECODE_FAILED_LINK}>
            {t('decFailuresLink')}
          </Link>
        </div>
        {s.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="no-failures">
            {t('decFailuresNone')}
          </p>
        ) : (
          <ul className="divide-y rounded-md border" data-testid="decoder-failures">
            {s.map((x) => (
              <li key={x.id} data-sample-id={x.id} className="flex flex-wrap items-center justify-between gap-2 p-3 text-sm">
                <div className="min-w-0">
                  <p className="break-words">
                    <span className="font-mono">{x.reason}</span>
                    <span className="text-muted-foreground"> · {x.deviceToken} · {t('decFailureVersion', { version: x.profileVersion })}</span>
                  </p>
                  <p className="break-words text-xs text-muted-foreground">{x.error}</p>
                  <p className="text-xs text-muted-foreground">{formatTime(x.createdAt)}</p>
                </div>
                <Button size="sm" variant="outline" onClick={() => onLoadSample(x.id)} aria-label={t('decFailureLoadFor', { id: x.id })}>
                  {t('decFailureLoad')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
