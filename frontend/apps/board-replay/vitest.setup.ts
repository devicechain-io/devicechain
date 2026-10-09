// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Test setup: a deterministic in-memory Web Storage (newer Node ships an experimental
// native localStorage global that shadows jsdom's; see apps/dashboard/vitest.setup.ts for
// the longer account), plus the two browser APIs the real widgets call that jsdom lacks.

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
    Object.defineProperty(target, 'localStorage', { configurable: true, writable: true, value: storage });
  } catch {
    (target as unknown as { localStorage: Storage }).localStorage = storage;
  }
}

// The widgets observe their container's size; jsdom has no ResizeObserver. A no-op is
// enough: nothing here asserts on chart geometry.
class NoopResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = NoopResizeObserver as unknown as typeof ResizeObserver;
}

// jsdom has no canvas, and the chart widgets (ECharts/zrender) draw on one. A context whose
// every method is a no-op lets them mount and dispose. The one thing it keeps is the TEXT
// drawn (fillText/strokeText), because a gauge's reading lives only on the canvas and a test
// that wants to know what the gauge says has no other way to ask.
export const canvasText: string[] = [];
(globalThis as unknown as { __canvasText: string[] }).__canvasText = canvasText;
{
  const noop2d = new Proxy(
    {},
    {
      get: (_t, prop) =>
        prop === 'canvas'
          ? undefined
          : prop === 'fillText' || prop === 'strokeText'
            ? (text: string) => {
                canvasText.push(String(text));
              }
          : prop === 'measureText'
            ? () => ({ width: 0 })
            : prop === 'createLinearGradient' || prop === 'createRadialGradient' || prop === 'createPattern'
              ? () => ({ addColorStop() {} })
              : () => undefined,
      set: () => true,
    },
  );
  if (typeof HTMLCanvasElement !== 'undefined') {
    HTMLCanvasElement.prototype.getContext = (() => noop2d) as unknown as HTMLCanvasElement['getContext'];
  }
}
