/* eslint-disable */
/** Internal type. DO NOT USE DIRECTLY. */
type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
/** Internal type. DO NOT USE DIRECTLY. */
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
import { DocumentTypeDecoration } from '@graphql-typed-document-node/core';
/**
 * Fields for a new delivery channel. The type must be one listed by
 * notificationChannelTypes and the config must be valid for it.
 */
export type NotificationChannelCreateRequest = {
  /** The channel type: smtp, webhook, slack, teams or pagerduty, as listed by notificationChannelTypes. */
  channelType: string;
  /**
   * The non-secret connection settings as a JSON object serialized to a string. For smtp:
   * host, port, from, username (optional; requires a secret) and security (starttls, the
   * default; tls; or none for cleartext). For webhook: url (http or https, required),
   * method (POST only), headers (an object of extra request headers) and auth (required:
   * none when the URL itself carries the credential, bearer to send the secret as a bearer
   * token, or header to send it in the header named by authHeader, optionally prefixed
   * with authScheme). A webhook with auth bearer or header must have a secret, and one
   * with auth none must not. For slack and teams: optional linkBaseUrl (the console URL the
   * message's "open alarms" link is built from). For pagerduty: optional region (us, the
   * default, or eu), source and linkBaseUrl. Unknown keys are refused for these three. The
   * platform restricts which destinations a channel may reach.
   */
  config?: string | null | undefined;
  /** Free-text description of what the channel is for. */
  description?: string | null | undefined;
  /** Whether the channel is active. Notifications are not sent through a disabled channel. */
  enabled: boolean;
  /** Free-form caller-defined metadata as a JSON object serialized to a string. */
  metadata?: string | null | undefined;
  /** Human-readable name shown in channel lists. */
  name?: string | null | undefined;
  /**
   * The delivery credential: the SMTP password, the webhook bearer or header value, the
   * Slack or Teams webhook URL (https), or the PagerDuty Events API v2 routing key. A slack,
   * teams or pagerduty channel requires one.
   * Write-only; stored encrypted and never returned. Omit it, or send an empty string, for
   * no secret.
   */
  secret?: string | null | undefined;
  /**
   * Unique identifier for the new channel within the tenant. Letters, digits, hyphens and
   * underscores, starting with a letter or digit, at most 128 characters.
   */
  token: string;
};

/** Filters and paging for the notificationChannels query. */
export type NotificationChannelSearchCriteria = {
  /** Return only channels of this type (smtp, webhook, slack, teams or pagerduty). Omit it to return every type. */
  channelType?: string | null | undefined;
  /** Return only enabled (true) or only disabled (false) channels. Omit it to return both. */
  enabled?: boolean | null | undefined;
  /** Page to return, starting at 1. A value below 1 is treated as 1. */
  pageNumber: number;
  /** Channels per page. A value below 1 is treated as 100; a value above 1000 is capped at 1000. */
  pageSize: number;
};

/**
 * A partial update to a delivery channel. Omit a field to leave the stored value alone,
 * send a value to set it, or send an explicit null to clear it (except where noted). The
 * channel is named by the mutation's token argument, so there is no token here; use
 * renameNotificationChannel to change the token.
 */
