// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { consumeStepUpReturn, setStepUpReturn } from './stepUpReturn';

beforeEach(() => { sessionStorage.clear(); vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-10T12:00:00Z')); });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });
describe('step-up return path', () => {
  it('returns a local path exactly once', () => {
    setStepUpReturn('/approvals/proposal-17');
    expect(consumeStepUpReturn()).toBe('/approvals/proposal-17');
    expect(consumeStepUpReturn()).toBeNull();
  });
  it.each(['//evil.com', 'https://x', 'approvals/p', '/\\evil.com', '/path/https://x', '/\n/evil.com', ''])('rejects unsafe path %j', path => {
    setStepUpReturn(path);
    expect(consumeStepUpReturn()).toBeNull();
  });
  it('expires after fifteen minutes', () => {
    setStepUpReturn('/approvals');
    vi.advanceTimersByTime(15 * 60 * 1000);
    expect(consumeStepUpReturn()).toBeNull();
  });
  it('still returns a path just before expiry', () => {
    setStepUpReturn('/approvals');
    vi.advanceTimersByTime(15 * 60 * 1000 - 1);
    expect(consumeStepUpReturn()).toBe('/approvals');
  });
  it('validates stored data and removes malformed entries', () => {
    sessionStorage.setItem('dc.stepUpReturn', JSON.stringify({ path: '//evil.com', at: Date.now() }));
    expect(consumeStepUpReturn()).toBeNull();
    expect(sessionStorage.getItem('dc.stepUpReturn')).toBeNull();
    sessionStorage.setItem('dc.stepUpReturn', '{');
    expect(consumeStepUpReturn()).toBeNull();
  });
  it('continues when session storage is blocked', () => {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => { throw new Error('blocked'); });
    expect(() => setStepUpReturn('/approvals')).not.toThrow();
    expect(consumeStepUpReturn()).toBeNull();
  });
});
