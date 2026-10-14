// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The reviewed alert presets as an operator meets them: the picker above the new-rule form fills the
// form with the documented values, the severity-coverage panel says whether the rule's severity
// reaches anyone and can create the policy that makes it so, and the form previews the rule against
// history with the same query (and the same result rendering) the canvas uses.

import '@/i18n/config';
import { cleanup, fireEvent, render as rtlRender, screen, waitFor } from '@testing-library/react';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const render = (ui: Parameters<typeof rtlRender>[0]) => rtlRender(ui, { wrapper: ConfirmProvider });

const authState = vi.hoisted(() => ({ authorities: ['notification:write'] as string[] }));

vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listEntityGroups: vi.fn(async () => []),
  listEntityGroupVersions: vi.fn(async () => []),
  createDetectionRule: vi.fn(async () => undefined),
  updateDetectionRule: vi.fn(async () => undefined),
  listMetricDefinitions: vi.fn(async () => [{ metricKey: 'temperature' }, { metricKey: 'battery_level' }]),
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
  useAuth: () => ({ tenant: null, identity: null, authorities: [], can: () => true, claims: { authorities: authState.authorities } }),
}));
vi.mock('@/lib/api/event-processing', () => ({
  validateDetectionRule: vi.fn(async () => ({ ok: true, message: null, warnings: [] })),
  draftDetectionRuleFromText: vi.fn(),
  compileCanvas: vi.fn(),
  previewRule: vi.fn(),
}));
vi.mock('@/lib/api/notifications', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listNotificationPolicies: vi.fn(async () => []),
  listNotificationChannels: vi.fn(async () => []),
  createSeverityPolicy: vi.fn(async () => undefined),
}));

import { DetectionRuleAuthoring } from './DetectionRuleAuthoring';
import { DetectionRuleForm } from './DetectionRuleForm';
import { validateDetectionRule, previewRule } from '@/lib/api/event-processing';
import { createDetectionRule, listMetricDefinitions } from '@/lib/api/device-management';
import { createSeverityPolicy, listNotificationChannels, listNotificationPolicies } from '@/lib/api/notifications';
import type { DetectionRule } from '@/lib/api/device-management';

afterEach(cleanup);
beforeEach(() => {
  vi.clearAllMocks();
  authState.authorities = ['notification:write'];
  vi.mocked(listNotificationPolicies).mockResolvedValue([]);
  vi.mocked(listNotificationChannels).mockResolvedValue([]);
});

const input = (id: string) => document.getElementById(id) as HTMLInputElement;

// Open the new-rule surface and wait for the profile's metrics to have loaded, which a preset reads.
async function openNew() {
  render(<DetectionRuleAuthoring profileToken="p" onDone={() => {}} />);
  await waitFor(() => expect(listMetricDefinitions).toHaveBeenCalled());
  // Let the resolved metrics land in state before a preset is picked.
  await new Promise((r) => setTimeout(r, 0));
}

describe('the preset picker', () => {
  it('Low battery fills the form with the documented values, the battery metric picked by name', async () => {
    await openNew();
    fireEvent.click(await screen.findByRole('button', { name: 'Low battery' }));

    await waitFor(() => expect(input('dr-cond-metric').value).toBe('battery_level'));
    expect(input('dr-cond-threshold').value).toBe('20');
    expect(input('dr-hold').value).toBe('10m');
    expect(screen.getByTestId('preset-help').textContent).toMatch(/Check the metric/);

    // And what the form would save is the preset's rule: the compile check receives it verbatim.
    fireEvent.change(input('dr-token'), { target: { value: 'low-battery' } });
    await waitFor(() => expect(validateDetectionRule).toHaveBeenCalled(), { timeout: 3000 });
    const calls = vi.mocked(validateDetectionRule).mock.calls;
    const sent = JSON.parse(calls[calls.length - 1][1] as string);
    expect(sent).toMatchObject({
      type: 'duration',
      severity: 'warning',
      hold: '10m',
      when: { metric: 'battery_level', op: 'lt', threshold: 20 },
      actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'low-battery' } }],
    });
  });

  it('Device silent is an absence rule: silence window 15m, no condition editor', async () => {
    await openNew();
    fireEvent.click(await screen.findByRole('button', { name: 'Device silent' }));

    await waitFor(() => expect(input('dr-timeout').value).toBe('15m'));
    expect(document.getElementById('dr-cond-metric')).toBeNull();
  });

  it('Held threshold leaves the limit and the metric for the author and holds 10m', async () => {
    await openNew();
    fireEvent.click(await screen.findByRole('button', { name: 'Above threshold for a duration' }));

    await waitFor(() => expect(input('dr-hold').value).toBe('10m'));
    // Never a plausible 0; two metrics are declared and none is chosen for the author.
    expect(input('dr-cond-threshold').value).toBe('');
    expect(input('dr-cond-metric').value).toBe('');
  });

  it('is not offered when editing an existing rule', () => {
    const entity = {
      token: 'r',
      name: 'r',
      description: null,
      definition: JSON.stringify({ name: 'r', type: 'absence', timeout: '5m' }),
      authoringGraph: null,
      enabled: true,
      metadata: null,
      entityGroupToken: null,
      entityGroupVersion: null,
    } as unknown as DetectionRule;
    render(<DetectionRuleAuthoring profileToken="p" entity={entity} onDone={() => {}} />);
    expect(screen.queryByText('Start from a preset')).toBeNull();
  });
});

