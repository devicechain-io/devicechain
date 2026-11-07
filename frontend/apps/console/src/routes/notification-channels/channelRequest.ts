// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Builds the create/update requests for a notification channel from the form's draft, and reads
// a stored channel back into one. Pure on purpose: the rules below are the part that can lose a
// credential or write a config the server refuses, and they are testable without a browser.
//
// The native chat/paging types (Slack, Teams, PagerDuty) keep their credential in the SECRET:
// the webhook URL or the routing key. It is write-only, so an edit sends it only when the
// operator typed a new one; leaving the box empty keeps the stored one.

import type {
  NotificationChannelCreateRequest,
  NotificationChannelUpdateRequest,
  NotificationChannelRecord,
} from '@/lib/api/notifications';

export const NATIVE_TYPES = ['slack', 'teams', 'pagerduty'] as const;
export type NativeType = (typeof NATIVE_TYPES)[number];

export const REGIONS = ['us', 'eu'] as const;

export function isNative(type: string): type is NativeType {
  return (NATIVE_TYPES as readonly string[]).includes(type);
}

export interface ChannelDraft {
  channelType: string;
  name: string;
  description: string;
  enabled: boolean;
  /** Typed credential. Empty on an edit means "keep the stored one". */
  secret: string;
  linkBaseUrl: string;
  region: string;
  source: string;
  /** The config JSON, for the types without a dedicated form (smtp, webhook). */
  rawConfig: string;
}

export function emptyDraft(channelType: string): ChannelDraft {
  return {
    channelType,
    name: '',
    description: '',
    enabled: true,
    secret: '',
    linkBaseUrl: '',
    region: 'us',
    source: '',
    rawConfig: '',
  };
}

/** The config document for a draft, or null when it carries nothing. */
export function configOf(d: ChannelDraft): string | null {
  switch (d.channelType) {
    case 'slack':
    case 'teams': {
      const url = d.linkBaseUrl.trim();
      return url ? JSON.stringify({ linkBaseUrl: url }) : null;
    }
    case 'pagerduty': {
      const cfg: Record<string, string> = {};
      // The default region is written out only when it is not the default, so a channel that
      // never touched it stores no config at all.
      if (d.region && d.region !== 'us') cfg.region = d.region;
      if (d.source.trim()) cfg.source = d.source.trim();
      if (d.linkBaseUrl.trim()) cfg.linkBaseUrl = d.linkBaseUrl.trim();
      return Object.keys(cfg).length > 0 ? JSON.stringify(cfg) : null;
    }
    default: {
      const raw = d.rawConfig.trim();
      return raw === '' ? null : raw;
    }
  }
}

/** Reads a stored channel into a draft. The secret is never returned, so it starts empty. */
export function draftOf(c: NotificationChannelRecord): ChannelDraft {
  const d = emptyDraft(c.channelType);
  d.name = c.name ?? '';
  d.description = c.description ?? '';
  d.enabled = c.enabled;
  if (!c.config) return d;
  if (!isNative(c.channelType)) {
    d.rawConfig = c.config;
    return d;
  }
  try {
    const v = JSON.parse(c.config) as Record<string, unknown>;
    if (typeof v.linkBaseUrl === 'string') d.linkBaseUrl = v.linkBaseUrl;
    if (typeof v.region === 'string') d.region = v.region;
    if (typeof v.source === 'string') d.source = v.source;
  } catch {
    // A config this form cannot read is left as stored: editRequest compares against the
    // stored text, so it is rewritten only if the operator changes a field.
  }
  return d;
}

/** Whether the draft can be submitted. `hasSecret` is whether the channel already stores one. */
export function draftProblem(d: ChannelDraft, hasSecret: boolean): 'secret' | 'config' | null {
  if (isNative(d.channelType) && d.secret.trim() === '' && !hasSecret) return 'secret';
  if (!isNative(d.channelType) && d.rawConfig.trim() !== '') {
    try {
      const v: unknown = JSON.parse(d.rawConfig);
      if (v === null || typeof v !== 'object' || Array.isArray(v)) return 'config';
    } catch {
      return 'config';
    }
  }
  return null;
}

export function createRequest(token: string, d: ChannelDraft): NotificationChannelCreateRequest {
  const req: NotificationChannelCreateRequest = {
    token,
    channelType: d.channelType,
    enabled: d.enabled,
  };
  if (d.name.trim()) req.name = d.name.trim();
  if (d.description.trim()) req.description = d.description.trim();
  const config = configOf(d);
  if (config !== null) req.config = config;
  if (d.secret !== '') req.secret = d.secret;
  return req;
}

/** A normalised view of a config document, so key order and spacing do not read as a change. */
function canonical(config: string | null | undefined): string {
  if (!config) return '';
  try {
    const v = JSON.parse(config) as Record<string, unknown>;
    return JSON.stringify(Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))));
  } catch {
    return config;
  }
}

/**
 * A PARTIAL update naming only what changed. The secret is named only when one was typed:
 * omitting it keeps the stored one, and sending null or "" would CLEAR it.
 */
export function editRequest(c: NotificationChannelRecord, d: ChannelDraft): NotificationChannelUpdateRequest {
  const req: NotificationChannelUpdateRequest = {};
  if (d.name.trim() !== (c.name ?? '')) req.name = d.name.trim() === '' ? null : d.name.trim();
  if (d.description.trim() !== (c.description ?? '')) {
    req.description = d.description.trim() === '' ? null : d.description.trim();
  }
  if (d.enabled !== c.enabled) req.enabled = d.enabled;
  const config = configOf(d);
  if (canonical(config) !== canonical(c.config)) req.config = config;
  if (d.secret !== '') req.secret = d.secret;
  return req;
}
