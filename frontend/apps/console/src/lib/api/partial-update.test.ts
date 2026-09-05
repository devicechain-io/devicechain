// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// WHAT A CALLER LEAVES OUT MUST TRAVEL AS ABSENT, NOT AS NULL.
//
// 🔴 THIS FILE EXISTS BECAUSE THE THREE-STATE UPDATE SEMANTIC RESTS ON A PROPERTY OF
// JSON.stringify. The wrappers below forward a rest object straight through, so a key
// the caller never set arrives as `{ secret: undefined }` — and it reaches the server as
// nothing at all only because JSON.stringify DROPS undefined object values. That is a
// language guarantee rather than something this code does, which is exactly why it is
// worth pinning: it is invisible at the call site, and if the transport ever grew its
// own serializer, every "leave this alone" would silently become "clear this" while the
// requests still looked correct in the debugger.
//
// The three mutations here are the ones where that mistake costs the most: `secret` is a
// write-only credential nobody can read back, so a request that cleared it by accident
// would be discovered when an outbound send or an inference call started failing, with
// no way to recover the value.
//
// The counterweight is in every case: an EXPLICIT null must still arrive as a null. A
// serializer that dropped both would pass the first half of each assertion and make the
// clear operation unreachable.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', () => ({ gql: gqlMock }));

const { updateConnector } = await import('./connectors');
const { updateAiProvider } = await import('./ai-inference-admin');
const { updateDeviceProfile } = await import('./device-management');

/** The `request` variable as it would reach the wire, after JSON serialization. */
function sentRequest(): Record<string, unknown> {
  const variables = gqlMock.mock.calls[0][2] as { request: unknown };
  return JSON.parse(JSON.stringify(variables.request)) as Record<string, unknown>;
}

beforeEach(() => {
  gqlMock.mockReset();
  gqlMock.mockResolvedValue({
    updateConnector: { token: 't', updatedAt: null },
    updateAiProvider: { token: 't', updatedAt: null },
    updateDeviceProfile: { token: 't' },
  });
});

describe('updateConnector', () => {
  it('omits a field the caller said nothing about', async () => {
    await updateConnector('pager', { name: 'Renamed' });

    const request = sentRequest();
    expect(request).toEqual({ name: 'Renamed' });
    expect('secret' in request).toBe(false);
    expect('config' in request).toBe(false);
  });

  it('sends an explicit null for a field the caller cleared', async () => {
    await updateConnector('pager', { description: null, secret: null });

    const request = sentRequest();
    expect(request).toEqual({ description: null, secret: null });
  });

  it('does not put the token back in the payload', async () => {
    await updateConnector('pager', { name: 'Renamed' });
    expect('token' in sentRequest()).toBe(false);
  });
});

describe('updateAiProvider', () => {
  it('omits a field the caller said nothing about', async () => {
    await updateAiProvider('primary', { model: 'claude-haiku-4-5-20251001' });

    const request = sentRequest();
    expect(request).toEqual({ model: 'claude-haiku-4-5-20251001' });
    expect('secret' in request).toBe(false);
    expect('enabled' in request).toBe(false);
  });

  it('sends an explicit null for a field the caller cleared', async () => {
    await updateAiProvider('primary', { endpoint: null, secret: null });

    expect(sentRequest()).toEqual({ endpoint: null, secret: null });
  });

  it('keeps expectedUpdatedAt out of the request payload', async () => {
    await updateAiProvider('primary', { enabled: false, expectedUpdatedAt: '2026-01-01T00:00:00Z' });

    const request = sentRequest();
    expect(request).toEqual({ enabled: false });
    expect(gqlMock.mock.calls[0][2]).toMatchObject({ expectedUpdatedAt: '2026-01-01T00:00:00Z' });
  });
});

describe('updateDeviceProfile', () => {
  // 🔴 `location` IS THE ONE THIS CONVERSION WAS FOR. It is the profile's position
  // declaration, no console form edits it, and under the previous full-replace input
  // omitting it CLEARED it — so editing a profile's name un-declared position for every
  // device built on it. It must be absent from a request that says nothing about it.
  it('omits the position declaration when the caller says nothing about it', async () => {
    await updateDeviceProfile('tracker', { name: 'Renamed', category: null });

    const request = sentRequest();
    expect(request).toEqual({ name: 'Renamed', category: null });
    expect('location' in request).toBe(false);
    expect('metadata' in request).toBe(false);
  });

  it('sends an explicit null when the caller does mean to clear the declaration', async () => {
    await updateDeviceProfile('tracker', { location: null });

    expect(sentRequest()).toEqual({ location: null });
  });
});
