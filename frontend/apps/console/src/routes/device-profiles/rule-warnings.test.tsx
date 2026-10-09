// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The compiler's advisory warnings must reach the author on the surfaces that compile a rule: the
// form (which is also where a "Describe" draft lands for review) and the canvas. A warning is not
// an error — the rule is accepted — so these tests also pin that it is shown as advice and that a
// clean rule shows none, which is what keeps the advice worth reading.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18n from 'i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listEntityGroups: vi.fn(async () => []),
  listEntityGroupVersions: vi.fn(async () => []),
  createDetectionRule: vi.fn(async () => undefined),
  updateDetectionRule: vi.fn(async () => undefined),
  listMetricDefinitions: vi.fn(async () => []),
}));
vi.mock('@/lib/api/connectors', () => ({ listConnectors: vi.fn(async () => []) }));
vi.mock('@xyflow/react', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ReactFlow: () => null,
  Background: () => null,
  Controls: () => null,
}));
vi.mock('@/lib/api/browse', () => ({ previewSelector: vi.fn(async () => ({ total: 0, sample: [] })) }));
vi.mock('@/auth/AuthProvider', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useAuth: () => ({ tenant: null, identity: null, authorities: [], can: () => true }),
}));
const validateDetectionRule = vi.fn();
const draftDetectionRuleFromText = vi.fn();
vi.mock('@/lib/api/event-processing', () => ({
  validateDetectionRule: (...a: unknown[]) => validateDetectionRule(...a),
  draftDetectionRuleFromText: (...a: unknown[]) => draftDetectionRuleFromText(...a),
  compileCanvas: vi.fn(),
  previewRule: vi.fn(),
}));

import { DetectionRuleAuthoring } from './DetectionRuleAuthoring';
import { DetectionRuleForm } from './DetectionRuleForm';
import { updateDetectionRule } from '@/lib/api/device-management';
import { paintDiagnostics } from './canvas/diagnostics';
import type { DetectionRule } from '@/lib/api/device-management';

afterEach(cleanup);
beforeEach(() => vi.clearAllMocks());

const fallbackRule = JSON.stringify({
  name: 'hot',
  type: 'threshold',
  severity: 'major',
  when: { cel: '!("lim" in attr) && "t" in m && m["t"] > 80.0' },
  actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'hot' } }],
});

// A stored rule; the "Describe" door hands its draft to this same form, so the form is where the
// draft is reviewed and where its warnings must show.
const stored = {
  token: 'hot',
  name: 'hot',
  description: null,
  definition: fallbackRule,
  authoringGraph: null,
  enabled: true,
  metadata: null,
  entityGroupToken: null,
  entityGroupVersion: null,
} as unknown as DetectionRule;

const negated = { code: 'negatedAttributeGuard', params: ['lim'], message: 'english fallback' };

describe('the rule form shows compiler warnings', () => {
  it('shows the localised warning for the rule under review, naming the attribute', async () => {
    validateDetectionRule.mockResolvedValue({ ok: true, message: null, warnings: [negated] });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);

    const list = await screen.findByTestId('rule-warnings', undefined, { timeout: 3000 });
    expect(list.textContent).toContain('“lim”');
    expect(list.textContent).toContain('applies to every device without it');
    // The rule still compiles: a warning sits beside the success line, it does not replace it.
    expect(screen.getByText(/The rule compiles/)).toBeTruthy();
  });

  it('falls back to the server message for a code this build does not know', async () => {
    validateDetectionRule.mockResolvedValue({
      ok: true,
      message: null,
      warnings: [{ code: 'someFutureCode', params: [], message: 'a newer server said this' }],
    });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);

    expect((await screen.findByTestId('rule-warnings', undefined, { timeout: 3000 })).textContent).toBe('a newer server said this');
  });

  it('shows a Describe draft own warnings for review before the author has named the rule', () => {
    // No token yet, so the inline check has not run (it needs one): the draft's warnings, which
    // arrived with it from the compiler, are what the reviewer sees on the first render.
    render(
      <DetectionRuleForm profileToken="p" initialDefinition={fallbackRule} initialWarnings={[negated]} onDone={() => {}} />,
    );

    expect(screen.getByTestId('rule-warnings').textContent).toContain('“lim”');
    expect(validateDetectionRule).not.toHaveBeenCalled();
  });

  it('shows nothing for a rule with no warnings', async () => {
    validateDetectionRule.mockResolvedValue({ ok: true, message: null, warnings: [] });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);

    await screen.findByText(/The rule compiles/, undefined, { timeout: 3000 });
    expect(screen.queryByTestId('rule-warnings')).toBeNull();
  });
});

