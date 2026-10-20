// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { ToggleButton } from '@/components/ui/toggle-button';
import { getDeviceMetricVocabulary } from '@/lib/api/device-management';
import { useQuery } from '@/lib/hooks/use-query';

// MetricChoices lists the metrics the bound device's profile publishes, as buttons that
// add or remove the key from the widget's measurements, and marks a calculated one. It
// sits BESIDE the free-text field rather than replacing it: a device may report a key its
// profile never declared, and the vocabulary is only what the published version knows.
//
// A failed, pending or empty lookup renders nothing. The text field above stays the authority; this
// is a convenience, and an error banner under a field that still works would be noise.
export function MetricChoices({
  deviceToken,
  measurements,
  onChange,
}: {
  deviceToken: string;
  measurements: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation('dashboards');
  const vocab = useQuery(
    () => (deviceToken ? getDeviceMetricVocabulary(deviceToken) : Promise.resolve(null)),
    [deviceToken],
  );
  // `current`, not `data`: data may still be the PREVIOUS device's list (while a new device
  // loads, after a failed lookup, and in the render before the new load starts). Showing it
  // would offer, and let a click write, keys the bound device may not declare.
  const metrics = vocab.current ?? [];
  if (metrics.length === 0) return null;

  const toggle = (key: string) =>
    onChange(measurements.includes(key) ? measurements.filter((m) => m !== key) : [...measurements, key]);

  return (
    <ul className="mt-2 flex flex-wrap gap-1.5" aria-label={t('widgetMetricChoicesLabel')}>
      {metrics.map((m) => {
        const chosen = measurements.includes(m.metricKey);
        return (
          <li key={m.metricKey}>
            <ToggleButton
              pressed={chosen}
              onClick={() => toggle(m.metricKey)}
              className={
                'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs ' +
                (chosen ? 'border-primary bg-primary/10' : 'hover:bg-muted')
              }
            >
              <span className="font-mono">{m.metricKey}</span>
              {m.calculated && (
                <Badge variant="secondary" title={t('widgetMetricCalculatedTitle')}>
                  {t('widgetMetricCalculated')}
                </Badge>
              )}
            </ToggleButton>
          </li>
        );
      })}
    </ul>
  );
}
