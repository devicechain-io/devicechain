// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The webhook (httpCall) and connector (publish) REACT actions are authored in the rule FORM as
// well as on the canvas. These tests pin three things: the form edits and saves the same wire
// fields the canvas does, a rule authored on either surface opens identically on the other, and the
// obvious mistakes (no URL, a non-http URL, no connector, a timeout over the ceiling, a header
// line with no key) are stopped at the form with a plain sentence rather than at publish.

import '@/i18n/config';
import { cleanup, fireEvent, render as rtlRender, screen, waitFor } from '@testing-library/react';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

// The form confirms a lossy save through the app-level confirm dialog, so it renders inside one.
const render = (ui: Parameters<typeof rtlRender>[0]) => rtlRender(ui, { wrapper: ConfirmProvider });
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TFunction } from 'i18next';

vi.mock('@/lib/api/device-management', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  listEntityGroups: vi.fn(async () => []),
  listEntityGroupVersions: vi.fn(async () => []),
  createDetectionRule: vi.fn(async () => undefined),
  updateDetectionRule: vi.fn(async () => undefined),
}));
vi.mock('@/lib/api/browse', () => ({ previewSelector: vi.fn(async () => ({ total: 0, sample: [] })) }));
vi.mock('@/auth/AuthProvider', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useAuth: () => ({ tenant: null, identity: null, authorities: [], can: () => true }),
}));
vi.mock('@/lib/api/event-processing', () => ({ validateDetectionRule: vi.fn(async () => ({ ok: true, warnings: [] })) }));
// The caller's connectors: whatever this returns is all the picker may offer.
vi.mock('@/lib/api/connectors', () => ({
  listConnectors: vi.fn(async () => ({ results: [{ token: 'pager', name: 'Pager' }], pagination: {} })),
}));

import { DetectionRuleForm, outboundHint } from './DetectionRuleForm';
import { updateDetectionRule } from '@/lib/api/device-management';
import { listConnectors } from '@/lib/api/connectors';
import type { DetectionRule } from '@/lib/api/device-management';
import { buildDefinition, parseDefinition, rebuildFrom, type ActionRow, type BuildArgs } from './rule-model';
import { ruleSurvivesRoundTrip, sameLogicalRule } from './rule-equal';
import { graphFromDefinition } from './canvas/roundtrip';
import { selectOption } from '@/test/select';
import GOLDEN from '../../../../../../backend/services/event-processing/internal/rules/graph/testdata/synthesis/guarded-and-outbound.json';

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

const base = { name: 'A rule', type: 'threshold', when: { metric: 'tempC', op: 'gt', threshold: 30 } };
const withActions = (actions: unknown[]) => JSON.stringify({ ...base, actions });

const webhook = {
  type: 'httpCall',
  httpCall: {
    url: 'https://hooks.example.com/alarm',
    headers: { 'X-Source': 'devicechain' },
    bodyTemplate: '"hot " + string(series)',
    secretRef: 'team/pager-token',
    timeoutMs: 5000,
  },
};
const publish = { type: 'publish', publish: { connectorRef: 'pager', payloadTemplate: '"hot"', timeoutMs: 1500 } };

const saved = () => JSON.parse((vi.mocked(updateDetectionRule).mock.calls[0][1] as { definition: string }).definition);

