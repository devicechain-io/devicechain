/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/** Filters and paging for the notificationChannels query. */
export type NotificationChannelSearchCriteria = {
  /** Return only channels of this type (smtp or webhook). Omit it to return every type. */
  channelType?: string | null | undefined;
  /** Return only enabled (true) or only disabled (false) channels. Omit it to return both. */
  enabled?: boolean | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Channels per page. A value below 1 is treated as 100; a value above 1000 is capped at 1000. */
  pageSize: number;
};

/** Fields for a new routing policy, including its complete rule set. */
export type NotificationPolicyCreateRequest = {
  /** Free-text description of what the policy is for. */
  description?: string | null | undefined;
  /** Not supported yet: any non-blank value is rejected. Leave it unset to create a policy that applies to every device. */
  deviceTypeToken?: string | null | undefined;
  /** Whether the policy is active. A disabled policy sends nothing. */
  enabled: boolean;
  /** Seconds after an alarm's last notification before an unacknowledged, uncleared alarm is re-sent. Omit it or use 0 for no escalation. */
  escalateAfterSeconds?: number | null | undefined;
  /** Maximum number of re-notifications per alarm. Omit it to use the service-wide default. */
  maxEscalations?: number | null | undefined;
  /** Free-form caller-defined metadata as a JSON object serialized to a string. */
  metadata?: string | null | undefined;
  /** Human-readable name shown in policy lists. */
  name?: string | null | undefined;
  /** The policy's complete rule set; pass an empty list for a policy with no rules yet. */
  rules: Array<NotificationRuleCreateRequest>;
  /** Minimum seconds between notifications about the same alarm. Omit it for no throttle. */
  throttleSeconds?: number | null | undefined;
  /**
   * Unique identifier for the new policy within the tenant. Letters, digits, hyphens and
   * underscores, starting with a letter or digit, at most 128 characters.
   */
  token: string;
};

/** Filters and paging for the notificationPolicies query. */
export type NotificationPolicySearchCriteria = {
  /** Return only policies scoped to this device type. Policies cannot yet be scoped, so any value matches nothing. */
  deviceTypeToken?: string | null | undefined;
  /** Return only enabled (true) or only disabled (false) policies. Omit it to return both. */
  enabled?: boolean | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Policies per page. A value below 1 is treated as 100; a value above 1000 is capped at 1000. */
  pageSize: number;
};

/**
 * A routing rule supplied when creating or updating a policy. The channel is referred to
 * by token; a token that matches no channel fails the whole write.
 */
export type NotificationRuleCreateRequest = {
  /** Token of the channel to send through. */
  channelToken: string;
  /**
   * The recipients as a JSON array of strings serialized to a string. For an smtp channel
   * these are email addresses; a webhook rule needs none.
   */
  recipients?: string | null | undefined;
  /**
   * The alarm severity to match: CRITICAL, MAJOR, MINOR, WARNING or INDETERMINATE, or * for
   * any. The match is exact and case-sensitive, so lowercase values are rejected.
   */
  severity: string;
};

export type NotificationPoliciesQueryVariables = Exact<{
  criteria: NotificationPolicySearchCriteria;
}>;


export type NotificationPoliciesQuery = { notificationPolicies: { results: Array<{ token: string, name: string | null, enabled: boolean, deviceTypeToken: string | null, rules: Array<{ severity: string, recipients: string | null, channel: { token: string, enabled: boolean, channelType: string } | null }> }>, pagination: { totalRecords: number | null } } };

export type NotificationChannelsQueryVariables = Exact<{
  criteria: NotificationChannelSearchCriteria;
}>;


export type NotificationChannelsQuery = { notificationChannels: { results: Array<{ token: string, name: string | null, channelType: string, enabled: boolean }> } };

export type CreateNotificationPolicyMutationVariables = Exact<{
  request: NotificationPolicyCreateRequest;
}>;


export type CreateNotificationPolicyMutation = { createNotificationPolicy: { token: string } };

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

export const NotificationPoliciesDocument = new TypedDocumentString(`
    query NotificationPolicies($criteria: NotificationPolicySearchCriteria!) {
  notificationPolicies(criteria: $criteria) {
    results {
      token
      name
      enabled
      deviceTypeToken
      rules {
        severity
        recipients
        channel {
          token
          enabled
          channelType
        }
      }
    }
    pagination {
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<NotificationPoliciesQuery, NotificationPoliciesQueryVariables>;
export const NotificationChannelsDocument = new TypedDocumentString(`
    query NotificationChannels($criteria: NotificationChannelSearchCriteria!) {
  notificationChannels(criteria: $criteria) {
    results {
      token
      name
      channelType
      enabled
    }
  }
}
    `) as unknown as TypedDocumentString<NotificationChannelsQuery, NotificationChannelsQueryVariables>;
export const CreateNotificationPolicyDocument = new TypedDocumentString(`
    mutation CreateNotificationPolicy($request: NotificationPolicyCreateRequest!) {
  createNotificationPolicy(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateNotificationPolicyMutation, CreateNotificationPolicyMutationVariables>;