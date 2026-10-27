// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { Integration } from '@/lib/api/integrations';
import {
  draftOf,
  emptyDraft,
  genericSettings,
  identityOf,
  integrationCreateRequest,
  integrationEditRequest,
  integrationProblems,
  parseGeneric,
  withType,
  type IntegrationDraft,
} from './integrationDraft';

const generic = (over: Partial<IntegrationDraft> = {}): IntegrationDraft => ({
  ...emptyDraft('GENERIC_JSON'),
  deviceTypeTokens: ['meter'],
  aliasName: 'serial',
  idKind: 'pointer',
  idValue: '/device/id',
  ...over,
});

const stored = (over: Partial<Integration> = {}): Integration =>
  ({
    id: '1',
    token: 'ig',
    name: 'Gateway fleet',
    description: null,
    type: 'GENERIC_JSON',
    enabled: true,
    identity: 'ALIAS:custom.serial',
    generic: '{"idFrom":"pointer:/device/id","itemsPointer":"/uplinks"}',
    linkMetrics: false,
    tokenIssuedAt: '2026-10-01T00:00:00Z',
    undecodedCount: 0,
    lastUndecodedAt: null,
    lastUndecodedSample: null,
    lastAuthFailureAt: null,
    createdAt: null,
    updatedAt: null,
    deviceTypes: [{ id: 't', token: 'meter', name: 'Meter' }],
    ...over,
  }) as Integration;

describe('the scope', () => {
  it('requires at least one device type', () => {
    expect(integrationProblems(emptyDraft())).toEqual(['scope']);
    expect(integrationProblems({ ...emptyDraft(), deviceTypeTokens: ['meter'] })).toEqual([]);
  });
});

describe('the generic settings validation', () => {
  it('accepts a well-formed JSON source', () => {
    expect(integrationProblems(generic({ itemsPointer: '/uplinks', timePointer: '/time' }))).toEqual([]);
  });

  it.each([
    ['a pointer without a leading slash', { idValue: 'device/id' }, 'idValue'],
    ['a bad escape in a pointer', { idValue: '/a~2b' }, 'idValue'],
    ['an empty identifier location', { idValue: '' }, 'idValue'],
    ['a header name with a space', { idKind: 'header', idValue: 'X Device' }, 'idValue'],
    ['a query name starting with a hyphen', { idKind: 'query', idValue: '-d' }, 'idValue'],
    ['an items pointer that is not a pointer', { itemsPointer: 'uplinks' }, 'itemsPointer'],
    ['a time pointer that is not a pointer', { timePointer: 'time' }, 'timePointer'],
    ['a frame pointer with no encoding', { framePointer: '/frame' }, 'frameEncoding'],
    ['an encoding with no frame pointer', { frameEncoding: 'HEX' }, 'framePointer'],
    ['an upper-case alias scheme name', { aliasName: 'Serial' }, 'aliasName'],
    ['an empty alias scheme name', { aliasName: '' }, 'aliasName'],
  ] as const)('refuses %s', (_label, over, problem) => {
    expect(integrationProblems(generic(over as Partial<IntegrationDraft>))).toContain(problem);
  });

  it('accepts header and query identifiers', () => {
    expect(integrationProblems(generic({ idKind: 'header', idValue: 'X-Device-Id' }))).toEqual([]);
    expect(integrationProblems(generic({ idKind: 'query', idValue: 'device.id' }))).toEqual([]);
  });

  it('accepts a frame pointer with its encoding', () => {
    expect(integrationProblems(generic({ framePointer: '/frame', frameEncoding: 'BASE64' }))).toEqual([]);
  });

  it('refuses a pointer identifier for a binary body, which has no fields', () => {
    const d = generic({ type: 'GENERIC_BINARY', idKind: 'pointer', idValue: '/id' });
    expect(integrationProblems(d)).toContain('idValue');
    expect(integrationProblems({ ...d, idKind: 'header', idValue: 'X-Id' })).toEqual([]);
  });

  it('writes only the keys that are set, and none of the JSON-only keys for a binary body', () => {
    expect(JSON.parse(genericSettings(generic()))).toEqual({ idFrom: 'pointer:/device/id' });
    expect(
      JSON.parse(genericSettings(generic({ itemsPointer: '/u', timePointer: '/t', framePointer: '/f', frameEncoding: 'HEX' }))),
    ).toEqual({ idFrom: 'pointer:/device/id', itemsPointer: '/u', timePointer: '/t', framePointer: '/f', frameEncoding: 'HEX' });
    const bin = generic({ type: 'GENERIC_BINARY', idKind: 'header', idValue: 'X-Id', itemsPointer: '/stale' });
    expect(JSON.parse(genericSettings(bin))).toEqual({ idFrom: 'header:X-Id' });
  });

  it('round-trips the stored settings text into the draft', () => {
    const d = draftOf(stored());
    expect(d).toMatchObject({ idKind: 'pointer', idValue: '/device/id', itemsPointer: '/uplinks', aliasName: 'serial' });
    expect(JSON.parse(genericSettings(d))).toEqual(JSON.parse(stored().generic!));
    expect(parseGeneric('not json')).toEqual({});
    expect(parseGeneric(null)).toEqual({});
  });
});

