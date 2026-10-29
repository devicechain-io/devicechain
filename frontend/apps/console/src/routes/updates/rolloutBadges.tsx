// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pieces every rollout surface shares: the state badge, the outcome badge and the progress
// bar, so the list row and the detail header never disagree about what a number means.
//
// 🔴 THE BAR COUNTS UNKNOWN WITH FAILED, in the same colour, because the service does: a device
// whose outcome is unknown counts toward the failure threshold and holds its slot. Painting it
// as neutral would show a rollout as healthier than the rule that pauses it believes it is.
import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { UpdateRollout, UpdateRolloutOutcome, UpdateRolloutState } from '@/lib/api/update-management';
import { rolloutOutcomeLabel, rolloutPauseReasonLabel, rolloutStateLabel } from '@/lib/enumLabel';
import '@/i18n/lazy/updates';

export function RolloutStateBadge({ state }: { state: UpdateRolloutState }) {
  const { t } = useTranslation('updates');
  const label = rolloutStateLabel(t, state);
  switch (state) {
    case 'RUNNING':
      return <Badge variant="secondary">{label}</Badge>;
    case 'PAUSED':
      return <Badge variant="warning">{label}</Badge>;
    case 'COMPLETED':
      return <Badge variant="success">{label}</Badge>;
    case 'ABORTED':
      return <Badge variant="destructive">{label}</Badge>;
    default:
      return <Badge variant="outline">{label}</Badge>;
  }
}

export function RolloutOutcomeBadge({ outcome }: { outcome: UpdateRolloutOutcome }) {
  const { t } = useTranslation('updates');
  const label = rolloutOutcomeLabel(t, outcome);
  switch (outcome) {
    case 'SUCCEEDED':
      return <Badge variant="success">{label}</Badge>;
    case 'FAILED':
    case 'UNKNOWN':
      return <Badge variant="destructive">{label}</Badge>;
    case 'IN_FLIGHT':
      return <Badge variant="secondary">{label}</Badge>;
    case 'PENDING':
      return <Badge variant="warning">{label}</Badge>;
    default:
      return <Badge variant="outline">{label}</Badge>;
  }
}

/** The pause reason in words, or null when the rollout is not paused. */
export function pauseReasonText(t: Parameters<typeof rolloutPauseReasonLabel>[0], r: UpdateRollout): string | null {
  return r.state === 'PAUSED' && r.pauseReason ? rolloutPauseReasonLabel(t, r.pauseReason) : null;
}

type Progress = UpdateRollout['progress'];

const SEGMENTS: Array<{ key: 'succeeded' | 'failed' | 'inFlight' | 'other'; className: string }> = [
  { key: 'succeeded', className: 'bg-success-fill' },
  { key: 'failed', className: 'bg-destructive-fill' },
  { key: 'inFlight', className: 'bg-primary' },
  { key: 'other', className: 'bg-muted-foreground/40' },
];

export function RolloutProgressBar({ progress, className }: { progress: Progress; className?: string }) {
  const { t } = useTranslation('updates');
  const parts = {
    succeeded: progress.succeeded,
    failed: progress.failed + progress.unknown,
    inFlight: progress.inFlight,
    other: progress.skipped + progress.cancelled,
  };
  const total = Math.max(progress.total, 1);
  const done = progress.total - progress.pending - progress.inFlight - progress.unknown;
  return (
    <div className={className}>
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={progress.total}
        aria-valuenow={done}
        aria-label={t('rolloutProgressLabel')}
        className="flex h-2 w-full overflow-hidden rounded-full bg-muted"
      >
        {SEGMENTS.map((s) =>
          parts[s.key] > 0 ? (
            <div key={s.key} className={s.className} style={{ width: `${(parts[s.key] / total) * 100}%` }} />
          ) : null,
        )}
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        {t('rolloutProgressSummary', {
          succeeded: progress.succeeded,
          total: progress.total,
          failed: parts.failed,
          inFlight: progress.inFlight,
        })}
      </p>
    </div>
  );
}
