// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The content upload transport, against a fake XMLHttpRequest. What is asserted is what the
// service depends on: the URL, the bearer token, the body being the file itself, and a
// refusal reaching the caller with the server's body untouched.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { tokenMock } = vi.hoisted(() => ({ tokenMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return { ...actual, resolveAuthToken: () => tokenMock() };
});

import {
  ArtifactUploadError,
  artifactContentUrl,
  formatBytes,
  parseByteCount,
  uploadArtifactContent,
} from './artifactUpload';

class FakeXhr {
  static last: FakeXhr;
  method = '';
  url = '';
  headers: Record<string, string> = {};
  body: unknown = null;
  status = 0;
  responseText = '';
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  onabort: (() => void) | null = null;
  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  send(body: unknown) {
    this.body = body;
  }
  abort() {
    this.onabort?.();
  }
}

beforeEach(() => {
  tokenMock.mockReset();
  tokenMock.mockResolvedValue('tok-123');
  vi.stubGlobal('XMLHttpRequest', FakeXhr);
});
afterEach(() => vi.unstubAllGlobals());

const file = new File(['abc'], 'a.bin', { type: 'application/octet-stream' });
const tick = () => new Promise((r) => setTimeout(r, 0));

describe('uploadArtifactContent', () => {
  it('PUTs the file itself to the artifact content endpoint with the bearer token', async () => {
    const p = uploadArtifactContent('fw 1', file);
    await tick();
    const x = FakeXhr.last;
    expect(x.method).toBe('PUT');
    expect(x.url).toBe('/api/update-management/updates/artifacts/fw%201/content');
    expect(x.url).toBe(artifactContentUrl('fw 1'));
    expect(x.headers.Authorization).toBe('Bearer tok-123');
    expect(x.body).toBe(file);

    x.status = 200;
    x.responseText = JSON.stringify({ token: 'fw 1', state: 'AVAILABLE', sizeBytes: '3', digest: 'sha256:x' });
    x.onload!();
    await expect(p).resolves.toMatchObject({ state: 'AVAILABLE', sizeBytes: '3' });
  });

  it('reports upload progress', async () => {
    const seen: [number, number][] = [];
    const p = uploadArtifactContent('t', file, { onProgress: (l, t) => seen.push([l, t]) });
    await tick();
    FakeXhr.last.upload.onprogress!({ lengthComputable: true, loaded: 1, total: 3 });
    FakeXhr.last.upload.onprogress!({ lengthComputable: false, loaded: 2, total: 0 });
    FakeXhr.last.status = 200;
    FakeXhr.last.responseText = '{}';
    FakeXhr.last.onload!();
    await p;
    expect(seen).toEqual([[1, 3]]);
  });

  it('rejects with the server body verbatim on a refusal', async () => {
    const p = uploadArtifactContent('t', file);
    await tick();
    FakeXhr.last.status = 422;
    FakeXhr.last.responseText = "the content's SHA-256 does not match the artifact's declared digest\n";
    FakeXhr.last.onload!();
    const err = await p.catch((e) => e);
    expect(err).toBeInstanceOf(ArtifactUploadError);
    expect(err.message).toBe("the content's SHA-256 does not match the artifact's declared digest");
    expect(err.status).toBe(422);
  });

  it('does not report success for a 2xx it cannot read', async () => {
    const p = uploadArtifactContent('t', file);
    await tick();
    FakeXhr.last.status = 200;
    FakeXhr.last.responseText = 'not json';
    FakeXhr.last.onload!();
    await expect(p).rejects.toThrow(/could not be read/);
  });

  it('refuses without a token instead of sending an unauthenticated PUT', async () => {
    tokenMock.mockResolvedValue(null);
    await expect(uploadArtifactContent('t', file)).rejects.toThrow('Not signed in.');
  });

  it('rejects on a network error', async () => {
    const p = uploadArtifactContent('t', file);
    await tick();
    FakeXhr.last.onerror!();
    await expect(p).rejects.toThrow('Could not reach the server.');
  });
});

describe('byte helpers', () => {
  it('parses only plain decimal strings, never treating junk as a limit', () => {
    expect(parseByteCount('5368709120')).toBe(5368709120n);
    expect(parseByteCount('-1')).toBeNull();
    expect(parseByteCount('1e9')).toBeNull();
    expect(parseByteCount('')).toBeNull();
    expect(parseByteCount(null)).toBeNull();
  });

  it('formats sizes in binary units', () => {
    expect(formatBytes('512')).toBe('512 B');
    expect(formatBytes('2048')).toBe('2.0 KiB');
    expect(formatBytes(5368709120n)).toBe('5.0 GiB');
  });
});
