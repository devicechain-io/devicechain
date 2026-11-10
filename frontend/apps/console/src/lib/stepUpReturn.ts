// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

const KEY = 'dc.stepUpReturn';
const TTL = 15 * 60 * 1000;

function safePath(path: unknown): path is string {
  return typeof path === 'string' && /^\/(?!\/)/.test(path) &&
    !/[\\\x00-\x20]/.test(path) && !/[a-z][a-z0-9+.-]*:/i.test(path);
}

export function setStepUpReturn(path: string): void {
  try {
    sessionStorage.removeItem(KEY);
    if (safePath(path)) sessionStorage.setItem(KEY, JSON.stringify({ path, at: Date.now() }));
  } catch { /* Storage may be blocked. Sign-in still works. */ }
}

export function consumeStepUpReturn(): string | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    sessionStorage.removeItem(KEY);
    if (!raw) return null;
    const entry: unknown = JSON.parse(raw);
    if (!entry || typeof entry !== 'object' || !('path' in entry) || !('at' in entry)) return null;
    if (!safePath(entry.path) || typeof entry.at !== 'number' ||
        !Number.isFinite(entry.at) || Date.now() < entry.at || Date.now() - entry.at >= TTL) return null;
    return entry.path;
  } catch { return null; }
}
