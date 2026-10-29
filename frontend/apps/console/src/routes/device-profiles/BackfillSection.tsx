// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "Backfilling history" part of a profile's Versions tab: the sentence that says calculated
// metrics start when a version is published, the button that opens the backfill dialog, and the
// jobs. It is for staff only. A customer-scoped caller never sees any of it, because a backfill is
// a whole-tenant operation with no scope to apply; a caller without device:write sees the jobs but
// not the start and cancel controls.
//
// The start button is DISABLED, never hidden, when there is nothing to fill or the server has said
// it cannot run a job, and the reason is written beside it. The instance is asked once when the
// section mounts (calculatedMetricBackfillAvailability, no polling); when that read is refused or
// fails, the first UNAVAILABLE from a start is remembered here until the page is left instead.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { History } from 'lucide-react';
import { hasAuthority, isScoped } from '@devicechain/client';
import { useAuth } from '@/auth/AuthProvider';
import { Button } from '@/components/ui/button';
import { HintText } from '@/components/ui/hint-text';
import { useQuery } from '@/lib/hooks/use-query';
import { getCalculatedMetricPlan } from '@/lib/api/device-management';
import { getCalculatedMetricBackfillAvailability } from '@/lib/api/event-management';
import { BackfillDrawer } from './BackfillDrawer';
import { BackfillJobs } from './BackfillJobs';
import '@/i18n/lazy/deviceProfiles';

const DEVICE_WRITE = 'device:write';
const EVENT_READ = 'event:read';

export function BackfillSection({
  profileToken,
  activeVersion,
  request,
  onRequestHandled,
}: {
  profileToken: string;
  activeVersion: number | null;
  /** A version the parent wants the dialog opened on, e.g. the one just published. */
  request: number | null;
  onRequestHandled: () => void;
}) {
  const { t } = useTranslation('deviceProfiles');
  const { claims } = useAuth();
  const scoped = isScoped(claims);
  const canWrite = hasAuthority(claims, DEVICE_WRITE);
  const canRead = hasAuthority(claims, EVENT_READ);
  const [open, setOpen] = useState(false);
  const [initial, setInitial] = useState<number | null>(null);
  const [refused, setRefused] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  // Whether the active version calculates anything at all decides if there is something to fill.
  const plan = useQuery(
    () => (!scoped && activeVersion != null ? getCalculatedMetricPlan(profileToken) : Promise.resolve(null)),
    [scoped, profileToken, activeVersion],
  );

  // The same decision a start is refused by, read up front, and only for a caller who can start
  // one: nobody else sees the control it disables. Needs event:read; without it, or when the read
  // fails, the remembered refusal below is the only signal.
  const availability = useQuery(
    () => (!scoped && canWrite && canRead ? getCalculatedMetricBackfillAvailability() : Promise.resolve(null)),
    [scoped, canWrite, canRead],
  );
  // available: false always carries a reason; the fallback only keeps a blank one from reading as available.
  const probed = availability.current && !availability.current.available ? (availability.current.reason || t('backfillUnavailableNoReason')) : null;
  const unavailable = probed ?? refused;

  useEffect(() => {
    if (request == null) return;
    setInitial(request);
    setOpen(true);
    onRequestHandled();
  }, [request, onRequestHandled]);

  if (scoped) return null;

  const hasCalculated = (plan.current?.calculated.length ?? 0) > 0;
  const reason = unavailable
    ? t('backfillUnavailable', { reason: unavailable })
    : activeVersion == null
      ? t('backfillNeedsVersion')
      : plan.current && !hasCalculated
        ? t('backfillNoCalculated')
        : null;

  return (
    <section className="space-y-3 border-t pt-4" data-testid="backfill-section">
      <h3 className="text-sm font-semibold">{t('backfillSectionTitle')}</h3>
      <div className="flex items-start justify-between gap-4">
        <div className="max-w-prose space-y-1">
          <HintText size="md">{t('backfillStartsAtActivation')}</HintText>
          {reason && (
            <p className="text-sm text-amber-600 dark:text-amber-500" data-testid="backfill-disabled-reason">
              {reason}
            </p>
          )}
        </div>
        {canWrite && (
          <Button
            size="sm"
            variant="outline"
            className="shrink-0"
            disabled={reason != null}
            onClick={() => {
              setInitial(null);
              setOpen(true);
            }}
          >
            <History size={16} /> {t('backfillButton')}
          </Button>
        )}
      </div>

      {canRead && <BackfillJobs profileToken={profileToken} canCancel={canWrite} reloadKey={reloadKey} />}

      {canWrite && (
        <BackfillDrawer
          profileToken={profileToken}
          activeVersion={activeVersion}
          initialVersion={initial}
          open={open}
          onOpenChange={setOpen}
          unavailable={unavailable}
          onUnavailable={setRefused}
          onStarted={() => setReloadKey((n) => n + 1)}
        />
      )}
    </section>
  );
}
