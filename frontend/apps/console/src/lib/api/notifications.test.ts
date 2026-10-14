// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { beforeEach, describe, expect, it, vi } from 'vitest';

const gql = vi.fn(async (..._args: unknown[]) => ({}));
vi.mock('@devicechain/client', () => ({ gql: (...a: unknown[]) => gql(...a) }));

import { createSeverityPolicy, severityCovered, type NotificationPolicy } from './notifications';

beforeEach(() => gql.mockClear());

type Channel = { token: string; enabled: boolean } | null;
const policy = (o: { enabled?: boolean; severity?: string; channel?: Channel } = {}): NotificationPolicy => ({
  token: 'p',
  name: null,
  enabled: o.enabled ?? true,
  rules: [{ severity: o.severity ?? 'MAJOR', channel: o.channel === undefined ? { token: 'c', enabled: true } : o.channel }],
});

describe('severityCovered', () => {
  it('matches the uppercase alarm tier against the lowercase rule severity', () => {
    expect(severityCovered([policy()], 'major')).toBe(true);
    expect(severityCovered([policy()], 'critical')).toBe(false);
  });
  it('treats * as any severity', () => {
    expect(severityCovered([policy({ severity: '*' })], 'warning')).toBe(true);
  });
  it('ignores a disabled policy, a deleted channel and a disabled channel', () => {
    expect(severityCovered([policy({ enabled: false })], 'major')).toBe(false);
    expect(severityCovered([policy({ channel: null })], 'major')).toBe(false);
    expect(severityCovered([policy({ channel: { token: 'c', enabled: false } })], 'major')).toBe(false);
  });
  it('is not covered with no policies at all', () => {
    expect(severityCovered([], 'major')).toBe(false);
  });
});

describe('createSeverityPolicy', () => {
  it('writes an enabled tenant-wide policy with one uppercase-severity rule', async () => {
    await createSeverityPolicy({ token: 'n', name: 'N', severity: 'major', channelToken: 'ops' });
    const vars = gql.mock.calls[0][2] as { request: Record<string, unknown> };
    expect(vars.request).toEqual({
      token: 'n',
      name: 'N',
      enabled: true,
      rules: [{ severity: 'MAJOR', channelToken: 'ops', recipients: null }],
    });
    // Policies cannot be device-type scoped; the request must not try.
    expect(vars.request).not.toHaveProperty('deviceTypeToken');
  });
  it('sends recipients as a JSON array string', async () => {
    await createSeverityPolicy({ token: 'n', name: 'N', severity: 'warning', channelToken: 'm', recipients: ['a@x.io'] });
    const vars = gql.mock.calls[0][2] as { request: { rules: { recipients: string }[] } };
    expect(vars.request.rules[0].recipients).toBe('["a@x.io"]');
  });
});
