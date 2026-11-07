// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  configOf,
  createRequest,
  draftOf,
  draftProblem,
  editRequest,
  emptyDraft,
  isNative,
} from './channelRequest';
import type { NotificationChannelRecord } from '@/lib/api/notifications';

const stored = (over: Partial<NotificationChannelRecord>): NotificationChannelRecord => ({
  id: '1',
  token: 'c',
  name: 'Ops',
  description: null,
  channelType: 'slack',
  config: null,
  hasSecret: true,
  enabled: true,
  updatedAt: '2026-11-06T00:00:00Z',
  ...over,
});

describe('channelRequest', () => {
  it('knows the native types', () => {
    expect(['slack', 'teams', 'pagerduty'].every(isNative)).toBe(true);
    expect(isNative('webhook')).toBe(false);
    expect(isNative('smtp')).toBe(false);
  });

  it('builds a slack create with the webhook URL as the secret and no config when no link is set', () => {
    const d = { ...emptyDraft('slack'), name: 'Ops', secret: 'https://hooks.slack.com/services/T/B/x' };
    expect(createRequest('ops-slack', d)).toEqual({
      token: 'ops-slack',
      channelType: 'slack',
      enabled: true,
      name: 'Ops',
      secret: 'https://hooks.slack.com/services/T/B/x',
    });
  });

  it('writes a pagerduty config only for what departs from the default', () => {
    expect(configOf(emptyDraft('pagerduty'))).toBeNull();
    expect(
      configOf({ ...emptyDraft('pagerduty'), region: 'eu', source: ' plant ', linkBaseUrl: 'https://c.example' }),
    ).toBe('{"region":"eu","source":"plant","linkBaseUrl":"https://c.example"}');
  });

  it('keeps raw config for smtp and webhook', () => {
    expect(configOf({ ...emptyDraft('webhook'), rawConfig: ' {"url":"https://x"} ' })).toBe('{"url":"https://x"}');
    expect(configOf(emptyDraft('smtp'))).toBeNull();
  });

  it('requires a secret for a native create but not once one is stored', () => {
    const d = emptyDraft('teams');
    expect(draftProblem(d, false)).toBe('secret');
    expect(draftProblem(d, true)).toBeNull();
    expect(draftProblem({ ...d, secret: 'https://x' }, false)).toBeNull();
  });

  it('rejects raw config that is not a JSON object', () => {
    for (const bad of ['nope', '[1]', '3', 'null']) {
      expect(draftProblem({ ...emptyDraft('webhook'), rawConfig: bad }, false)).toBe('config');
    }
    expect(draftProblem({ ...emptyDraft('webhook'), rawConfig: '{}' }, false)).toBeNull();
  });

  it('an unchanged edit names nothing, and never names the secret', () => {
    const c = stored({ channelType: 'pagerduty', config: '{"source":"plant","region":"eu"}' });
    expect(editRequest(c, draftOf(c))).toEqual({});
  });

  it('names the secret only when one was typed', () => {
    const c = stored({});
    expect(editRequest(c, { ...draftOf(c), secret: 'https://new' })).toEqual({ secret: 'https://new' });
    expect('secret' in editRequest(c, draftOf(c))).toBe(false);
  });

  it('clears a config the operator emptied, and a name with null', () => {
    const c = stored({ config: '{"linkBaseUrl":"https://c.example"}' });
    const d = { ...draftOf(c), linkBaseUrl: '', name: '' };
    expect(editRequest(c, d)).toEqual({ config: null, name: null });
  });

  it('reads key order and spacing as no change', () => {
    const c = stored({ channelType: 'pagerduty', config: '{ "source": "a", "region": "eu" }' });
    expect(editRequest(c, draftOf(c))).toEqual({});
  });

  it('carries enabled', () => {
    const c = stored({});
    expect(editRequest(c, { ...draftOf(c), enabled: false })).toEqual({ enabled: false });
  });

  it('reads an unparseable stored config without throwing', () => {
    const c = stored({ config: '{broken' });
    expect(() => draftOf(c)).not.toThrow();
  });
});
