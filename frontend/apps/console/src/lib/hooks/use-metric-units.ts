// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react';
import { getDeviceMetricVocabulary } from '@/lib/api/device-management';

type UnitTable = Record<string, Record<string, string>>;

/**
 * Looks up the unit each device's profile declares for each metric, for the devices named, and
 * returns a lookup `(deviceToken, metricKey) => unit`. The vocabulary is the published one - the
 * same the device's measurements are validated against. It is best-effort: a device whose
 * vocabulary cannot be read (no device:read, a deleted device, a failed request) simply has no
 * units, and the value is shown bare. Each device is read once while the hook lives.
 */
export function useMetricUnits(deviceTokens: (string | null | undefined)[]) {
  const [units, setUnits] = useState<UnitTable>({});
  const asked = useRef(new Set<string>());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const wanted = [...new Set(deviceTokens.filter((t): t is string => !!t))].sort();
  const key = wanted.join('\n');

  useEffect(() => {
    for (const token of wanted) {
      if (asked.current.has(token)) continue;
      asked.current.add(token);
      getDeviceMetricVocabulary(token).then(
        (metrics) => {
          if (!mounted.current) return;
          const table: Record<string, string> = {};
          for (const m of metrics ?? []) if (m.unit) table[m.metricKey] = m.unit;
          setUnits((prev) => ({ ...prev, [token]: table }));
        },
        () => {
          // Forget the failure so a later render may try again.
          asked.current.delete(token);
        },
      );
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return (deviceToken: string | null | undefined, metricKey: string): string | undefined =>
    deviceToken ? units[deviceToken]?.[metricKey] : undefined;
}
