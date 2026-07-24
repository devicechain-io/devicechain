// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Alarm-specific status pills. Severity and the state×acknowledged status are the
// two at-a-glance signals in the alarm list, so each gets a dedicated pill with
// semantic colour (kept local here rather than widening the shared Badge variants,
// which are tied to the app's accent/CSS-variable palette). Colour is backed by an
// icon + label so status never reads on hue alone.
import { AlertTriangle, BellRing, CircleCheck, UserCheck } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';

const pillBase =
  'inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-semibold';

// Severity ramp, descending urgency: CRITICAL → MAJOR → MINOR → WARNING →
// INDETERMINATE (ADR-041 ordering). Warm-to-cool so the eye ranks rows without
// reading the label. An unknown value falls back neutral rather than blank.
const SEVERITY_STYLES: Record<string, string> = {
  CRITICAL: 'border-transparent bg-red-600 text-white dark:bg-red-500',
  MAJOR: 'border-transparent bg-orange-500 text-white dark:bg-orange-500',
  MINOR: 'border-transparent bg-amber-400 text-amber-950 dark:bg-amber-400 dark:text-amber-950',
  WARNING: 'border-transparent bg-sky-500 text-white dark:bg-sky-500',
  INDETERMINATE: 'border-border bg-muted text-muted-foreground',
};

const SEVERITY_LABELS: Record<string, string> = {
  CRITICAL: 'sevCritical',
  MAJOR: 'sevMajor',
  MINOR: 'sevMinor',
  WARNING: 'sevWarning',
  INDETERMINATE: 'sevIndeterminate',
};

export function AlarmSeverityBadge({ severity }: { severity: string }) {
  const { t } = useTranslation('alarms');
  const style = SEVERITY_STYLES[severity] ?? SEVERITY_STYLES.INDETERMINATE;
  return <span className={cn(pillBase, style)}>{t(SEVERITY_LABELS[severity] ?? 'sevIndeterminate')}</span>;
}

// AlarmStatusBadge collapses the four-state model (state × acknowledged) into the
// one signal an operator reads: an unacknowledged active alarm demands attention
// (red), an acknowledged one is owned but still live (amber), and a cleared one is
// resolved (muted). Cleared wins regardless of the acknowledged flag.
export function AlarmStatusBadge({
  state,
  acknowledged,
}: {
  state: string;
  acknowledged: boolean;
}) {
  const { t } = useTranslation('alarms');
  if (state === 'CLEARED') {
    return (
      <span className={cn(pillBase, 'border-border bg-muted text-muted-foreground')}>
        <CircleCheck size={12} /> {t('stateCleared')}
      </span>
    );
  }
  if (acknowledged) {
    return (
      <span
        className={cn(
          pillBase,
          'border-amber-500/30 bg-amber-500/15 text-amber-700 dark:text-amber-400',
        )}
      >
        <UserCheck size={12} /> {t('ackAcknowledged')}
      </span>
    );
  }
  return (
    <span
      className={cn(
        pillBase,
        'border-red-500/30 bg-red-500/15 text-red-700 dark:text-red-400',
      )}
    >
      <BellRing size={12} /> {t('stateActive')}
    </span>
  );
}

// AlarmEventTypeBadge labels a live transition (RAISED/ESCALATED/…): shown on the
// most-recent-activity line so an operator sees what just changed. Purely a muted
// accent — the severity/status pills already carry the colour weight.
const EVENT_ICONS: Record<string, typeof AlertTriangle> = {
  RAISED: BellRing,
  ESCALATED: AlertTriangle,
  DEESCALATED: AlertTriangle,
  CLEARED: CircleCheck,
  ACKNOWLEDGED: UserCheck,
};

const EVENT_LABELS: Record<string, string> = {
  RAISED: 'eventRaised',
  ESCALATED: 'eventEscalated',
  DEESCALATED: 'eventDeescalated',
  CLEARED: 'eventCleared',
  ACKNOWLEDGED: 'eventAcknowledged',
};

export function AlarmEventTypeBadge({ eventType }: { eventType: string }) {
  const { t } = useTranslation('alarms');
  const Icon = EVENT_ICONS[eventType] ?? AlertTriangle;
  return (
    <span className={cn(pillBase, 'border-border bg-muted text-muted-foreground')}>
      <Icon size={12} /> {t(EVENT_LABELS[eventType] ?? 'eventUnknown')}
    </span>
  );
}
