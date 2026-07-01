/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
export type CommandCreateRequest = {
  deviceToken: string;
  expiresAt?: string | null | undefined;
  metadata?: string | null | undefined;
  name: string;
  payload?: string | null | undefined;
  token: string;
};

export type CommandSearchCriteria = {
  deviceToken?: string | null | undefined;
  pageNumber: number;
  pageSize: number;
  status?: string | null | undefined;
};

export type CommandsQueryVariables = Exact<{
  criteria: CommandSearchCriteria;
}>;


export type CommandsQuery = { commands: { results: Array<{ id: string, token: string, deviceToken: string, name: string, payload: string | null, status: string, queuedTime: string | null, sentTime: string | null, deliveredTime: string | null, respondedTime: string | null, expiresAt: string | null, responsePayload: string | null, error: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type CreateCommandMutationVariables = Exact<{
  request: CommandCreateRequest;
}>;


export type CreateCommandMutation = { createCommand: { id: string, token: string, status: string } };

export type CancelCommandMutationVariables = Exact<{
  token: string;
}>;


export type CancelCommandMutation = { cancelCommand: { id: string, token: string, status: string } };

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

export const CommandsDocument = new TypedDocumentString(`
    query Commands($criteria: CommandSearchCriteria!) {
  commands(criteria: $criteria) {
    results {
      id
      token
      deviceToken
      name
      payload
      status
      queuedTime
      sentTime
      deliveredTime
      respondedTime
      expiresAt
      responsePayload
      error
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<CommandsQuery, CommandsQueryVariables>;
export const CreateCommandDocument = new TypedDocumentString(`
    mutation CreateCommand($request: CommandCreateRequest!) {
  createCommand(request: $request) {
    id
    token
    status
  }
}
    `) as unknown as TypedDocumentString<CreateCommandMutation, CreateCommandMutationVariables>;
export const CancelCommandDocument = new TypedDocumentString(`
    mutation CancelCommand($token: String!) {
  cancelCommand(token: $token) {
    id
    token
    status
  }
}
    `) as unknown as TypedDocumentString<CancelCommandMutation, CancelCommandMutationVariables>;