describe('a warning is advice, never a gate', () => {
  it('leaves Save on, and the rule saves', async () => {
    validateDetectionRule.mockResolvedValue({ ok: true, message: null, warnings: [negated] });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);
    await screen.findByTestId('rule-warnings', undefined, { timeout: 3000 });

    const save = screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement;
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(vi.mocked(updateDetectionRule)).toHaveBeenCalledTimes(1));
  });
});

describe('the Describe door hands its draft and warnings to the form', () => {
  it('reviews the draft with its warnings, through the authoring component', async () => {
    draftDetectionRuleFromText.mockResolvedValue({
      ok: true,
      definition: fallbackRule,
      warnings: [{ field: 'when', code: negated.code, params: negated.params, message: negated.message }],
      diagnostics: [],
      unavailable: false,
    });
    render(<DetectionRuleAuthoring profileToken="p" onDone={() => {}} />);

    fireEvent.click(screen.getByRole('radio', { name: 'Describe' }));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'alarm when hot unless a limit is set' } });
    fireEvent.click(screen.getByRole('button', { name: 'Draft rule' }));

    // The form is showing the draft (its name field is filled), with the warning, and no token has
    // been entered, so the inline check has not run: the warning came with the draft.
    const list = await screen.findByTestId('rule-warnings');
    expect(list.textContent).toContain('“lim”');
    expect((screen.getByPlaceholderText('Overheating') as HTMLInputElement).value).toBe('hot');
    expect(validateDetectionRule).not.toHaveBeenCalled();
  });
});

describe('a rule the form cannot edit', () => {
  it('shows no warnings and does not crash, whether its type is unmodelled or it is unreadable', () => {
    const unmodelled = JSON.stringify({ name: 'x', type: 'frobnicate' });
    for (const def of [unmodelled, '{not json']) {
      const { unmount } = render(
        <DetectionRuleForm profileToken="p" initialDefinition={def} initialWarnings={[negated]} onDone={() => {}} />,
      );
      expect(screen.queryByTestId('rule-warnings')).toBeNull();
      unmount();
    }
    expect(validateDetectionRule).not.toHaveBeenCalled();
  });
});

describe('a handed-off draft warnings do not outlive the draft', () => {
  it('drop once the author changes the rule, and are not brought back', async () => {
    validateDetectionRule.mockRejectedValue(new Error('down'));
    render(
      <DetectionRuleForm profileToken="p" initialDefinition={fallbackRule} initialWarnings={[negated]} onDone={() => {}} />,
    );
    expect(screen.getByTestId('rule-warnings')).toBeTruthy();

    fireEvent.change(screen.getByPlaceholderText('Overheating'), { target: { value: 'renamed' } });
    await waitFor(() => expect(screen.queryByTestId('rule-warnings')).toBeNull());

    // Naming the rule lets the inline check run; it fails (transport), which clears the check state.
    // The draft's warnings must stay gone: they describe a rule that is no longer on screen.
    fireEvent.change(document.getElementById('dr-token') as HTMLElement, { target: { value: 'hot-rule' } });
    await waitFor(() => expect(validateDetectionRule).toHaveBeenCalled(), { timeout: 3000 });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByTestId('rule-warnings')).toBeNull();
  });
});

describe('the canvas keeps a warning apart from an error', () => {
  const t = i18n.t.bind(i18n);

  it('paints a coded warning on its node as a warning, not a diagnostic', () => {
    const painted = paintDiagnostics(
      [{ nodeId: 'c', severity: 'warning', message: 'english fallback', code: negated.code, params: negated.params }],
      t,
    );
    expect(painted.get('c')?.diagnostic).toBeUndefined();
    expect(painted.get('c')?.warning).toContain('“lim”');
  });

  it('paints an uncoded warning (the lowering\'s own) from its message', () => {
    const painted = paintDiagnostics(
      [{ nodeId: 'b', severity: 'warning', message: 'this branch routes nothing', code: null, params: [] }],
      t,
    );
    expect(painted.get('b')).toEqual({ warning: 'this branch routes nothing' });
  });

  it('keeps an error as an error, and ignores a graph-level diagnostic with no node', () => {
    const painted = paintDiagnostics(
      [
        { nodeId: 'c', severity: 'error', message: 'bad', code: null, params: [] },
        { nodeId: null, severity: 'error', message: 'cycle', code: null, params: [] },
      ],
      t,
    );
    expect(painted.get('c')).toEqual({ diagnostic: 'bad' });
    expect(painted.size).toBe(1);
  });
});
