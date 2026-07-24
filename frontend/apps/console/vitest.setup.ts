// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Test setup: install a deterministic in-memory Web Storage.
//
// Newer Node (>=22) ships an EXPERIMENTAL native `localStorage` global that is
// unavailable without the `--localstorage-file` flag and, crucially, SHADOWS the
// working `localStorage` jsdom installs — so under CI's Node the bare
// `localStorage` binding that browser code (and these tests) use resolves to an
// undefined value and throws (older Node, which does not define the global at
// all, lets jsdom's win, which is why this only bit in CI). Rather than depend on
// a Node flag or on jsdom's storage-origin quirks, install our own implementation
// on both globalThis and window AFTER the environment is set up. This runs only
// under vitest (setupFiles) — production browser code uses the real localStorage.

class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  clear(): void {
    this.store.clear();
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) as string) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
}

const storage = new MemoryStorage();
for (const target of [globalThis, typeof window !== 'undefined' ? window : undefined]) {
  if (!target) continue;
  try {
    // Node's experimental global is defined configurable, so this replaces it;
    // fall back to assignment if some environment defines it otherwise.
    Object.defineProperty(target, 'localStorage', {
      configurable: true,
      writable: true,
      value: storage,
    });
  } catch {
    (target as unknown as { localStorage: Storage }).localStorage = storage;
  }
}
