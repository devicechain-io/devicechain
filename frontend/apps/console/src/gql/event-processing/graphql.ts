/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DetectionRuleInput = {
  definition: string;
  groupScoped?: boolean | null | undefined;
  token: string;
};

export type PreviewRuleInput = {
  end: string;
  graph?: string | null | undefined;
  profileToken: string;
  ruleDefinition?: string | null | undefined;
  start: string;
  trace?: boolean | null | undefined;
};

export type RuleStatus =
  | 'ACTIVE'
  | 'COMPILE_ERROR';

export type ValidateDetectionRulesQueryVariables = Exact<{
  rules: Array<DetectionRuleInput> | DetectionRuleInput;
}>;


export type ValidateDetectionRulesQuery = { validateDetectionRules: { valid: boolean, errors: Array<{ index: number, token: string, message: string }> } };

export type CompileCanvasQueryVariables = Exact<{
  graph: string;
  profileToken: string;
}>;


export type CompileCanvasQuery = { compileCanvas: { ok: boolean, definition: string | null, estimatedCost: number | null, diagnostics: Array<{ nodeId: string | null, severity: string, message: string }> } };

export type PreviewRuleQueryVariables = Exact<{
  input: PreviewRuleInput;
}>;


export type PreviewRuleQuery = { previewRule: { ok: boolean, degraded: string | null, firings: Array<{ occurredAt: string, series: string, signal: string, trace: Array<{ nodeId: string, kind: string, disposition: string, detail: string | null }> }>, stats: { eventsScanned: number, firingCount: number, evalErrors: number, wallMs: number }, diagnostics: Array<{ nodeId: string | null, severity: string, message: string }> } };

export type RuleHealthQueryVariables = Exact<{
  profileToken: string;
}>;


export type RuleHealthQuery = { ruleHealth: Array<{ ruleId: string, ruleToken: string, name: string, status: RuleStatus, lastFiredAt: string | null, fireCount: number, lastSignal: string | null, message: string | null }> };

export type DetectionStreamSubscriptionVariables = Exact<{
  profileToken: string;
}>;


export type DetectionStreamSubscription = { detectionStream: { ruleId: string, ruleToken: string, kind: string, edge: string, series: string, occurredTime: string, severity: string | null, value: number | null } };

export class TypedDocumentString<TResult, TVariables>
  extends String
  implements DocumentTypeDecoration<TResult, TVariables>
{
  __apiType?: NonNullable<DocumentTypeDecoration<TResult, TVariables>['__apiType']>;
  private value: string;
  public __meta__?: Record<string, any> | undefined;

  constructor(value: string, __meta__?: Record<string, any> | undefined) {
    super(value);
    this.value = value;
    this.__meta__ = __meta__;
  }

  override toString(): string & DocumentTypeDecoration<TResult, TVariables> {
    return this.value;
  }
}

export const ValidateDetectionRulesDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<ValidateDetectionRulesQuery, ValidateDetectionRulesQueryVariables>;
export const CompileCanvasDocument = new TypedDocumentString(`
    query CompileCanvas($graph: String!, $profileToken: String!) {
  compileCanvas(graph: $graph, profileToken: $profileToken) {
    ok
    definition
    estimatedCost
    diagnostics {
      nodeId
      severity
      message
    }
  }
}
    `) as unknown as TypedDocumentString<CompileCanvasQuery, CompileCanvasQueryVariables>;
export const PreviewRuleDocument = new TypedDocumentString(`
    query PreviewRule($input: PreviewRuleInput!) {
  previewRule(input: $input) {
    ok
    firings {
      occurredAt
      series
      signal
      trace {
        nodeId
        kind
        disposition
        detail
      }
    }
    stats {
      eventsScanned
      firingCount
      evalErrors
      wallMs
    }
    degraded
    diagnostics {
      nodeId
      severity
      message
    }
  }
}
    `) as unknown as TypedDocumentString<PreviewRuleQuery, PreviewRuleQueryVariables>;
export const RuleHealthDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<RuleHealthQuery, RuleHealthQueryVariables>;
export const DetectionStreamDocument = new TypedDocumentString(`
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
    `) as unknown as TypedDocumentString<DetectionStreamSubscription, DetectionStreamSubscriptionVariables>;