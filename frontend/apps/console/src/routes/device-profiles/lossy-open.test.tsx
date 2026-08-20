// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THIS FILE EXISTS BECAUSE A MUTATION SURVIVED. Setting `lossyOpen = false` outright — the
// whole point of the round-trip guard, switched off — broke nothing. `ruleSurvivesRoundTrip`
// was well tested as a function and completely untested as a BEHAVIOUR: nothing checked that
// the form actually shows the warning, so the guard could have been wired to a constant and
// every gate would have stayed green.
//
// That is the same shape as the defect this slice is about. A rule with no enforcer is a
// comment; an enforcer nothing exercises is a comment with a function signature.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The form talks to three areas on mount. None of them is what this file measures, and a real
// call would make the test a network test, so they are stubbed to the shape of "nothing found".
vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listEntityGroups: vi.fn(async () => []),
  listEntityGroupVersions: vi.fn(async () => []),
  createDetectionRule: vi.fn(async () => undefined),
  updateDetectionRule: vi.fn(async () => undefined),
}));
vi.mock('@/lib/api/browse', () => ({ previewSelector: vi.fn(async () => ({ total: 0, sample: [] })) }));
// The CREATE path renders the token field, which reads the session to suggest a token prefix.
// Stubbed rather than wrapped in a provider: this file measures one warning, and a real auth
// context would make it depend on session shape it has no opinion about.
vi.mock('@/auth/AuthProvider', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useAuth: () => ({ tenant: null, identity: null, authorities: [], can: () => true }),
}));
vi.mock('@/lib/api/event-processing', () => ({ validateDetectionRule: vi.fn(async () => ({ errors: [] })) }));

import { DetectionRuleForm } from './DetectionRuleForm';
import type { DetectionRule } from '@/lib/api/device-management';

afterEach(cleanup);
beforeEach(() => vi.clearAllMocks());

const rule = (definition: string): DetectionRule =>
  ({
    token: 'r1',
    name: 'A rule',
    description: null,
    definition,
    authoringGraph: null,
    enabled: true,
    metadata: null,
    entityGroupToken: null,
    entityGroupVersion: null,
  }) as unknown as DetectionRule;

const WARNING = /This form cannot express everything this rule/;
const UNREADABLE = /could not be read into the form/;

const threshold = JSON.stringify({
  name: 'A rule',
  type: 'threshold',
  when: { metric: 'tempC', op: 'gt', threshold: 30 },
});

describe('opening a stored rule the form cannot fully hold', () => {
  it('says so when the definition carries a field the form does not model', () => {
    const exotic = JSON.stringify({ ...JSON.parse(threshold), futureKnob: 'a field from a later release' });
    render(<DetectionRuleForm profileToken="p" entity={rule(exotic)} onDone={() => {}} />);

    expect(screen.getByText(WARNING)).toBeTruthy();
  });

  // 🔴 THE COUNTERWEIGHT, AND IT IS THE HALF THAT MATTERS MOST. A warning that fired on every
  // open would satisfy the test above and teach operators to ignore it — which is worse than
  // no warning, because it also buries the real one.
  it('stays quiet on a rule it can hold completely', () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(threshold)} onDone={() => {}} />);

    expect(screen.queryByText(WARNING)).toBeNull();
    expect(screen.queryByText(UNREADABLE)).toBeNull();
  });

  // The kind that was unauthorable for a whole release. It must now open cleanly, with no
  // warning at all — the fix is that the form UNDERSTANDS it, not that it apologises for it.
  it('opens a connectivity rule without complaint', () => {
    const connectivity = JSON.stringify({
      name: 'A rule',
      type: 'connectivity',
      severity: 'critical',
      actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'offline' } }],
    });
    render(<DetectionRuleForm profileToken="p" entity={rule(connectivity)} onDone={() => {}} />);

    expect(screen.queryByText(WARNING)).toBeNull();
    expect(screen.queryByText(UNREADABLE)).toBeNull();
  });

  it('reports an unreadable definition differently from a lossy one', () => {
    render(<DetectionRuleForm profileToken="p" entity={rule('{not json at all')} onDone={() => {}} />);

    expect(screen.getByText(UNREADABLE)).toBeTruthy();
    // The two sentences describe different situations and must not both appear: an unparseable
    // rule opens BLANK, a lossy one opens with everything the form did understand.
    expect(screen.queryByText(WARNING)).toBeNull();
  });

  // 🔴 THE PATH THAT HAD NO WARNING AT ALL. The "Describe" door hands the form a definition a
  // model wrote, and the guards used to be gated on `editing` — so the surface most likely to
  // produce a shape the form cannot hold was the one surface that said nothing.
  it('warns about a handed-off draft too, not only a stored rule', () => {
    const exotic = JSON.stringify({ ...JSON.parse(threshold), futureKnob: 'from the NL door' });
    render(<DetectionRuleForm profileToken="p" initialDefinition={exotic} onDone={() => {}} />);

    expect(screen.getByText(WARNING)).toBeTruthy();
  });

  it('says nothing when creating a rule from scratch', () => {
    // No stored rule and no draft: there is nothing to have lost, and a warning here would be
    // pure noise on the most common path in the drawer.
    render(<DetectionRuleForm profileToken="p" onDone={() => {}} />);

    expect(screen.queryByText(WARNING)).toBeNull();
    expect(screen.queryByText(UNREADABLE)).toBeNull();
  });
});
