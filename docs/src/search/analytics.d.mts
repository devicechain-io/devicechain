// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

export const MAX_QUERY_LENGTH: number;
export const DEFAULT_DEBOUNCE_MS: number;
export function looksSensitive(raw: unknown): boolean;
export function sanitizeQuery(
  raw: unknown,
): null | { redacted: true } | { redacted: false; query: string };
export interface PostHogLike {
  capture: (event: string, props?: Record<string, unknown>) => void;
  __loaded?: boolean;
  has_opted_out_capturing?: () => boolean;
}
export function activePostHog(getPostHog: () => PostHogLike | undefined | null): PostHogLike | null;
export interface SearchTracker {
  input(value: string): void;
  select(rank: number): void;
  navigated(url: string): void;
  pageChanged(): void;
  dispose(): void;
}
export function createSearchTracker(options: {
  getPostHog: () => PostHogLike | undefined | null;
  getLocale: () => string;
  getPagePath: () => string;
  getResultCount: () => number | null;
  debounceMs?: number;
  maxResultRetries?: number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}): SearchTracker;