describe('editing the outbound actions in the form', () => {
  it('shows every webhook field and saves an edit to each of them', async () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([webhook]))} onDone={() => {}} />);

    expect((screen.getByLabelText('URL') as HTMLInputElement).value).toBe('https://hooks.example.com/alarm');
    expect((screen.getByLabelText('Headers') as HTMLTextAreaElement).value).toBe('X-Source: devicechain');
    expect((screen.getByLabelText('Secret handle') as HTMLInputElement).value).toBe('team/pager-token');
    expect((screen.getByLabelText('Timeout (ms)') as HTMLInputElement).value).toBe('5000');

    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'https://hooks.example.com/v2' } });
    fireEvent.change(screen.getByLabelText('Headers'), { target: { value: 'X-Source: devicechain\nX-Team: ops' } });
    fireEvent.change(screen.getByLabelText('Timeout (ms)'), { target: { value: '8000' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions).toEqual([
      {
        type: 'httpCall',
        httpCall: {
          ...webhook.httpCall,
          url: 'https://hooks.example.com/v2',
          headers: { 'X-Source': 'devicechain', 'X-Team': 'ops' },
          timeoutMs: 8000,
        },
      },
    ]);
  });

  it('shows the publish connector from the caller’s own list, and saves an edit to the payload', async () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([publish]))} onDone={() => {}} />);

    await waitFor(() => expect(listConnectors).toHaveBeenCalled());
    // The trigger shows the connector by its display name once the list has loaded.
    expect(await screen.findByText('Pager (pager)')).toBeTruthy();
    expect(screen.queryByText(/not found/)).toBeNull();

    fireEvent.change(screen.getByLabelText('Payload (CEL)'), { target: { value: '"hot " + string(series)' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions).toEqual([
      { type: 'publish', publish: { connectorRef: 'pager', payloadTemplate: '"hot " + string(series)', timeoutMs: 1500 } },
    ]);
  });

  it('flags a connector the caller cannot see as not found, rather than dropping it', async () => {
    const ghost = { type: 'publish', publish: { connectorRef: 'someone-elses' } };
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([ghost]))} onDone={() => {}} />);

    expect(await screen.findByText('someone-elses (not found)')).toBeTruthy();
  });

  it('keeps a canvas guard on an outbound action across a save', async () => {
    const guarded = { ...webhook, guard: 'value > 100.0' };
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([guarded]))} onDone={() => {}} />);

    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'https://hooks.example.com/g' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions[0].guard).toBe('value > 100.0');
  });

  it('keeps a canvas guard on a publish action across a save', async () => {
    const guarded = { ...publish, guard: 'value < 0.0' };
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([guarded]))} onDone={() => {}} />);

    fireEvent.change(screen.getByLabelText('Payload (CEL)'), { target: { value: '"cold"' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions[0]).toEqual({ ...publish, publish: { ...publish.publish, payloadTemplate: '"cold"' }, guard: 'value < 0.0' });
  });

  // Every test above edits an action that was ALREADY outbound. This one authors them from
  // scratch, which is the feature: the picker must offer both kinds and each must save.
  it('authors a new publish action and a new webhook action from the action picker', async () => {
    const cmd = { type: 'sendCommand', sendCommand: { command: 'reset' } };
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([cmd, cmd]))} onDone={() => {}} />);

    fireEvent.click(screen.getByRole('button', { name: 'Action 1' }));
    fireEvent.click(screen.getByRole('button', { name: /Publish to a connector/ }));
    await selectOption(screen.getByLabelText('Connector'), /Pager \(pager\)/);

    fireEvent.click(screen.getByRole('button', { name: 'Action 2' }));
    fireEvent.click(screen.getByRole('button', { name: /Call a webhook/ }));
    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'https://hooks.example.com/new' } });

    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions).toEqual([
      { type: 'publish', publish: { connectorRef: 'pager' } },
      { type: 'httpCall', httpCall: { url: 'https://hooks.example.com/new' } },
    ]);
  });

  // When the caller's connector list cannot be read, a stored ref is neither labelled "not
  // found" (nobody knows that) nor cleared — it is shown as itself and saved as itself.
  it('keeps a stored connector when the connector list fails to load', async () => {
    vi.mocked(listConnectors).mockRejectedValueOnce(new Error('forbidden'));
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([publish]))} onDone={() => {}} />);

    expect(await screen.findByText(/connector list could not be loaded/)).toBeTruthy();
    expect(screen.queryByText(/not found/)).toBeNull();
    fireEvent.change(screen.getByLabelText('Payload (CEL)'), { target: { value: '"x"' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(saved().actions[0].publish.connectorRef).toBe('pager');
  });

  // A canvas-authored rule carries its graph sidecar. An incidental save keeps it; an edit to
  // an outbound action changes the rule, so the stale sidecar must be cleared, not kept.
  it('keeps the canvas graph across an unchanged save and drops it when an action changes', async () => {
    const graph = '{"nodes":[],"edges":[]}';
    const canvasRule = { ...rule(withActions([webhook])), authoringGraph: graph } as unknown as DetectionRule;
    const graphSent = (n: number) => (vi.mocked(updateDetectionRule).mock.calls[n][1] as { authoringGraph: unknown }).authoringGraph;

    const first = render(<DetectionRuleForm profileToken="p" entity={canvasRule} onDone={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(1));
    expect(graphSent(0)).toBe(graph);
    first.unmount();

    render(<DetectionRuleForm profileToken="p" entity={canvasRule} onDone={() => {}} />);
    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'https://hooks.example.com/moved' } });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => expect(updateDetectionRule).toHaveBeenCalledTimes(2));
    expect(graphSent(1)).toBeNull();
  });

  it('says nothing is lost when it opens a stored webhook or publish action', () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([webhook, publish]))} onDone={() => {}} />);
    expect(screen.queryByText(/cannot express everything/)).toBeNull();
  });
});

