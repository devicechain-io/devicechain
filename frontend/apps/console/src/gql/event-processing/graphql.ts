/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type DetectionRuleInput = {
  definition: string;
  token: string;
};

export type ValidateDetectionRulesQueryVariables = Exact<{
  rules: Array<DetectionRuleInput> | DetectionRuleInput;
}>;


export type ValidateDetectionRulesQuery = { validateDetectionRules: { valid: boolean, errors: Array<{ index: number, token: string, message: string }> } };

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