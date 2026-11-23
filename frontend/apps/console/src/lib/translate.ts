// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The shape of a translate function that plain (non-React) helpers accept.
//
// A helper that takes `t` cannot call useTranslation itself, and the `t` it is handed is scoped
// to whichever namespaces the caller loaded (`TFunction<'devices'>`, `TFunction<'connectors'>`).
// i18next 27 types `t` as a union of argument tuples, so a plain
// `(key: string, options?: Record<string, unknown>) => string` is no longer a supertype of any
// of them. Two overloads - one with no options, one with an options object - are: each is
// matched by a tuple the real `t` accepts. A test double or a hand-written default (roundtrip.ts)
// satisfies it just as well.
export interface TranslateFn {
  (key: string): string;
  (key: string, options: Record<string, unknown>): string;
}
