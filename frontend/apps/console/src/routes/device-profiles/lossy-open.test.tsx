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
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
import { updateDetectionRule, createDetectionRule } from '@/lib/api/device-management';
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

// 🔴 A TYPE THE FORM DOES NOT MODEL MUST NEVER BECOME A THRESHOLD. parseDefinition used to
// fall back to `threshold` for any `type` it did not know, so opening (or drafting) a rule of a
// later release's kind showed a threshold form pre-filled from it, and Save wrote the threshold
// over the real rule. The form now refuses to render an editor at all.
describe('opening a rule of a type the form does not model', () => {
  const NOT_EDITABLE = /can.t be edited in the form/i;
  const unknown = JSON.stringify({ name: 'A rule', type: 'frobnicate', severity: 'major', window: '5m' });

  it('shows a non-dismissable refusal and no editor, and never offers Save', () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(unknown)} onDone={() => {}} />);

    const alert = screen.getByRole('alert');
    expect(alert.textContent).toMatch(NOT_EDITABLE);
    expect(alert.textContent).toMatch(/frobnicate/);
    expect(screen.queryByRole('button', { name: /dismiss|close/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /save changes/i })).toBeNull();
    // No threshold form pre-filled from it.
    expect(screen.queryByLabelText(/metric/i)).toBeNull();
    expect(screen.queryByText(WARNING)).toBeNull();
    // The stored bytes are shown read-only, verbatim.
    expect(screen.getByLabelText(/stored definition/i).textContent).toContain('frobnicate');
  });

  it('never writes anything for it', async () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(unknown)} onDone={() => {}} />);
    await new Promise((r) => setTimeout(r, 500)); // past the validation debounce
    expect(updateDetectionRule).not.toHaveBeenCalled();
    expect(createDetectionRule).not.toHaveBeenCalled();
  });

  it('refuses a handed-off draft of an unmodelled type too, instead of creating a threshold', () => {
    render(<DetectionRuleForm profileToken="p" initialDefinition={unknown} onDone={() => {}} />);

    expect(screen.getByRole('alert').textContent).toMatch(NOT_EDITABLE);
    expect(screen.queryByRole('button', { name: /create/i })).toBeNull();
  });

  it('treats a definition with no type as unmodelled rather than a threshold', () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(JSON.stringify({ name: 'x' }))} onDone={() => {}} />);

    expect(screen.getByRole('alert').textContent).toMatch(NOT_EDITABLE);
  });

  it('round-trips a connectivity rule exactly through Save', async () => {
    const connectivity = {
      name: 'A rule',
      type: 'connectivity',
      severity: 'critical',
      actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'offline' } }],
    };
    render(<DetectionRuleForm profileToken="p" entity={rule(JSON.stringify(connectivity))} onDone={() => {}} />);

    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    const sent = JSON.parse((vi.mocked(updateDetectionRule).mock.calls[0][1] as { definition: string }).definition);
    expect(sent).toEqual(connectivity);
  });
});