export type NotificationChannelUpdateRequest = {
  /** New channel type (smtp, webhook, slack, teams or pagerduty). Omit it to keep the stored type; an explicit null is refused. */
  channelType?: string | null | undefined;
  /**
   * New connection settings as a JSON object serialized to a string, in the format
   * described on NotificationChannelCreateRequest.config, or null to clear them. For a
   * webhook channel the result must still be valid, including its auth setting.
   */
  config?: string | null | undefined;
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** Whether the channel is active. Omit it to keep the stored value; an explicit null is refused. */
  enabled?: boolean | null | undefined;
  /** New metadata as a JSON object serialized to a string, or null to clear it. */
  metadata?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
  /**
   * The write-only delivery credential. Omit it to keep the stored secret, send a value to
   * replace it, or send null or an empty string to delete it. A webhook channel whose auth
   * is bearer or header cannot be left without a secret, and one whose auth is none cannot
   * have one.
   */
  secret?: string | null | undefined;
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
  /** Also notify the policy's channels when an alarm it paged clears. Omit it for false, the behaviour before this option existed. */
  notifyOnClear?: boolean | null | undefined;
  /**
   * The severity at or above which alarms are sent even during quiet hours. Omit it to hold
   * every severity. It has no effect while the policy has no quiet hours.
   */
  quietHoursBypassSeverity?: string | null | undefined;
  /**
   * Quiet hours as a JSON array of weekly slots serialized to a string, each
   * {day, start, durationMinutes}. Set it together with quietHoursTimezone. Slots may not
   * overlap, and together they may not cover the whole week.
   */
  quietHoursSlots?: string | null | undefined;
  /**
   * IANA time zone for the quiet-hours slots, such as Europe/Berlin. Set it together with
   * quietHoursSlots, or omit both for no quiet hours. Local and unknown zone names are rejected.
   */
  quietHoursTimezone?: string | null | undefined;
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
 * A partial update to a routing policy. Omit a field to leave the stored value alone, send
 * a value to set it, or send an explicit null to clear it (except where noted). The policy
 * is named by the mutation's token argument, so there is no token here, and a policy's
 * token cannot be changed.
 */
export type NotificationPolicyUpdateRequest = {
  /** New description, or null to clear it. */
  description?: string | null | undefined;
  /** Whether the policy is active. Omit it to keep the stored value; an explicit null is refused. */
  enabled?: boolean | null | undefined;
  /** New escalation window in seconds, or null to disable escalation. */
  escalateAfterSeconds?: number | null | undefined;
  /** New maximum number of re-notifications per alarm, or null to use the service-wide default. */
  maxEscalations?: number | null | undefined;
  /** New metadata as a JSON object serialized to a string, or null to clear it. */
  metadata?: string | null | undefined;
  /** New name, or null to clear it. */
  name?: string | null | undefined;
  /** Whether the policy also notifies on clear. Omit it to keep the stored value; an explicit null is refused. */
  notifyOnClear?: boolean | null | undefined;
  /** New bypass severity, or null so that quiet hours hold every severity. */
  quietHoursBypassSeverity?: string | null | undefined;
  /** New quiet-hours slots (a JSON array serialized to a string), or null to clear them. */
  quietHoursSlots?: string | null | undefined;
  /**
   * New quiet-hours time zone, or null to clear it. The quiet-hours fields are checked
   * together after the update is applied, so to remove quiet hours clear quietHoursTimezone
   * and quietHoursSlots in the same request. A stored quietHoursBypassSeverity is kept, and
   * has no effect while the policy has no quiet hours. An update that names none of the
   * three leaves the stored quiet hours exactly as they are, without checking them.
   */
  quietHoursTimezone?: string | null | undefined;
  /**
   * The policy's complete rule set. Omit it to leave the stored rules untouched. Send a
   * list to replace all the rules with it, or send null or an empty list to remove all
   * rules. Rules are replaced wholesale, never merged, and each rule gets a new id.
   */
  rules?: Array<NotificationRuleCreateRequest> | null | undefined;
  /** New minimum seconds between notifications about the same alarm, or null to remove the throttle. */
  throttleSeconds?: number | null | undefined;
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

export type NotificationPolicyRegistryQueryVariables = Exact<{
  criteria: NotificationPolicySearchCriteria;
}>;


export type NotificationPolicyRegistryQuery = { notificationPolicies: { results: Array<{ id: string, token: string, name: string | null, description: string | null, enabled: boolean, notifyOnClear: boolean, throttleSeconds: number | null, escalateAfterSeconds: number | null, maxEscalations: number | null, metadata: string | null, quietHoursTimezone: string | null, quietHoursSlots: string | null, quietHoursBypassSeverity: string | null, createdAt: string | null, updatedAt: string | null, rules: Array<{ id: string, severity: string, recipients: string | null, channel: { token: string, name: string | null } | null }> }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type NotificationPolicyByTokenQueryVariables = Exact<{
  tokens: Array<string> | string;
}>;


export type NotificationPolicyByTokenQuery = { notificationPoliciesByToken: Array<{ id: string, token: string, name: string | null, description: string | null, enabled: boolean, notifyOnClear: boolean, throttleSeconds: number | null, escalateAfterSeconds: number | null, maxEscalations: number | null, metadata: string | null, quietHoursTimezone: string | null, quietHoursSlots: string | null, quietHoursBypassSeverity: string | null, createdAt: string | null, updatedAt: string | null, rules: Array<{ id: string, severity: string, recipients: string | null, channel: { token: string, name: string | null } | null }> }> };

export type CreateNotificationPolicyRecordMutationVariables = Exact<{
  request: NotificationPolicyCreateRequest;
}>;


export type CreateNotificationPolicyRecordMutation = { createNotificationPolicy: { token: string, updatedAt: string | null } };

export type UpdateNotificationPolicyMutationVariables = Exact<{
  token: string;
  request: NotificationPolicyUpdateRequest;
  expectedUpdatedAt?: string | null | undefined;
}>;


export type UpdateNotificationPolicyMutation = { updateNotificationPolicy: { token: string, updatedAt: string | null } };

export type DeleteNotificationPolicyMutationVariables = Exact<{
  token: string;
}>;


export type DeleteNotificationPolicyMutation = { deleteNotificationPolicy: boolean };

export type NotificationChannelRegistryQueryVariables = Exact<{
  criteria: NotificationChannelSearchCriteria;
}>;


export type NotificationChannelRegistryQuery = { notificationChannels: { results: Array<{ id: string, token: string, name: string | null, description: string | null, channelType: string, config: string | null, hasSecret: boolean, enabled: boolean, updatedAt: string | null }>, pagination: { pageStart: number | null, pageEnd: number | null, totalRecords: number | null } } };

export type NotificationChannelTypesQueryVariables = Exact<{ [key: string]: never; }>;


export type NotificationChannelTypesQuery = { notificationChannelTypes: Array<{ id: string, label: string, description: string, available: boolean }> };

export type CreateNotificationChannelMutationVariables = Exact<{
  request: NotificationChannelCreateRequest;
}>;


export type CreateNotificationChannelMutation = { createNotificationChannel: { token: string } };

export type UpdateNotificationChannelMutationVariables = Exact<{
  token: string;
  request: NotificationChannelUpdateRequest;
}>;


export type UpdateNotificationChannelMutation = { updateNotificationChannel: { token: string } };

export type DeleteNotificationChannelMutationVariables = Exact<{
  token: string;
}>;


export type DeleteNotificationChannelMutation = { deleteNotificationChannel: boolean };

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
export const NotificationPolicyRegistryDocument = new TypedDocumentString(`
    query NotificationPolicyRegistry($criteria: NotificationPolicySearchCriteria!) {
  notificationPolicies(criteria: $criteria) {
    results {
      id
      token
      name
      description
      enabled
      notifyOnClear
      throttleSeconds
      escalateAfterSeconds
      maxEscalations
      metadata
      quietHoursTimezone
      quietHoursSlots
      quietHoursBypassSeverity
      createdAt
      updatedAt
      rules {
        id
        severity
        recipients
        channel {
          token
          name
        }
      }
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<NotificationPolicyRegistryQuery, NotificationPolicyRegistryQueryVariables>;
export const NotificationPolicyByTokenDocument = new TypedDocumentString(`
    query NotificationPolicyByToken($tokens: [String!]!) {
  notificationPoliciesByToken(tokens: $tokens) {
    id
    token
    name
    description
    enabled
    notifyOnClear
    throttleSeconds
    escalateAfterSeconds
    maxEscalations
    metadata
    quietHoursTimezone
    quietHoursSlots
    quietHoursBypassSeverity
    createdAt
    updatedAt
    rules {
      id
      severity
      recipients
      channel {
        token
        name
      }
    }
  }
}
    `) as unknown as TypedDocumentString<NotificationPolicyByTokenQuery, NotificationPolicyByTokenQueryVariables>;
export const CreateNotificationPolicyRecordDocument = new TypedDocumentString(`
    mutation CreateNotificationPolicyRecord($request: NotificationPolicyCreateRequest!) {
  createNotificationPolicy(request: $request) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<CreateNotificationPolicyRecordMutation, CreateNotificationPolicyRecordMutationVariables>;
export const UpdateNotificationPolicyDocument = new TypedDocumentString(`
    mutation UpdateNotificationPolicy($token: String!, $request: NotificationPolicyUpdateRequest!, $expectedUpdatedAt: String) {
  updateNotificationPolicy(
    token: $token
    request: $request
    expectedUpdatedAt: $expectedUpdatedAt
  ) {
    token
    updatedAt
  }
}
    `) as unknown as TypedDocumentString<UpdateNotificationPolicyMutation, UpdateNotificationPolicyMutationVariables>;
export const DeleteNotificationPolicyDocument = new TypedDocumentString(`
    mutation DeleteNotificationPolicy($token: String!) {
  deleteNotificationPolicy(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteNotificationPolicyMutation, DeleteNotificationPolicyMutationVariables>;
export const NotificationChannelRegistryDocument = new TypedDocumentString(`
    query NotificationChannelRegistry($criteria: NotificationChannelSearchCriteria!) {
  notificationChannels(criteria: $criteria) {
    results {
      id
      token
      name
      description
      channelType
      config
      hasSecret
      enabled
      updatedAt
    }
    pagination {
      pageStart
      pageEnd
      totalRecords
    }
  }
}
    `) as unknown as TypedDocumentString<NotificationChannelRegistryQuery, NotificationChannelRegistryQueryVariables>;
export const NotificationChannelTypesDocument = new TypedDocumentString(`
    query NotificationChannelTypes {
  notificationChannelTypes {
    id
    label
    description
    available
  }
}
    `) as unknown as TypedDocumentString<NotificationChannelTypesQuery, NotificationChannelTypesQueryVariables>;
export const CreateNotificationChannelDocument = new TypedDocumentString(`
    mutation CreateNotificationChannel($request: NotificationChannelCreateRequest!) {
  createNotificationChannel(request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<CreateNotificationChannelMutation, CreateNotificationChannelMutationVariables>;
export const UpdateNotificationChannelDocument = new TypedDocumentString(`
    mutation UpdateNotificationChannel($token: String!, $request: NotificationChannelUpdateRequest!) {
  updateNotificationChannel(token: $token, request: $request) {
    token
  }
}
    `) as unknown as TypedDocumentString<UpdateNotificationChannelMutation, UpdateNotificationChannelMutationVariables>;
export const DeleteNotificationChannelDocument = new TypedDocumentString(`
    mutation DeleteNotificationChannel($token: String!) {
  deleteNotificationChannel(token: $token)
}
    `) as unknown as TypedDocumentString<DeleteNotificationChannelMutation, DeleteNotificationChannelMutationVariables>;