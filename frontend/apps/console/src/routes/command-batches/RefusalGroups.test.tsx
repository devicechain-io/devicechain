// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE DEFECT THIS FILE EXISTS TO PREVENT is a panel that tells the operator how much of
// something it is showing them, when nobody said.
//
// `refusals.test.ts` next door already pins the RULE — the exact count wins over the capped
// sample. What it cannot see is what the panel SAYS, and the panel said the wrong thing in a
// state the rule handled correctly: for a group with no exact count it rendered "Showing all
// 3 refused devices", one line under a badge reading "Devices refused (total not reported)".
// Two adjacent lines, one claiming completeness and the other disclaiming the total it would
// need to make that claim.
//
// The cause is the shape this slice keeps finding: a boolean answering a three-valued
// question. `isSampleTruncated` folds "unknown" in with "not truncated", which is right for
// the narrow question it asks and wrong as a basis for a sentence.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';

import i18n from '@/i18n/config';
import { RefusalGroupBlock, RefusalGroupList } from './RefusalGroups';
import { groupRefusals, sampleCoverage, type RefusalGroup } from './refusals';

afterEach(cleanup);

const entry = (deviceToken: string, code: string) => ({
  deviceToken,
  code,
  reason: `device ${deviceToken} refused`,
});

describe('what the refusals panel claims about its own coverage', () => {
  it('says the names are all of them only when an exact count agrees', () => {
    const group: RefusalGroup = {
      code: 'COMMAND_NOT_IN_VOCABULARY',
      count: 2,
      sample: [entry('a', 'X'), entry('b', 'X')],
    };
    render(<RefusalGroupBlock group={group} />);

    expect(screen.getByText('Showing all 2 refused devices.')).toBeTruthy();
  });

  it('says how many of how many when the sample is capped short', () => {
    const group: RefusalGroup = {
      code: 'DEVICE_NOT_FOUND',
      count: 4312,
      sample: Array.from({ length: 100 }, (_, i) => entry(`dev-${i}`, 'DEVICE_NOT_FOUND')),
    };
    render(<RefusalGroupBlock group={group} />);

    expect(
      screen.getByText('Showing 100 of 4,312 refused devices — the list of names is capped, the total is exact.'),
    ).toBeTruthy();
  });

  // 🔴 THE ONE THAT SHIPPED WRONG. A null count means the server named devices under a code
  // its counts never mentioned. The names are real and must be shown; how many they are OUT
  // OF is unknown, and the panel must not invent an answer.
  it('does not claim completeness when no total was reported', () => {
    const group: RefusalGroup = {
      code: 'MYSTERY_CODE',
      count: null,
      sample: [entry('a', 'MYSTERY_CODE'), entry('b', 'MYSTERY_CODE'), entry('c', 'MYSTERY_CODE')],
    };
    render(<RefusalGroupBlock group={group} />);

    // The badge line is honest already...
    expect(screen.getByText('Devices refused (total not reported)')).toBeTruthy();
    // ...and now the line under it agrees with it instead of contradicting it.
    expect(screen.queryByText(/Showing all/)).toBeNull();
    expect(
      screen.getByText(
        'Naming 3 refused devices. The service did not report a total, so whether these are all of them is not known.',
      ),
    ).toBeTruthy();
  });

  // The device names themselves are the reason the block exists; a coverage sentence that
  // was right while the names went missing would be a worse bug than the one being fixed.
  it('names every device in the sample, whatever the coverage', () => {
    for (const count of [null, 3, 900]) {
      cleanup();
      render(
        <RefusalGroupBlock
          group={{ code: 'C', count, sample: [entry('alpha', 'C'), entry('beta', 'C'), entry('gamma', 'C')] }}
        />,
      );
      for (const token of ['alpha', 'beta', 'gamma']) {
        expect(screen.getByText(token), `count=${count} dropped ${token}`).toBeTruthy();
      }
    }
  });
});

