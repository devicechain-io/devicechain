// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What an operator types into the sender to connect it: the URL, the header and, for the two
// network servers, the settings that decide whether an uplink is read at all. Pure, so the exact
// strings are pinned by connectInstructions.test.ts.
//
// 🔴 THE URL IS BUILT FROM FACTS THE CONSOLE HAS: its own origin (the instance's HTTPS front end,
// where the integrations route is published as /api/integrations/<tenant>/<integration>) and the
// signed-in tenant. Nothing in it is a guessed host or a placeholder.
//
// 🔴 THE BEARER TOKEN IS NEVER IN THE URL, only in the Authorization header: a token in a path
// lands in the ingress access log. It is also stored only as a digest, so it can be shown only
// once, when it was generated; everywhere else the header holds `<bearer-token>`.
import { shellQuote } from '@/routes/devices/connectSnippet';

/** The header value placeholder used wherever the real token can no longer be shown. */
export const BEARER_PLACEHOLDER = '<bearer-token>';

/** The Things Stack's uplink path names the platform reads; any other path is acknowledged and ignored. */
export const TTS_UPLINK_PATHS = ['up', 'uplink', 'uplinks', 'uplink-message'] as const;
/** The path the snippet suggests for it, the most specific of them. */
export const TTS_SUGGESTED_PATH = 'uplink-message';

export interface SnippetInput {
  /** The tenant the console is signed in to. */
  tenant: string;
  /** The integration's own token: the last segment of the URL. */
  integrationToken: string;
  /** The instance's public origin; defaults to the console's own, where the route is served. */
  origin?: string;
  /** The bearer token, only while it is being shown for the first time. */
  bearerToken?: string;
}

/** `<console origin>/api/integrations/<tenant>/<integration token>`. */
export function integrationUrl(i: SnippetInput): string {
  const origin = (i.origin ?? window.location.origin).replace(/\/+$/, '');
  return `${origin}/api/integrations/${encodeURIComponent(i.tenant)}/${encodeURIComponent(i.integrationToken)}`;
}

export const AUTH_HEADER_NAME = 'Authorization';

/** The value of the Authorization header, with the real token only when it is being shown once. */
export function authorizationValue(i: SnippetInput): string {
  return `Bearer ${i.bearerToken || BEARER_PLACEHOLDER}`;
}

/** `Authorization: Bearer <token>` on one line, for a curl. */
export function authorizationHeader(i: SnippetInput): string {
  return `${AUTH_HEADER_NAME}: ${authorizationValue(i)}`;
}

/** One labelled value to copy; `labelKey` is a key in the `integrations` catalog. */
export interface SnippetField {
  labelKey: string;
  value: string;
  /** True for a value that holds the secret, which is masked in screenshots. */
  secret?: boolean;
}

// Both network servers take a header as a name and a value in separate boxes.
function headerFields(i: SnippetInput): SnippetField[] {
  return [
    { labelKey: 'snippetHeaderName', value: AUTH_HEADER_NAME },
    { labelKey: 'snippetHeaderValue', value: authorizationValue(i), secret: i.bearerToken != null },
  ];
}

/** The fields to enter into a ChirpStack v4 HTTP integration. */
export function chirpstackFields(i: SnippetInput): SnippetField[] {
  return [
    { labelKey: 'snippetEncoding', value: 'JSON' },
    { labelKey: 'snippetEndpoint', value: integrationUrl(i) },
    ...headerFields(i),
  ];
}

/** The fields to enter into a The Things Stack v3 webhook. */
export function ttsFields(i: SnippetInput): SnippetField[] {
  return [
    { labelKey: 'snippetBaseUrl', value: integrationUrl(i) },
    ...headerFields(i),
    { labelKey: 'snippetUplinkPath', value: `/${TTS_SUGGESTED_PATH}` },
  ];
}

/** A ready-to-run curl for a generic source, naming the identifier where the settings say it is. */
export function genericCurl(
  i: SnippetInput,
  settings: { binary: boolean; idKind: 'pointer' | 'header' | 'query'; idValue: string },
): string {
  let url = integrationUrl(i);
  const headers = [authorizationHeader(i)];
  let body: string;
  if (settings.binary) {
    headers.push('Content-Type: application/octet-stream');
    if (settings.idKind === 'header') headers.push(`${settings.idValue}: <device-id>`);
    if (settings.idKind === 'query') url += `?${settings.idValue}=<device-id>`;
    body = '--data-binary @frame.bin';
  } else {
    headers.push('Content-Type: application/json');
    if (settings.idKind === 'header') headers.push(`${settings.idValue}: <device-id>`);
    if (settings.idKind === 'query') url += `?${settings.idValue}=<device-id>`;
    body = `-d ${shellQuote(exampleJsonBody(settings))}`;
  }
  return [`curl -i -X POST ${shellQuote(url)}`, ...headers.map((h) => `  -H ${shellQuote(h)}`), `  ${body}`].join(' \\\n');
}

// A body with the identifier where a pointer says it is, and one reading.
function exampleJsonBody(s: { idKind: string; idValue: string }): string {
  const fields: Record<string, unknown> = { temperature: 21.5 };
  if (s.idKind === 'pointer') {
    const keys = s.idValue.split('/').slice(1).map((k) => k.replace(/~1/g, '/').replace(/~0/g, '~'));
    let node: Record<string, unknown> = fields;
    keys.forEach((k, idx) => {
      if (idx === keys.length - 1) node[k] = '<device-id>';
      else node = (node[k] = {}) as Record<string, unknown>;
    });
  }
  return JSON.stringify(fields);
}
