// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How a batch's refusals are rendered — the thin renderer over `refusals.ts`.
//
// 🔴 SHARED BY TWO SCREENS ON PURPOSE. The same pair of shapes (`refusalCounts`, exact;
// `refusals`, a per-code sample capped at 100) arrives in two different places: on the
// RECORD of a batch that fanned out partially, and on the REJECTION of a batch that was
// refused whole. The rule for reading them is identical and it is the rule that is easy
// to get wrong — presenting the capped sample as the whole set tells an operator that
// 100 devices were refused when 4,312 were. Two copies of this rendering would be two
// chances to lose the "of {{total}}" label, and only one of them would be reviewed on
// the day the other broke.

import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import { HintText } from '@/components/ui/hint-text';
import { sampleCoverage, type RefusalGroup } from './refusals';

export function RefusalGroupBlock({ group }: { group: RefusalGroup }) {
  const { t } = useTranslation('commandBatches');
  const coverage = sampleCoverage(group);
  return (
    <div className="rounded-md border border-border p-4">
      <div className="flex flex-wrap items-center gap-2">
        {/* The code is the backend's stable classification and stays English by policy —
            rendered verbatim, never translated and never prettified. */}
        <Badge variant="destructive">{group.code}</Badge>
        <span className="text-sm font-medium text-foreground">
          {group.count == null
            ? t('refusedUnknownTotal')
            : t('refusedDevices', { count: group.count })}
        </span>
      </div>
      {group.sample.length > 0 && (
        <>
          {/* 🔴 THE LABEL IS THE POINT. The sample is capped per code, so a group of 4,312
              shows 100 names — and presenting those 100 as the whole set tells the
              operator that 4,212 devices they were never shown are fine. */}
          {/* 🔴 THREE-WAY, AND THE THIRD BRANCH IS THE ONE THAT WAS MISSING. With no exact
              count there is no basis for saying the names below are all of them — and saying
              so directly under a badge reading "total not reported" is a screen contradicting
              itself in two adjacent lines. */}
          <HintText size="md" className="mt-2">
            {coverage === 'truncated'
              ? t('sampleTruncated', { shown: group.sample.length, total: group.count })
              : coverage === 'complete'
                ? t('sampleComplete', { count: group.sample.length })
                : t('sampleUnknownCoverage', { count: group.sample.length })}
          </HintText>
          <ul className="mt-2 space-y-1">
            {group.sample.map((r) => (
              <li key={r.deviceToken} className="text-sm">
                <span className="font-mono text-foreground">{r.deviceToken}</span>{' '}
                {/* The reason is client-safe prose from the service, English by policy. */}
                <span className="text-muted-foreground">{r.reason}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {group.sample.length === 0 && group.count != null && (
        <HintText size="md" className="mt-2">
          {t('sampleNone')}
        </HintText>
      )}
    </div>
  );
}

export function RefusalGroupList({ groups }: { groups: RefusalGroup[] }) {
  return (
    <div className="space-y-3">
      {groups.map((g) => (
        <RefusalGroupBlock key={g.code} group={g} />
      ))}
    </div>
  );
}
