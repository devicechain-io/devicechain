// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Typed GraphQL operations against the notification-management service. The console touches only
// what the detection-rule form needs to close the alarm → human last mile: read the tenant's
// routing policies (to tell whether an alarm severity reaches anyone), list the delivery channels
// (to pick one), and create a policy. Policies route by SEVERITY ONLY — they cannot be limited to
// one rule or one device type — so everything here speaks in severities.
import { gql } from '@devicechain/client';
import { graphql } from '@/gql/notification-management';
import type {
  NotificationPoliciesQuery,
  NotificationChannelsQuery,
  NotificationPolicyRegistryQuery,
  NotificationPolicyCreateRequest,
  NotificationPolicyUpdateRequest,
} from '@/gql/notification-management/graphql';

export type NotificationPolicy = NotificationPoliciesQuery['notificationPolicies']['results'][number];
export type { NotificationPolicyCreateRequest, NotificationPolicyUpdateRequest };
/** A policy as the registry reads it: the coverage fields plus everything the editor needs. */
export type NotificationPolicyRecord =
  NotificationPolicyRegistryQuery['notificationPolicies']['results'][number];
export type NotificationPolicySearchResults = NotificationPolicyRegistryQuery['notificationPolicies'];
export type NotificationChannel = NotificationChannelsQuery['notificationChannels']['results'][number];

const NOTIFICATION_POLICIES = graphql(`
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
`);

/** Every policy of the tenant (one large page: a tenant has a handful). */
export async function listNotificationPolicies(): Promise<NotificationPolicy[]> {
  const data = await gql('notification-management', NOTIFICATION_POLICIES, {
    criteria: { pageNumber: 1, pageSize: 1000 },
  });
  return data.notificationPolicies.results;
}

const NOTIFICATION_CHANNELS = graphql(`
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
`);

/** The tenant's enabled delivery channels. */
export async function listNotificationChannels(): Promise<NotificationChannel[]> {
  const data = await gql('notification-management', NOTIFICATION_CHANNELS, {
    criteria: { pageNumber: 1, pageSize: 1000, enabled: true },
  });
  return data.notificationChannels.results;
}

const CREATE_NOTIFICATION_POLICY = graphql(`
  mutation CreateNotificationPolicy($request: NotificationPolicyCreateRequest!) {
    createNotificationPolicy(request: $request) {
      token
    }
  }
`);

/**
 * Whether an enabled policy sends the given detection-rule severity (lowercase, as a rule is
 * authored) through a channel that exists and is enabled. The alarm tier is uppercase on the
 * notification side and matched exactly, with `*` meaning any severity.
 */
export function severityCovered(policies: readonly NotificationPolicy[], severity: string): boolean {
  const tier = severity.toUpperCase();
  return policies.some(
    (p) =>
      p.enabled &&
      // Policies are tenant-wide today and a device-type-scoped one is skipped by the dispatcher,
      // so one that somehow carries a scope does not count as covering every device.
      !p.deviceTypeToken &&
      p.rules.some(
        (r) =>
          (r.severity === tier || r.severity === '*') &&
          r.channel != null &&
          r.channel.enabled &&
          // An smtp rule with no recipients is never delivered; a webhook's endpoint is its own
          // destination and needs none.
          (r.channel.channelType !== 'smtp' || hasRecipients(r.recipients)),
      ),
  );
}

function hasRecipients(raw: string | null): boolean {
  if (!raw) return false;
  try {
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) && v.some((x) => typeof x === 'string' && x.trim() !== '');
  } catch {
    return false;
  }
}

/** Creates a tenant-wide policy that sends one severity through one channel. */
export async function createSeverityPolicy(input: {
  token: string;
  name: string;
  severity: string;
  channelToken: string;
  /** Email addresses, for an smtp channel; omitted for a webhook channel. */
  recipients?: string[];
}): Promise<void> {
  await gql('notification-management', CREATE_NOTIFICATION_POLICY, {
    request: {
      token: input.token,
      name: input.name,
      enabled: true,
      rules: [
        {
          severity: input.severity.toUpperCase(),
          channelToken: input.channelToken,
          recipients: input.recipients && input.recipients.length > 0 ? JSON.stringify(input.recipients) : null,
        },
      ],
    },
  });
}

// ── The policy registry (list / edit / create / delete) ──
//
// One selection shared by every read and write below so each result stays assignable to
// NotificationPolicyRecord. `quietHoursSlots` is an opaque JSON STRING on the wire — the
// console builds and reads it with the maintenance-window slot helpers.

const NOTIFICATION_POLICY_REGISTRY = graphql(`
  query NotificationPolicyRegistry($criteria: NotificationPolicySearchCriteria!) {
    notificationPolicies(criteria: $criteria) {
      results {
        id
        token
        name
        description
        enabled
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
`);

export async function listNotificationPolicyRecords(opts: {
  pageNumber: number;
  pageSize: number;
}): Promise<NotificationPolicySearchResults> {
  const data = await gql('notification-management', NOTIFICATION_POLICY_REGISTRY, {
    criteria: { pageNumber: opts.pageNumber, pageSize: opts.pageSize },
  });
  return data.notificationPolicies;
}

const NOTIFICATION_POLICY_BY_TOKEN = graphql(`
  query NotificationPolicyByToken($tokens: [String!]!) {
    notificationPoliciesByToken(tokens: $tokens) {
      id
      token
      name
      description
      enabled
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
`);

export async function getNotificationPolicy(token: string): Promise<NotificationPolicyRecord | null> {
  const data = await gql('notification-management', NOTIFICATION_POLICY_BY_TOKEN, { tokens: [token] });
  return data.notificationPoliciesByToken[0] ?? null;
}

const CREATE_NOTIFICATION_POLICY_RECORD = graphql(`
  mutation CreateNotificationPolicyRecord($request: NotificationPolicyCreateRequest!) {
    createNotificationPolicy(request: $request) {
      token
      updatedAt
    }
  }
`);

export async function createNotificationPolicy(request: NotificationPolicyCreateRequest) {
  const data = await gql('notification-management', CREATE_NOTIFICATION_POLICY_RECORD, { request });
  return data.createNotificationPolicy;
}

const UPDATE_NOTIFICATION_POLICY = graphql(`
  mutation UpdateNotificationPolicy(
    $token: String!
    $request: NotificationPolicyUpdateRequest!
    $expectedUpdatedAt: String
  ) {
    updateNotificationPolicy(token: $token, request: $request, expectedUpdatedAt: $expectedUpdatedAt) {
      token
      updatedAt
    }
  }
`);

/**
 * A PARTIAL update: pass only what changed (omit = keep, null = clear, value = set). Pass the
 * `updatedAt` last read as `expectedUpdatedAt` so a concurrent edit is refused rather than
 * silently overwritten. `rules` is never sent from the editor, which leaves the rules untouched.
 */
export async function updateNotificationPolicy(
  token: string,
  request: NotificationPolicyUpdateRequest,
  expectedUpdatedAt?: string | null,
) {
  const data = await gql('notification-management', UPDATE_NOTIFICATION_POLICY, {
    token,
    request,
    expectedUpdatedAt: expectedUpdatedAt ?? null,
  });
  return data.updateNotificationPolicy;
}

const DELETE_NOTIFICATION_POLICY = graphql(`
  mutation DeleteNotificationPolicy($token: String!) {
    deleteNotificationPolicy(token: $token)
  }
`);

export async function deleteNotificationPolicy(token: string): Promise<boolean> {
  const data = await gql('notification-management', DELETE_NOTIFICATION_POLICY, { token });
  return data.deleteNotificationPolicy;
}
