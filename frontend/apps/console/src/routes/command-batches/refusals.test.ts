// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE DEFECT THIS FILE EXISTS TO PREVENT is a panel that reports the SAMPLE SIZE as
// the number of refused devices. The sample is capped at 100 per code; the counts are
// exact. A batch that refused 4,312 devices renders 100 names, and reading "100" off
// them tells the operator that 4,212 devices they were never shown are fine.

import { describe, expect, it } from 'vitest';

import { groupRefusals, sampleCoverage, totalRefused } from './refusals';

const entry = (deviceToken: string, code: string) => ({
  deviceToken,
  code,
  reason: `device ${deviceToken} refused`,
});

describe('groupRefusals', () => {
  // 🔴🔴 THE ONE THAT MATTERS. The count and the sample disagree by design; the count wins.
  it('keeps the exact count when the sample is capped short of it', () => {
    const sample = Array.from({ length: 100 }, (_, i) => entry(`dev-${i}`, 'DEVICE_NOT_FOUND'));
    const groups = groupRefusals([{ code: 'DEVICE_NOT_FOUND', count: 4312 }], sample);

    expect(groups).toHaveLength(1);
    // The authority is the count — never sample.length, which is what a naive panel shows.
    expect(groups[0].count).toBe(4312);
    expect(groups[0].sample).toHaveLength(100);
    expect(sampleCoverage(groups[0])).toBe('truncated');
  });

  // The counterweight: a small batch's sample IS the whole set, and calling it truncated
  // would be a second, opposite lie. A rule that always claimed truncation would pass the
  // test above and fail here.
  it('does not claim truncation when the sample is complete', () => {
    const groups = groupRefusals(
      [{ code: 'COMMAND_NOT_IN_VOCABULARY', count: 2 }],
      [entry('a', 'COMMAND_NOT_IN_VOCABULARY'), entry('b', 'COMMAND_NOT_IN_VOCABULARY')],
    );

    expect(groups[0].count).toBe(2);
    expect(sampleCoverage(groups[0])).toBe('complete');
  });

  it('groups each code separately and attaches only its own devices', () => {
    const groups = groupRefusals(
      [
        { code: 'HELD_CEILING_EXCEEDED', count: 3 },
        { code: 'DEVICE_NOT_FOUND', count: 7 },
      ],
      [entry('a', 'DEVICE_NOT_FOUND'), entry('b', 'HELD_CEILING_EXCEEDED')],
    );

    // Largest count first: the refusal most worth acting on leads.
    expect(groups.map((g) => g.code)).toEqual(['DEVICE_NOT_FOUND', 'HELD_CEILING_EXCEEDED']);
    expect(groups[0].sample.map((s) => s.deviceToken)).toEqual(['a']);
    expect(groups[1].sample.map((s) => s.deviceToken)).toEqual(['b']);
  });

  // A code the server counted but sampled none of is still a real refusal — it must be
  // shown with its total rather than dropped for having no names to print.
  it('keeps a counted code that the sample never mentions', () => {
    const groups = groupRefusals([{ code: 'HELD_CEILING_EXCEEDED', count: 900 }], []);

    expect(groups).toHaveLength(1);
    expect(groups[0].count).toBe(900);
    expect(groups[0].sample).toEqual([]);
    // No total is being withheld — the panel must not claim "showing 0 of 900 names"
    // as though a sample had been cut; it simply has none.
    expect(sampleCoverage(groups[0])).toBe('truncated');
  });

  // The inconsistent-server case. Inventing an exact total from a capped sample is the
  // very error this module exists to prevent, so the total stays UNKNOWN — null, which
  // is emphatically not zero.
  it('reports an unknown total for a sampled code the counts never declared', () => {
    const groups = groupRefusals([{ code: 'DEVICE_NOT_FOUND', count: 5 }], [entry('x', 'MYSTERY')]);

    const mystery = groups.find((g) => g.code === 'MYSTERY');
    expect(mystery).toBeDefined();
    expect(mystery!.count).toBeNull();
    expect(mystery!.sample).toHaveLength(1);
    // 🔴 THIS ASSERTION USED TO READ `isSampleTruncated(mystery) === false`, which is the
    // defect written down as an expectation: a group with no total is not "not truncated",
    // it is UNKNOWN, and the panel turned that false into "Showing all N refused devices".
    expect(sampleCoverage(mystery!)).toBe('unknown');
    // …and it sorts last: an anomaly is not a headline.
    expect(groups[groups.length - 1].code).toBe('MYSTERY');
  });

  it('has nothing to show for a batch that refused nobody', () => {
    expect(groupRefusals([], [])).toEqual([]);
  });
});

describe('totalRefused', () => {
  it('sums the exact per-code counts, not the sample', () => {
    expect(
      totalRefused([
        { code: 'DEVICE_NOT_FOUND', count: 4312 },
        { code: 'HELD_CEILING_EXCEEDED', count: 88 },
      ]),
    ).toBe(4400);
    expect(totalRefused([])).toBe(0);
  });
});
