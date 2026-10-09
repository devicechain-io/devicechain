// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

/// <reference types="node" />

import { describe, expect, it } from 'vitest';

import { ReplayLoadError, loadReplay, sha256Hex } from './load';
import { BOARD_URL, ORIGIN, RECORDING_URL, boardBytes, boardSha256, fixtureRecordingText, stubFetch } from './testing/harness';

async function failure(p: Promise<unknown>): Promise<ReplayLoadError> {
  try {
    await p;
  } catch (e) {
    expect(e).toBeInstanceOf(ReplayLoadError);
    return e as ReplayLoadError;
  }
  throw new Error('expected loadReplay to reject');
}

describe('loadReplay', () => {
  it('loads a recording and the board it names, and fetches exactly those two files', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    const loaded = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(stub.calls).toEqual([RECORDING_URL, BOARD_URL]);
    expect(loaded.recording.runId).toBe('run-fixture');
    expect(loaded.definition.widgets.length).toBeGreaterThan(30);
  });

  it('takes the site anchor from the pinned board: assigned / customer / acme-earthworks', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    const loaded = await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(loaded.siteAnchors).toEqual([
      { relationship: 'assigned', targetType: 'customer', targetToken: 'acme-earthworks' },
    ]);
  });

  it('asks for each file without credentials and with redirects refused', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    await loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(stub.inits).toHaveLength(2);
    for (const init of stub.inits) expect(init).toMatchObject({ redirect: 'error', credentials: 'omit' });
  });

  it('turns a refused redirect into a fetch failure, not a silent follow', async () => {
    const redirecting = (async (_u: RequestInfo | URL, init?: RequestInit) => {
      if (init?.redirect === 'error') throw new TypeError('redirect mode is set to error');
      return new Response('{}');
    }) as typeof fetch;
    const err = await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: redirecting, origin: ORIGIN }));
    expect(err.code).toBe('fetch');
  });

  it('compares the whole hash: one differing only in its last character is refused', async () => {
    const real = await boardSha256();
    const flipped = real.slice(0, -1) + (real.endsWith('0') ? '1' : '0');
    expect(flipped).not.toBe(real);
    expect(flipped.slice(0, 63)).toBe(real.slice(0, 63));
    const text = await fixtureRecordingText((d) => {
      d.board.sha256 = flipped;
    });
    const stub = stubFetch({ [RECORDING_URL]: text, [BOARD_URL]: boardBytes() });
    const err = await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }));
    expect(err.code).toBe('boardMismatch');
  });

  it('refuses a board whose SHA-256 differs from the recording header, naming the mismatch', async () => {
    const changed = Buffer.concat([boardBytes(), Buffer.from(' ')]);
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: changed });
    const err = await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }));
    expect(err.code).toBe('boardMismatch');
  });

  it('accepts the board only because the hash matches (control: the same bytes with the header hash changed are refused)', async () => {
    const text = await fixtureRecordingText((d) => {
      d.board.sha256 = '0'.repeat(64);
    });
    const stub = stubFetch({ [RECORDING_URL]: text, [BOARD_URL]: boardBytes() });
    const err = await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }));
    expect(err.code).toBe('boardMismatch');
    expect(await boardSha256()).not.toBe('0'.repeat(64));
  });

  it.each([
    ['another host', 'https://elsewhere.test/run.json'],
    ['a protocol-relative URL', '//elsewhere.test/run.json'],
    ['another scheme', 'http://site.test/run.json'],
  ])('refuses %s without fetching anything', async (_n, url) => {
    const stub = stubFetch({});
    const err = await failure(loadReplay({ recordingUrl: url, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }));
    expect(err.code).toBe('crossOrigin');
    expect(stub.calls).toEqual([]);
  });

  it('refuses a cross-origin board URL before fetching the recording', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText() });
    const err = await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: 'https://elsewhere.test/b.json', fetchFn: stub.fetchFn, origin: ORIGIN }));
    expect(err.code).toBe('crossOrigin');
    expect(stub.calls).toEqual([]);
  });

  it('accepts a relative URL, resolved against the page origin', async () => {
    const stub = stubFetch({ [RECORDING_URL]: await fixtureRecordingText(), [BOARD_URL]: boardBytes() });
    await loadReplay({ recordingUrl: '/assets/run.json', boardUrl: '/assets/sp-dashboard.json', fetchFn: stub.fetchFn, origin: ORIGIN });
    expect(stub.calls).toEqual([RECORDING_URL, BOARD_URL]);
  });

  it('reports a missing address, a 404 and an unparseable recording as distinct codes', async () => {
    expect((await failure(loadReplay({ recordingUrl: '', boardUrl: BOARD_URL, origin: ORIGIN }))).code).toBe('missingUrl');
    const missing = stubFetch({});
    expect((await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: missing.fetchFn, origin: ORIGIN }))).code).toBe('fetch');
    const bad = stubFetch({ [RECORDING_URL]: '{"kind":"nope"}', [BOARD_URL]: boardBytes() });
    expect((await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: bad.fetchFn, origin: ORIGIN }))).code).toBe('recordingInvalid');
  });

  it('refuses a recording with a key the format does not define', async () => {
    const text = await fixtureRecordingText((d) => {
      d.extra = 1;
    });
    const stub = stubFetch({ [RECORDING_URL]: text, [BOARD_URL]: boardBytes() });
    expect((await failure(loadReplay({ recordingUrl: RECORDING_URL, boardUrl: BOARD_URL, fetchFn: stub.fetchFn, origin: ORIGIN }))).code).toBe('recordingInvalid');
  });
});

describe('sha256Hex', () => {
  it('matches the known digest of "abc"', async () => {
    const bytes = new TextEncoder().encode('abc');
    expect(await sha256Hex(bytes.buffer as ArrayBuffer)).toBe(
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad',
    );
  });
});