describe('the outbound hints', () => {
  const t = ((k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k)) as unknown as TFunction;
  const row = (patch: Partial<ActionRow>): ActionRow => ({ type: 'httpCall', alarmKey: '', command: '', payload: '', ...patch });

  it.each([
    ['no url', row({ url: '' }), 'ruleHintHttpUrlRequired'],
    ['a non-http url', row({ url: 'ftp://x.example.com' }), 'ruleHintHttpUrlScheme'],
    ['a space in the host', row({ url: 'https://exa mple.com/' }), 'ruleHintHttpUrlScheme'],
    ['no host at all', row({ url: 'http:///hook' }), 'ruleHintHttpUrlScheme'],
    ['a password in the url', row({ url: 'https://user:hunter2@x.example.com/' }), 'ruleHintHttpUrlCredentials'],
    ['a header with an empty key', row({ url: 'https://x.example.com', headers: ': value' }), 'ruleHintHeaderMalformed'],
    ['a negative timeout', row({ url: 'https://x.example.com', timeoutMs: '-1' }), 'ruleHintTimeoutRange'],
    ['a handle one byte over the cap', row({ url: 'https://x.example.com', secretRef: 'a'.repeat(257) }), 'ruleHintSecretHandleInvalid'],
    ['a header with no key', row({ url: 'https://x.example.com', headers: 'no colon here' }), 'ruleHintHeaderMalformed'],
    ['a bad secret handle', row({ url: 'https://x.example.com', secretRef: '/abs' }), 'ruleHintSecretHandleInvalid'],
    ['a dot segment in the handle', row({ url: 'https://x.example.com', secretRef: 'a/../b' }), 'ruleHintSecretHandleInvalid'],
    ['a timeout over the ceiling', row({ url: 'https://x.example.com', timeoutMs: '20001' }), 'ruleHintTimeoutRange'],
    ['a fractional timeout', row({ url: 'https://x.example.com', timeoutMs: '1.5' }), 'ruleHintTimeoutRange'],
    ['no connector', row({ type: 'publish', connectorRef: '' }), 'ruleHintConnectorRequired'],
    ['a publish timeout over the ceiling', row({ type: 'publish', connectorRef: 'pager', timeoutMs: '99999' }), 'ruleHintTimeoutRange'],
  ])('refuses %s', (_name, r, key) => {
    expect(outboundHint(t, r)?.startsWith(key)).toBe(true);
  });

  // The counterweight: a hint that fired on a good action would block every save.
  it.each([
    row({ url: 'https://x.example.com/hook', headers: 'A: b', secretRef: 'team/token.v1', timeoutMs: '20000' }),
    row({ url: 'http://x.example.com', timeoutMs: '' }),
    row({ type: 'publish', connectorRef: 'pager', payloadTemplate: '"x"', timeoutMs: '0' }),
    // The server's URL check is a parse, not a whitespace ban: a space in the path or query is
    // accepted there (net/http escapes it), so a stored rule carrying one must stay saveable.
    row({ url: 'https://x.example.com/a b?q=c d' }),
    // Exactly at the handle's byte cap.
    row({ url: 'https://x.example.com', secretRef: 'a'.repeat(256) }),
  ])('accepts a complete action', (r) => {
    expect(outboundHint(t, r)).toBeNull();
  });

  it('blocks Create in the form until the webhook has a usable URL', async () => {
    render(<DetectionRuleForm profileToken="p" entity={rule(withActions([{ type: 'httpCall', httpCall: { url: 'ftp://nope' } }]))} onDone={() => {}} />);
    expect(screen.getByText(/must start with http:\/\/ or https:\/\//)).toBeTruthy();
    expect((screen.getByRole('button', { name: /save changes/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});

// A rule authored on one surface must open identically on the other. The canvas's own output is
// pinned by a golden shared with the Go synthesis tests, so this runs against the bytes the server
// actually produces for an outbound rule, not against a shape invented here.
describe('canvas and form agree on outbound actions', () => {
  const canvasDefinition = JSON.stringify(GOLDEN.definition);

  it('opens a canvas-authored rule in the form and saves it back unchanged', () => {
    const parsed = parseDefinition(canvasDefinition)!;
    expect(ruleSurvivesRoundTrip(canvasDefinition, rebuildFrom(parsed))).toBe(true);
    expect(sameLogicalRule(canvasDefinition, rebuildFrom(parsed))).toBe(true);
  });

  it('opens a form-authored rule on the canvas with every field on the action node', () => {
    const args: BuildArgs = {
      definitionName: 'A rule', description: '', severity: '', ruleType: 'threshold', condMode: 'structured',
      condMetric: 'tempC', condOp: 'gt', boundKind: 'literal', condThreshold: '30', condAttr: '', cel: '',
      valueMetric: '', aggFunc: 'avg', windowMode: 'tumbling', windowStr: '', holdStr: '', timeoutStr: '',
      gapStr: '', countStr: '', rate: false, aggOp: 'gt', aggThreshold: '', anchorType: '', memberCapStr: '',
      actions: [
        { type: 'httpCall', alarmKey: '', command: '', payload: '', url: 'https://hooks.example.com/alarm', headers: 'X-Source: devicechain', bodyTemplate: '"x"', secretRef: 'team/tok', timeoutMs: '5000' },
        { type: 'publish', alarmKey: '', command: '', payload: '', connectorRef: 'pager', payloadTemplate: '"y"', timeoutMs: '1500' },
      ],
    };
    const def = buildDefinition(args);
    const { graph, error } = graphFromDefinition(def, 'thermostat');
    expect(error).toBeUndefined();
    const actionConfigs = graph!.nodes.filter((n) => n.type === 'action').map((n) => n.config);
    expect(actionConfigs).toEqual([
      { action: 'httpCall', url: 'https://hooks.example.com/alarm', headers: { 'X-Source': 'devicechain' }, bodyTemplate: '"x"', secretRef: 'team/tok', timeoutMs: 5000 },
      { action: 'publish', connectorRef: 'pager', payloadTemplate: '"y"', timeoutMs: 1500 },
    ]);
    // ...and back: what the canvas holds reads into the form as the same rule.
    expect(sameLogicalRule(def, rebuildFrom(parseDefinition(def)!))).toBe(true);
  });

  // The control: a field the form does not model is still reported, not swallowed.
  it('reports an outbound action carrying a field neither surface models', () => {
    const exotic = withActions([{ type: 'httpCall', httpCall: { url: 'https://x.example.com', futureKnob: 1 } }]);
    expect(ruleSurvivesRoundTrip(exotic, rebuildFrom(parseDefinition(exotic)!))).toBe(false);
  });
});