// The rule behind the rendering, enumerated. Three inputs, three answers, and the mapping
// asserted rather than exemplified — the panel above renders whichever this returns.
describe('sampleCoverage', () => {
  it.each([
    [null, 3, 'unknown'],
    [3, 3, 'complete'],
    [2, 3, 'complete'], // a count BELOW the sample is not truncation; it is the opposite race
    [900, 100, 'truncated'],
    [0, 0, 'complete'],
  ] as const)('count=%s sample=%i ⇒ %s', (count, sampleLen, want) => {
    const group: RefusalGroup = {
      code: 'C',
      count,
      sample: Array.from({ length: sampleLen }, (_, i) => entry(`d${i}`, 'C')),
    };
    expect(sampleCoverage(group)).toBe(want);
  });

  // 🔴 The null-count group is not hypothetical: groupRefusals MINTS one whenever the sample
  // carries a code the counts never mentioned. This is the path from a real server response
  // to the state the panel used to lie about.
  it('is reached by a group groupRefusals actually builds', () => {
    const groups = groupRefusals([{ code: 'KNOWN', count: 1 }], [entry('a', 'KNOWN'), entry('b', 'UNMENTIONED')]);
    const mystery = groups.find((g) => g.code === 'UNMENTIONED');
    expect(mystery).toBeDefined();
    expect(mystery!.count).toBeNull();
    expect(sampleCoverage(mystery!)).toBe('unknown');
  });
});


// ── The list, and the other locale ──────────────────────────────────────────

describe('RefusalGroupList', () => {
  // 🔴 EVERY TEST ABOVE RENDERS ONE BLOCK, AND BOTH SCREENS RENDER THE LIST. Narrowing the
  // list to `groups.slice(0, 1)` makes every refusal code but the first vanish from the batch
  // detail page AND the create form — and left the whole command-batches suite green, because
  // nothing rendered more than one group.
  it('renders every group it is given', () => {
    render(
      <RefusalGroupList
        groups={[
          { code: 'DEVICE_NOT_FOUND', count: 9, sample: [entry('a', 'DEVICE_NOT_FOUND')] },
          { code: 'COMMAND_NOT_IN_VOCABULARY', count: 4, sample: [entry('b', 'COMMAND_NOT_IN_VOCABULARY')] },
          { code: 'DEVICE_DISABLED', count: 1, sample: [entry('c', 'DEVICE_DISABLED')] },
        ]}
      />,
    );
    for (const code of ['DEVICE_NOT_FOUND', 'COMMAND_NOT_IN_VOCABULARY', 'DEVICE_DISABLED']) {
      expect(screen.getByText(code), `${code} was not rendered`).toBeTruthy();
    }
    expect(screen.getByText('9 devices refused')).toBeTruthy();
    expect(screen.getByText('1 device refused')).toBeTruthy();
  });
});

// 🔴 THE ENGLISH COPY IS NOT THE PRODUCT. Setting the Spanish `sampleUnknownCoverage_other`
// back to a completeness claim — "Mostrando los N dispositivos rechazados" — reintroduces the
// exact defect this file was written for, and every assertion above stays green, because they
// all read English. A locale is a place the fix can be undone.
describe('the unknown-coverage sentence in Spanish', () => {
  beforeAll(async () => {
    await i18n.changeLanguage('es');
  });
  afterAll(async () => {
    await i18n.changeLanguage('en');
  });

  // Read the WHOLE block rather than one node: "the screen does not contradict itself" is a
  // property of everything rendered together, and the defect was two adjacent lines
  // disagreeing. A single-element query cannot express that.
  it('does not claim completeness when no total was reported', () => {
    const { container } = render(
      <RefusalGroupBlock
        group={{ code: 'MYSTERY', count: null, sample: [entry('a', 'MYSTERY'), entry('b', 'MYSTERY')] }}
      />,
    );
    const text = container.textContent ?? '';

    expect(text).toMatch(/total no informado/); // the badge is honest
    expect(text).toMatch(/no se sabe/); // and so is the line under it
    // The claim that must not be made anywhere in the block: "these are all of them".
    expect(text).not.toMatch(/Mostrando los|Mostrando todos|\btodos los\b/);
  });

  it('still says how many of how many when the total IS known — the counterweight', () => {
    const { container } = render(
      <RefusalGroupBlock group={{ code: 'K', count: 900, sample: [entry('a', 'K'), entry('b', 'K')] }} />,
    );
    const text = container.textContent ?? '';

    // A locale that said "no se sabe" for every group would pass the test above and tell an
    // operator nothing is knowable when the exact total was right there.
    expect(text).toMatch(/900/);
    expect(text).not.toMatch(/no se sabe/);
  });
});