describe('the create request', () => {
  it('sends a network server by its identity and no generic settings', () => {
    const r = integrationCreateRequest(' cs ', { ...emptyDraft('TTS_V3'), deviceTypeTokens: ['meter'], identity: 'DEVICE_ID', linkMetrics: true });
    expect(r).toEqual({
      token: 'cs',
      name: null,
      description: null,
      type: 'TTS_V3',
      enabled: true,
      deviceTypeTokens: ['meter'],
      identity: 'DEVICE_ID',
      linkMetrics: true,
    });
    expect(r).not.toHaveProperty('generic');
  });

  it('sends a generic source with its alias identity and settings document', () => {
    const r = integrationCreateRequest('gen', generic());
    expect(r.identity).toBe('ALIAS:custom.serial');
    expect(JSON.parse(r.generic!)).toEqual({ idFrom: 'pointer:/device/id' });
  });

  it('never carries a bearer token: the server generates it', () => {
    const r = integrationCreateRequest('gen', generic());
    expect(Object.keys(r).join()).not.toMatch(/secret|bearer|issued/i);
  });

  it('keeps what applies when the type changes and resets the rest', () => {
    const d = withType({ ...generic({ name: 'Mine', linkMetrics: true }) }, 'CHIRPSTACK_V4');
    expect(d).toMatchObject({ type: 'CHIRPSTACK_V4', name: 'Mine', linkMetrics: true, identity: 'DEV_EUI', aliasName: '' });
    const toBinary = withType(generic(), 'GENERIC_BINARY');
    expect(toBinary).toMatchObject({ idKind: 'header', idValue: '', aliasName: 'serial' });
  });
});

describe('the edit request', () => {
  const initial = draftOf(stored());

  it('names nothing when nothing changed', () => {
    expect(integrationEditRequest(initial, { ...initial })).toEqual({});
  });

  it('names only the fields that changed', () => {
    expect(integrationEditRequest(initial, { ...initial, enabled: false })).toEqual({ enabled: false });
    expect(integrationEditRequest(initial, { ...initial, name: ' Renamed ' })).toEqual({ name: 'Renamed' });
    expect(integrationEditRequest(initial, { ...initial, name: '' })).toEqual({ name: null });
    expect(integrationEditRequest(initial, { ...initial, deviceTypeTokens: ['meter', 'pump'] })).toEqual({
      deviceTypeTokens: ['meter', 'pump'],
    });
  });

  it('treats a reordered scope as unchanged', () => {
    const two = { ...initial, deviceTypeTokens: ['a', 'b'] };
    expect(integrationEditRequest(two, { ...two, deviceTypeTokens: ['b', 'a'] })).toEqual({});
  });

  it('re-sends the settings document only when a setting changed', () => {
    const r = integrationEditRequest(initial, { ...initial, timePointer: '/time' });
    expect(Object.keys(r)).toEqual(['generic']);
    expect(JSON.parse(r.generic!)).toEqual({ idFrom: 'pointer:/device/id', itemsPointer: '/uplinks', timePointer: '/time' });
  });

  it('never names the type or the token', () => {
    const r = integrationEditRequest(initial, { ...initial, name: 'x', enabled: false, linkMetrics: true });
    expect(r).not.toHaveProperty('type');
    expect(r).not.toHaveProperty('token');
  });

  it('identity of a generic draft is its alias scheme', () => {
    expect(identityOf(initial)).toBe('ALIAS:custom.serial');
  });
});
