// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the event-processing service. Its one console-facing
// surface is the DETECT rule compiler validation gate (ADR-051 / ADR-044): the console
// authoring form calls it to show a rule's compile/cost/type diagnostics inline BEFORE the
// profile publish re-validates the frozen snapshot authoritatively. It is the single-homed
// authority for rule structure — device-management stores the rule blob opaquely and never
// parses the taxonomy, so this is where a bad rule is actually caught.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-processing';
import type { RuleHealthQuery, DetectionStreamSubscription } from '@/gql/event-processing/graphql';

const VALIDATE_DETECTION_RULES = graphql(`
  query ValidateDetectionRules($rules: [DetectionRuleInput!]!) {
    validateDetectionRules(rules: $rules) {
      valid
      errors {
        index
        token
        message
      }
    }
  }
`);

// The outcome of validating one rule: ok, or the compiler's console-surfaceable reason.
export interface RuleValidation {
  ok: boolean;
  message: string | null;
}

// validateDetectionRule compiles + cost-gates a single rule definition through the batch
// gate and collapses the result to one diagnostic. `token` anchors the compiler's error
// messages; the definition is the opaque rules.Rule JSON string the form emits. It is pure
// validation (no state is read or written), so it is safe to call repeatedly / debounced.
export async function validateDetectionRule(token: string, definition: string): Promise<RuleValidation> {
  const data = await gql('event-processing', VALIDATE_DETECTION_RULES, {
    rules: [{ token, definition }],
  });
  const result = data.validateDetectionRules;
  if (result.valid) return { ok: true, message: null };
  return { ok: false, message: result.errors[0]?.message ?? 'The rule did not compile.' };
}

// ── Rule health (ADR-051 slice 7b/7c) ─────────────────────────────────────
// Per-rule status / last-fired / fire-count for a profile's ACTIVE published version.

export type RuleHealth = RuleHealthQuery['ruleHealth'][number];

const RULE_HEALTH = graphql(`
  query RuleHealth($profileToken: String!) {
    ruleHealth(profileToken: $profileToken) {
      ruleId
      ruleToken
      name
      status
      lastFiredAt
      fireCount
      lastSignal
      message
    }
  }
`);

export async function listRuleHealth(profileToken: string): Promise<RuleHealth[]> {
  const data = await gql('event-processing', RULE_HEALTH, { profileToken });
  return data.ruleHealth;
}

// ── Live detection feed (ADR-037 / ADR-057 slice 7c) ──────────────────────
// The stream of a profile's DETECT firings (raised/resolved), consumed by
// useDetectionStream. Best-effort delivery: it is a live feed, not a source of
// truth (ruleHealth's fire counts are the durable record) — a consumer treats
// each event as an append to a capped recent-firings list and re-runs ruleHealth
// on reconnect. Exported (unlike the query documents) because the hook passes it
// to the SDK subscribe().
export type DetectionStreamData = DetectionStreamSubscription;
export type DetectionEvent = DetectionStreamSubscription['detectionStream'];

export const DETECTION_STREAM = graphql(`
  subscription DetectionStream($profileToken: String!) {
    detectionStream(profileToken: $profileToken) {
      ruleId
      ruleToken
      kind
      edge
      series
      occurredTime
      severity
      value
    }
  }
`);