describe('severity coverage', () => {
  const silent = async () => {
    await openNew();
    fireEvent.click(await screen.findByRole('button', { name: 'Device silent' }));
  };

  it('says "not covered" when no policy routes the severity', async () => {
    await silent();
    expect((await screen.findByTestId('coverage-uncovered')).textContent).toMatch(/Not covered.*major/);
    expect(screen.queryByTestId('coverage-covered')).toBeNull();
  });

  it('says "covered" when an enabled policy routes it through a working channel', async () => {
    vi.mocked(listNotificationPolicies).mockResolvedValue([
      { token: 'p1', name: null, enabled: true, rules: [{ severity: 'MAJOR', channel: { token: 'c', enabled: true, channelType: 'webhook' }, recipients: null }], deviceTypeToken: null },
    ]);
    await silent();
    expect((await screen.findByTestId('coverage-covered')).textContent).toMatch(/Covered/);
    expect(screen.queryByTestId('coverage-uncovered')).toBeNull();
  });

  it('creates a policy for exactly that severity, then re-checks', async () => {
    vi.mocked(listNotificationChannels).mockResolvedValue([{ token: 'ops-hook', name: 'Ops', channelType: 'webhook', enabled: true }]);
    await silent();
    fireEvent.click(await screen.findByRole('button', { name: /Create a policy for major/ }));

    // A single channel is pre-selected; a webhook needs no recipients.
    await waitFor(() => expect((screen.getByRole('button', { name: /Create major policy/ }) as HTMLButtonElement).disabled).toBe(false));
    // The re-check after the create sees the new policy.
    vi.mocked(listNotificationPolicies).mockResolvedValue([
      { token: 'n', name: null, enabled: true, rules: [{ severity: 'MAJOR', channel: { token: 'ops-hook', enabled: true, channelType: 'webhook' }, recipients: null }], deviceTypeToken: null },
    ]);
    fireEvent.click(screen.getByRole('button', { name: /Create major policy/ }));

    await waitFor(() => expect(createSeverityPolicy).toHaveBeenCalledTimes(1));
    expect(vi.mocked(createSeverityPolicy).mock.calls[0][0]).toMatchObject({
      severity: 'major',
      channelToken: 'ops-hook',
      recipients: undefined,
    });
    expect(await screen.findByTestId('coverage-covered')).toBeTruthy();
  });

  it('hides the create action without notification:write but still reports the gap', async () => {
    authState.authorities = ['alarm:write'];
    await silent();
    expect(await screen.findByTestId('coverage-uncovered')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Create a policy for/ })).toBeNull();
  });

  it('asks for recipients on an email channel', async () => {
    vi.mocked(listNotificationChannels).mockResolvedValue([{ token: 'mail', name: 'Mail', channelType: 'smtp', enabled: true }]);
    await silent();
    fireEvent.click(await screen.findByRole('button', { name: /Create a policy for major/ }));
    await waitFor(() => expect(input('dr-coverage-recipients')).toBeTruthy());
    expect((screen.getByRole('button', { name: /Create major policy/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(input('dr-coverage-recipients'), { target: { value: 'a@x.io, b@x.io' } });
    await waitFor(() => expect((screen.getByRole('button', { name: /Create major policy/ }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole('button', { name: /Create major policy/ }));
    await waitFor(() => expect(createSeverityPolicy).toHaveBeenCalled());
    expect(vi.mocked(createSeverityPolicy).mock.calls[0][0].recipients).toEqual(['a@x.io', 'b@x.io']);
  });

  it('points at channel creation when the tenant has none', async () => {
    await silent();
    fireEvent.click(await screen.findByRole('button', { name: /Create a policy for major/ }));
    expect(await screen.findByTestId('coverage-no-channels')).toBeTruthy();
  });

  it('does not claim either way when the policies cannot be read', async () => {
    vi.mocked(listNotificationPolicies).mockRejectedValue(new Error('forbidden'));
    await silent();
    expect(await screen.findByText(/Could not check notification policies/)).toBeTruthy();
    expect(screen.queryByTestId('coverage-covered')).toBeNull();
    expect(screen.queryByTestId('coverage-uncovered')).toBeNull();
  });
});

describe('the preview in the form', () => {
  const definition = JSON.stringify({
    name: 'quiet',
    type: 'absence',
    severity: 'major',
    timeout: '15m',
    actions: [{ type: 'raiseAlarm', raiseAlarm: { alarmKey: 'device-silent' } }],
  });
  const stored = {
    token: 'quiet',
    name: 'quiet',
    description: null,
    definition,
    authoringGraph: null,
    enabled: true,
    metadata: null,
    entityGroupToken: null,
    entityGroupVersion: null,
  } as unknown as DetectionRule;

  const run = async () => {
    const button = await screen.findByRole('button', { name: 'Run preview' });
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false), { timeout: 3000 });
    fireEvent.click(button);
  };

  it('replays the rule as a definition (no graph) and shows both the raise and the resolve edge', async () => {
    vi.mocked(previewRule).mockResolvedValue({
      ok: true,
      degraded: null,
      diagnostics: [],
      stats: { eventsScanned: 42, firingCount: 2, evalErrors: 0, wallMs: 7 },
      firings: [
        { occurredAt: '2026-10-13T10:00:00Z', series: 'dev-1', signal: 'raised', trace: [] },
        { occurredAt: '2026-10-13T10:20:00Z', series: 'dev-1', signal: 'resolved', trace: [] },
      ],
    });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);
    await run();

    await waitFor(() => expect(previewRule).toHaveBeenCalledTimes(1));
    const arg = vi.mocked(previewRule).mock.calls[0][0];
    expect(arg.ruleDefinition).toBe(definition);
    expect(arg.graph).toBeUndefined();
    expect(arg.profileToken).toBe('p');
    expect(await screen.findByText('RAISE')).toBeTruthy();
    expect(screen.getByText('RESOLVE')).toBeTruthy();
  });

  it('renders a failed run and a rejected draft the way the canvas does', async () => {
    vi.mocked(previewRule).mockRejectedValueOnce(new Error('replay unavailable'));
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);
    await run();
    expect(await screen.findByText(/replay unavailable/)).toBeTruthy();

    vi.mocked(previewRule).mockResolvedValueOnce({
      ok: false,
      degraded: null,
      firings: [],
      stats: { eventsScanned: 0, firingCount: 0, evalErrors: 0, wallMs: 0 },
      diagnostics: [{ nodeId: null, severity: 'error', message: 'timeout is too long', code: null, params: [] }],
    });
    fireEvent.click(screen.getByRole('button', { name: 'Run preview' }));
    expect(await screen.findByText('timeout is too long')).toBeTruthy();
  });

  it('is not runnable while the compiler rejects the rule', async () => {
    vi.mocked(validateDetectionRule).mockResolvedValueOnce({ ok: false, message: 'nope', warnings: [] });
    render(<DetectionRuleForm profileToken="p" entity={stored} onDone={() => {}} />);
    const button = (await screen.findByRole('button', { name: 'Run preview' })) as HTMLButtonElement;
    await waitFor(() => expect(button.title).toMatch(/compile errors/i), { timeout: 3000 });
    expect(button.disabled).toBe(true);
  });
});

describe('creating from a preset saves a draft rule', () => {
  it('creates the rule the preset described, once named, with no group scope', async () => {
    await openNew();
    fireEvent.click(await screen.findByRole('button', { name: 'Device silent' }));
    await waitFor(() => expect(input('dr-timeout').value).toBe('15m'));
    fireEvent.change(input('dr-token'), { target: { value: 'quiet' } });
    const create = screen.getByRole('button', { name: 'Create detection rule' }) as HTMLButtonElement;
    await waitFor(() => expect(create.disabled).toBe(false));
    fireEvent.click(create);
    await waitFor(() => expect(createDetectionRule).toHaveBeenCalledTimes(1));
    const sent = vi.mocked(createDetectionRule).mock.calls[0][0] as { definition: string; entityGroupToken: string | null };
    expect(JSON.parse(sent.definition)).toMatchObject({ type: 'absence', timeout: '15m', severity: 'major' });
    expect(sent.entityGroupToken).toBeNull();
  });
});
