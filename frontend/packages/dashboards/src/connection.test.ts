// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ConnectionTracker, OFFLINE_AFTER_MS } from './connection';

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('ConnectionTracker', () => {
  it('starts live, goes reconnecting on a close, and live again on a connect', () => {
    const tracker = new ConnectionTracker();
    const handle = tracker.register();
    expect(tracker.state).toBe('live');
    handle.closed();
    expect(tracker.state).toBe('reconnecting');
    handle.connected();
    expect(tracker.state).toBe('live');
  });

  it('escalates to offline once a socket has been down past the threshold, and recovers', () => {
    const tracker = new ConnectionTracker();
    const handle = tracker.register();
    handle.closed();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - 1);
    expect(tracker.state).toBe('reconnecting');
    vi.advanceTimersByTime(1);
    expect(tracker.state).toBe('offline');
    handle.connected();
    expect(tracker.state).toBe('live');
  });

  it('does not escalate a socket that came back in time', () => {
    const tracker = new ConnectionTracker();
    const handle = tracker.register();
    handle.closed();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS - 1);
    handle.connected();
    vi.advanceTimersByTime(OFFLINE_AFTER_MS * 2);
    expect(tracker.state).toBe('live');
  });

  it('stays reconnecting while ANY subscription is down, and ignores a released one', () => {
    const tracker = new ConnectionTracker();
    const a = tracker.register();
    const b = tracker.register();
    a.closed();
    b.closed();
    a.connected();
    expect(tracker.state).toBe('reconnecting');
    b.release();
    expect(tracker.state).toBe('live');
  });

  it('notifies listeners on a change and not on a repeat', () => {
    const tracker = new ConnectionTracker();
    const listener = vi.fn();
    const off = tracker.subscribe(listener);
    const handle = tracker.register();
    handle.closed();
    handle.closed();
    expect(listener).toHaveBeenCalledTimes(1);
    off();
    handle.connected();
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it('reads the browser going offline as offline at once, and back online as recovered', () => {
    const tracker = new ConnectionTracker();
    tracker.subscribe(() => {});
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false });
    window.dispatchEvent(new Event('offline'));
    expect(tracker.state).toBe('offline');
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true });
    window.dispatchEvent(new Event('online'));
    expect(tracker.state).toBe('live');
  });
});
