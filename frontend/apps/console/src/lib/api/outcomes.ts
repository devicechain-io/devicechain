// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// EXPERIMENT: outcome objects. A typed read of event-management's evaluateOutcome, which is
// off unless the instance enables it (the query is then refused as UNAVAILABLE).
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/event-management';
import type { OutcomeDefinitionInput, EvaluateOutcomeQuery } from '@/gql/event-management/graphql';

export type { OutcomeDefinitionInput };
export type OutcomeEvaluation = EvaluateOutcomeQuery['evaluateOutcome'];
export type OutcomeAreaStatus = OutcomeEvaluation['areas'][number];

const EVALUATE_OUTCOME = graphql(`
  query EvaluateOutcome($definition: OutcomeDefinitionInput!) {
    evaluateOutcome(definition: $definition) {
      token
      status
      objectiveStatus
      strayBuckets
      span {
        start
        end
        intervalSeconds
        bucketCount
      }
      areas {
        area
        status
        goodBuckets
        badBuckets
        emptyBuckets
        totalBuckets
        compliance
        coverage
        budgetRemaining
      }
      equity {
        evaluatedAreas
        excludedAreas
        spreadUndefined
        complete
        worst
        worstArea
        median
        spread
        pooled
        breached
      }
    }
  }
`);

export async function evaluateOutcome(definition: OutcomeDefinitionInput): Promise<OutcomeEvaluation> {
  return (await gql('event-management', EVALUATE_OUTCOME, { definition })).evaluateOutcome;
}
