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
