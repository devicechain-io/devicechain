/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** One detection rule submitted for validation. */
export type DetectionRuleInput = {
  /** The rule definition, as a JSON document serialized to a string. */
  definition: string;
  /**
   * Whether the rule is scoped to a device group. Some rule kinds (absence and correlation)
   * cannot be group-scoped and are rejected if this is true. Defaults to false.
   */
  groupScoped?: boolean | null | undefined;
  /** The rule's token, used to name the rule in errors and warnings. */
  token: string;
};

/** Input to draftDetectionRuleFromText. */
export type DraftRuleFromTextInput = {
  /**
   * Optional metric vocabulary of the target profile, so the model refers to real metric keys.
   * Omit it and the model works from the description alone.
   */
  metrics?: Array<MetricHintInput> | null | undefined;
  /** Token of the target device profile. */
  profileToken: string;
  /** The author's plain-language description of the rule they want. */
  text: string;
};

/**
 * One metric of the target profile's vocabulary, offered to the model as context. Only key is
 * required.
 */
export type MetricHintInput = {
  /** The metric's data type, as a hint. */
  dataType?: string | null | undefined;
  /** What the metric measures, as a hint. */
  description?: string | null | undefined;
  /** The metric's key. */
  key: string;
  /** The metric's unit, as a hint. */
  unit?: string | null | undefined;
};

/** Input to previewRule. Exactly one of graph and ruleDefinition names the draft. */
export type PreviewRuleInput = {
  /**
   * End of the replay window by event occurred time, as an RFC 3339 timestamp. Must be after
   * start. A window longer than 24 hours is shortened to the 24 hours ending at end, and the
   * result says so in degraded.
   */
  end: string;
  /** A canvas definition, as the JSON document the console editor emits, serialized to a string. */
  graph?: string | null | undefined;
  /** Token of the device profile whose published history the draft is replayed against. */
  profileToken: string;
  /** A rule definition, as a JSON document serialized to a string (the form builder's output). */
  ruleDefinition?: string | null | undefined;
  /** Start of the replay window by event occurred time, as an RFC 3339 timestamp. */
  start: string;
  /**
   * When true and the draft is a canvas graph, each firing carries a per-node trace of what
   * every canvas node did for it. Off by default; ignored for a ruleDefinition draft.
   */
  trace?: boolean | null | undefined;
};

/** The status of a live detection rule. */
export type RuleStatus =
  /** The rule compiles under the current limits and is running. */
  | 'ACTIVE'
  /**
   * The published rule no longer compiles under the current limits (for example after a maximum
   * rule duration was lowered). It is surfaced so its author can re-author it. A rule that
   * failed to compile at publish time never reaches this state, because publishing is refused.
   */
  | 'COMPILE_ERROR';

export type ValidateDetectionRulesQueryVariables = Exact<{
  rules: Array<DetectionRuleInput> | DetectionRuleInput;
}>;


export type ValidateDetectionRulesQuery = { validateDetectionRules: { valid: boolean, errors: Array<{ index: number, token: string, message: string }>, warnings: Array<{ code: string, params: Array<string>, message: string }> } };

export type CompileCanvasQueryVariables = Exact<{
  graph: string;
  profileToken: string;
}>;


export type CompileCanvasQuery = { compileCanvas: { ok: boolean, definition: string | null, estimatedCost: number | null, diagnostics: Array<{ nodeId: string | null, severity: string, message: string, code: string | null, params: Array<string> }> } };

export type DraftDetectionRuleFromTextMutationVariables = Exact<{
  input: DraftRuleFromTextInput;
}>;


export type DraftDetectionRuleFromTextMutation = { draftDetectionRuleFromText: { ok: boolean, definition: string | null, estimatedCost: number | null, model: string | null, provider: string | null, attempts: number, rawCandidate: string | null, unavailable: boolean, unavailableReason: string | null, diagnostics: Array<{ field: string | null, message: string }>, warnings: Array<{ field: string | null, message: string, code: string | null, params: Array<string> }> } };

export type PreviewRuleQueryVariables = Exact<{
  input: PreviewRuleInput;
}>;


export type PreviewRuleQuery = { previewRule: { ok: boolean, degraded: string | null, firings: Array<{ occurredAt: string, series: string, signal: string, trace: Array<{ nodeId: string, kind: string, disposition: string, detail: string | null }> }>, stats: { eventsScanned: number, firingCount: number, evalErrors: number, wallMs: number }, diagnostics: Array<{ nodeId: string | null, severity: string, message: string, code: string | null, params: Array<string> }> } };

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
    warnings {
      code
      params
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
      code
      params
    }
  }
}
    `) as unknown as TypedDocumentString<CompileCanvasQuery, CompileCanvasQueryVariables>;
export const DraftDetectionRuleFromTextDocument = new TypedDocumentString(`
    mutation DraftDetectionRuleFromText($input: DraftRuleFromTextInput!) {
  draftDetectionRuleFromText(input: $input) {
    ok
    definition
    estimatedCost
    model
    provider
    attempts
    rawCandidate
    diagnostics {
      field
      message
    }
    warnings {
      field
      message
      code
      params
    }
    unavailable
    unavailableReason
  }
}
    `) as unknown as TypedDocumentString<DraftDetectionRuleFromTextMutation, DraftDetectionRuleFromTextMutationVariables>;
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
      code
      params
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