// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pages load on first visit (App.tsx), so a browser still holding the index.html of the previous
// deploy asks for page chunks under hashes the new deploy no longer serves. Vite reports that
// failure as a `vite:preloadError` event on window; the remedy is a reload, which fetches the new
// index.html and with it the chunk names that exist. It is done ONCE per window of time: a chunk
// that is missing from the new deploy too would otherwise reload forever, so a second failure
// soon after is left to throw, and the route's error boundary offers the Reload button instead.

const KEY = 'dc.chunkReloadAt';
const WINDOW_MS = 60_000;

export interface ChunkReloadHost {
  addEventListener(type: 'vite:preloadError', listener: (e: Event) => void): void;
  sessionStorage: Pick<Storage, 'getItem' | 'setItem'>;
  location: { reload(): void };
}

export function installChunkReload(host: ChunkReloadHost, now: () => number = Date.now): void {
  host.addEventListener('vite:preloadError', (e) => {
    let last = 0;
    try {
      last = Number(host.sessionStorage.getItem(KEY) ?? 0) || 0;
    } catch {
      // Storage blocked: without a record of the last attempt a reload could loop, so leave the
      // failure to the error boundary.
      return;
    }
    if (now() - last < WINDOW_MS) return;
    try {
      host.sessionStorage.setItem(KEY, String(now()));
    } catch {
      return;
    }
    e.preventDefault();
    host.location.reload();
  });
}
