// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The "waiting for maintenance window" line under a command row. Renders nothing for a
// command that is not waiting (see lib/command-waiting.ts for the rule).

import { useTranslation } from 'react-i18next';
import { Clock } from 'lucide-react';
import { commandWait } from '@/lib/command-waiting';

export function CommandWaitingNote({
  command,
}: {
  command: { status: string; dispatchAfter?: string | null; maintenanceWindowToken?: string | null };
}) {
  const { t } = useTranslation('common');
  const wait = commandWait(command);
  if (!wait) return null;
  return (
    <span
      className="mt-0.5 flex w-fit items-center gap-1 text-xs font-normal text-muted-foreground"
      title={wait.windowToken ? t('commandWaitingWindowTitle', { token: wait.windowToken }) : undefined}
      data-testid="command-waiting"
    >
      <Clock size={12} />
      {t('commandWaitingForWindow', { until: wait.until.toLocaleString() })}
    </span>
  );
}
