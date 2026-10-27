// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What an operator types into the sender to connect it: the URL, the header and, for the two
// network servers, the settings that decide whether an uplink is read at all. Pure, so the exact
// strings are pinned by connectSnippet.test.ts.
//
// 🔴 WHAT IS REAL AND WHAT IS A VISIBLE PLACEHOLDER. The tenant and the integration's token are
// the caller's own and are filled in. The instance id and the public host are deployment facts no
// API serves (the same position the device connect helper is in, so it uses the same
// placeholders): they are left as `<instance-id>` and `<ingest-host>` and never guessed, because a
// guessed host is a URL that looks right and fails. A caller that DOES know them passes them in.
//
// 🔴 THE BEARER TOKEN IS NEVER IN THE URL, only in the Authorization header: a token in a path
// lands in the ingress access log. It is also stored only as a digest, so it can be shown only
// once, when it was generated; everywhere else the header holds `<bearer-token>`.
import { PLACEHOLDER } from '@/routes/devices/connectSnippet';

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
  /** The public host (and port, if any) senders reach ingest at, when known. */
  host?: string;
  /** The instance id, when known. */
  instanceId?: string;
  /** The bearer token, only while it is being shown for the first time. */
  bearerToken?: string;
}

/** `https://<host>/<instance>/<tenant>/integrations/<token>`. */
export function integrationUrl(i: SnippetInput): string {
  const host = i.host?.trim() || PLACEHOLDER.ingestHost;
  const instance = i.instanceId?.trim() || PLACEHOLDER.instanceId;
  return `https://${host}/${instance}/${encodeURIComponent(i.tenant)}/integrations/${encodeURIComponent(i.integrationToken)}`;
}

/** `Authorization: Bearer <token>`, with the real token only when it is being shown once. */
export function authorizationHeader(i: SnippetInput): string {
  return `Authorization: Bearer ${i.bearerToken || BEARER_PLACEHOLDER}`;
}

export type SnippetKind = 'chirpstack' | 'tts' | 'generic-json' | 'generic-binary';

/** One labelled value to copy; `labelKey` is a key in the `integrations` catalog. */
export interface SnippetField {
  labelKey: string;
  value: string;
  /** True for a value that holds the secret, which is masked in screenshots. */
  secret?: boolean;
}

/** The fields to enter into a ChirpStack v4 HTTP integration. */
export function chirpstackFields(i: SnippetInput): SnippetField[] {
  return [
    { labelKey: 'snippetEncoding', value: 'JSON' },
    { labelKey: 'snippetEndpoint', value: integrationUrl(i) },
    { labelKey: 'snippetHeader', value: authorizationHeader(i), secret: i.bearerToken != null },
  ];
}

/** The fields to enter into a The Things Stack v3 webhook. */
export function ttsFields(i: SnippetInput): SnippetField[] {
  return [
    { labelKey: 'snippetBaseUrl', value: integrationUrl(i) },
    { labelKey: 'snippetHeader', value: authorizationHeader(i), secret: i.bearerToken != null },
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
    body = `-d '${exampleJsonBody(settings)}'`;
  }
  return [`curl -i -X POST '${url}'`, ...headers.map((h) => `  -H '${h}'`), `  ${body}`].join(' \\\n');
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
