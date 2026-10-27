// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a `sparkplug.*` command's status MEANS, under its badge in the command history.
//
// Sparkplug has no command acknowledgement, so the badge alone over-claims or under-claims:
// SUCCESSFUL reads as "the device did it" when the platform saw a report of the value, and
// TIMEOUT reads as "it failed" when the command may well have been obeyed and not reported.
// This says which it is. Renders nothing for any other command.

import { useTranslation } from 'react-i18next';
import { Info, TriangleAlert } from 'lucide-react';
import { sparkplugOutcome } from './sparkplugCommands';

function when(iso: string | undefined): string | undefined {
  if (!iso) return undefined;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? undefined : d.toLocaleString();
}

export function SparkplugOutcomeNote({
  command,
}: {
  command: { name: string; status: string; responsePayload?: string | null };
}) {
  const { t } = useTranslation('devices');
  const { outcome, secondaryHost } = sparkplugOutcome(command);
  if (!outcome && !secondaryHost) return null;

  let text: string | null = null;
  switch (outcome?.kind) {
    case 'inFlight':
      text = t('sparkplugOutcomeInFlight');
      break;
    case 'unconfirmed':
      text = t('sparkplugOutcomeUnconfirmed');
      break;
    case 'confirmedByReport':
      text = t('sparkplugOutcomeConfirmed', {
        metric: outcome.metric ?? '',
        value: outcome.value ?? '',
        at: when(outcome.at) ?? t('sparkplugOutcomeUnknownTime'),
        via: outcome.via,
      });
      break;
    case 'confirmedByBirth':
      text = t('sparkplugOutcomeConfirmedBirth', { at: when(outcome.at) ?? t('sparkplugOutcomeUnknownTime') });
      break;
    case 'alreadyAtValue':
      text = t('sparkplugOutcomeUnchanged', {
        metric: outcome.metric ?? '',
        value: outcome.value ?? '',
        since: when(outcome.since) ?? t('sparkplugOutcomeUnknownTime'),
      });
      break;
  }

  return (
    <>
      {text && (
        <span
          className="mt-0.5 flex w-fit max-w-xs items-start gap-1 text-xs font-normal text-muted-foreground"
          data-testid="sparkplug-outcome"
        >
          <Info size={12} className="mt-0.5 shrink-0" />
          {text}
        </span>
      )}
      {secondaryHost && (
        <span
          className="mt-0.5 flex w-fit max-w-xs items-start gap-1 text-xs font-normal text-warning"
          data-testid="sparkplug-secondary-host"
        >
          <TriangleAlert size={12} className="mt-0.5 shrink-0" />
          {t('sparkplugSecondaryHostWarning')}
        </span>
      )}
    </>
  );
}
