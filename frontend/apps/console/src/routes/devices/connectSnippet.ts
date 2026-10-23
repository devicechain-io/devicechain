// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The connectivity helper's command builder: a ready-to-run `mosquitto_pub` and `curl`
// that send one measurement event for a device.
//
// 🔴 EVERY STRING THIS PRODUCES IS PINNED BY THE PLATFORM, NOT BY THIS FILE. The rendered
// commands for a set of cases live in frontend/testdata/connect-snippets/cases.json.
// connectSnippet.test.ts holds this builder to that file byte for byte, and two Go tests
// read the same file and run the commands' values through the real ingest code:
//   - event-sources (processor/connect_snippet_golden_test.go) POSTs the curl body to the
//     curl URL through the real HTTP ingest route and decodes the mosquitto_pub message with
//     the real JSON decoder, checking the topic is the device's own events topic;
//   - device-management (processor/connect_snippet_golden_test.go) checks the client id and
//     username the broker's auth-callout would accept, and runs the payload through the
//     resolver's measurement validation and storage step, asserting no reading is dropped.
// A change here that the platform would refuse fails one of those, not a device.
//
// What the console cannot know is left as a visible placeholder, never guessed: the
// instance id and the hosts are deployment facts no API serves, and an MQTT_BASIC password
// or an ACCESS_TOKEN is stored only as a digest, so it was shown once when it was set and
// cannot be shown again.

export const PLACEHOLDER = {
  instanceId: '<instance-id>',
  mqttHost: '<mqtt-host>',
  ingestHost: '<ingest-host>',
  password: '<password>',
  accessToken: '<access-token>',
} as const;

// The broker's MQTT listener port (TLS) and the HTTP ingest listener's default port.
export const MQTT_PORT = 1883;
export const HTTP_INGEST_PORT = 8081;
// The file name the instance CA is saved under, as the device guide names it.
export const CA_FILE = 'instance-ca.crt';

// At most this many declared metrics go into the example, so it stays readable. One
// event may carry 256 readings, so this is a readability choice, not a limit.
export const MAX_EXAMPLE_METRICS = 8;

// Undeclared keys the example falls back to when the profile declares nothing usable,
// tried in order. An undeclared key is accepted unvalidated, as long as its value is a
// number; the first key the profile does NOT declare is used, since a declared key would
// be validated (and a declared STRING would be dropped).
const FALLBACK_KEYS = ['temperature', 'reading', 'value'] as const;
const FALLBACK_VALUE = '21.5';

export type SnippetCredentialType = 'ACCESS_TOKEN' | 'MQTT_BASIC';

export interface SnippetCredential {
  credentialType: SnippetCredentialType;
  credentialId: string;
}

export interface SnippetMetric {
  metricKey: string;
  dataType: string;
  minValue?: number | null;
  maxValue?: number | null;
  allowedValues?: readonly string[] | null;
}

export interface SnippetInput {
  tenant: string;
  deviceToken: string;
  credential: SnippetCredential;
  // The device's PUBLISHED metrics (deviceMetricVocabulary), never the profile draft.
  metrics: readonly SnippetMetric[];
}

export interface ExampleMeasurements {
  measurements: Record<string, string>;
  // How many usable declared metrics were left out to keep the example short.
  omitted: number;
  // True when the example uses an undeclared fallback key.
  fallback: boolean;
}

// The literals Go's strconv.ParseBool accepts, which is what ingest parses a BOOLEAN with.
const GO_BOOLS = new Set(['1', 't', 'T', 'TRUE', 'true', 'True', '0', 'f', 'F', 'FALSE', 'false', 'False']);
// A conservative subset of what strconv.ParseFloat accepts; a value outside it is simply
// not chosen, never sent.
const DECIMAL = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/;
const INTEGER = /^[+-]?\d+$/;
const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;

function withinBounds(m: SnippetMetric, n: number): boolean {
  if (m.minValue != null && n < m.minValue) return false;
  if (m.maxValue != null && n > m.maxValue) return false;
  return true;
}

// conforms mirrors model.ValidateMetricValue for the storable types: the value parses as
// the type, a number sits inside the bounds, and an allow-list contains it. It only has
// to be no more lenient than the server; the golden tests run the real one.
export function conforms(m: SnippetMetric, value: string): boolean {
  switch (m.dataType) {
    case 'DOUBLE':
      if (!DECIMAL.test(value) || !withinBounds(m, Number(value))) return false;
      break;
    case 'INT': {
      if (!INTEGER.test(value)) return false;
      const big = BigInt(value);
      if (big < INT64_MIN || big > INT64_MAX || !withinBounds(m, Number(value))) return false;
      break;
    }
    case 'BOOLEAN':
      if (!GO_BOOLS.has(value)) return false;
      break;
    default:
      // STRING and anything unknown are not stored as a measurement: ingest drops them.
      return false;
  }
  const allowed = m.allowedValues;
  return !allowed || allowed.length === 0 || allowed.includes(value);
}

function clampToBounds(m: SnippetMetric, n: number): number {
  let v = n;
  if (m.minValue != null && v < m.minValue) v = m.minValue;
  if (m.maxValue != null && v > m.maxValue) v = m.maxValue;
  return v;
}

// exampleValue picks a value ingest stores for the metric, or null when there is none to
// offer (a type ingest does not store as a measurement, an allow-list with no conforming
// member, bounds with no integer between them).
export function exampleValue(m: SnippetMetric): string | null {
  const allowed = m.allowedValues;
  if (allowed && allowed.length > 0) {
    return allowed.find((v) => conforms(m, v)) ?? null;
  }
  let candidate: string;
  switch (m.dataType) {
    case 'DOUBLE':
      candidate = withinBounds(m, 21.5) ? '21.5' : String(clampToBounds(m, 0));
      break;
    case 'INT': {
      if (withinBounds(m, 42)) {
        candidate = '42';
        break;
      }
      const lo = m.minValue != null ? Math.ceil(m.minValue) : null;
      const hi = m.maxValue != null ? Math.floor(m.maxValue) : null;
      let n = 0;
      if (lo != null && n < lo) n = lo;
      if (hi != null && n > hi) n = hi;
      if (!Number.isSafeInteger(n)) return null;
      candidate = BigInt(n).toString();
      break;
    }
    case 'BOOLEAN':
      candidate = 'true';
      break;
    default:
      return null;
  }
  return conforms(m, candidate) ? candidate : null;
}

export function exampleMeasurements(metrics: readonly SnippetMetric[]): ExampleMeasurements {
  const usable: [string, string][] = [];
  for (const m of [...metrics].sort((a, b) => (a.metricKey < b.metricKey ? -1 : a.metricKey > b.metricKey ? 1 : 0))) {
    const v = exampleValue(m);
    if (v != null) usable.push([m.metricKey, v]);
  }
  if (usable.length > 0) {
    const shown = usable.slice(0, MAX_EXAMPLE_METRICS);
    // Object.fromEntries defines each key as an own property, so even a key spelled
    // "__proto__" is a reading rather than a prototype.
    return { measurements: Object.fromEntries(shown), omitted: usable.length - shown.length, fallback: false };
  }
  const declared = new Set(metrics.map((m) => m.metricKey));
  const key = FALLBACK_KEYS.find((k) => !declared.has(k));
  if (!key) return { measurements: {}, omitted: 0, fallback: true };
  return { measurements: Object.fromEntries([[key, FALLBACK_VALUE]]), omitted: 0, fallback: true };
}

// eventBody is the JSON event the device sends. Over HTTP the body is the only authentication,
// so it carries the credential. Over MQTT the broker has already authenticated the device at
// CONNECT, and an event on the platform broker that carries a credential is REFUSED (its
// secret would be written to the broker's stream before the platform could protect it), so
// the MQTT body carries none.
export function eventBody(input: SnippetInput, transport: 'http' | 'mqtt' = 'http'): string {
  const { measurements } = exampleMeasurements(input.metrics);
  const envelope: Record<string, unknown> = {
    device: input.deviceToken,
    eventType: 'Measurement',
  };
  if (transport === 'http') {
    envelope.credentialType = input.credential.credentialType;
    envelope.credentialId = input.credential.credentialId;
    if (input.credential.credentialType === 'MQTT_BASIC') envelope.credentialSecret = PLACEHOLDER.password;
  }
  envelope.payload = { entries: [{ measurements }] };
  return JSON.stringify(envelope);
}

// shellQuote wraps a value in single quotes for a POSIX shell, so nothing in it (a $, a
// space, a quote in a metric key) is interpreted.
export function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

const CONTINUATION = ' \\\n  ';

// mqttCommand publishes the event on the device's own events topic. The client id and
// username are the shapes the broker's auth-callout requires; an ACCESS_TOKEN connects
// with no password, which is how the broker tells it from an MQTT_BASIC username.
export function mqttCommand(input: SnippetInput): string {
  const { tenant, deviceToken, credential } = input;
  const login =
    `-u ${shellQuote(`${tenant}:${credential.credentialId}`)}` +
    (credential.credentialType === 'MQTT_BASIC' ? ` -P ${shellQuote(PLACEHOLDER.password)}` : '');
  return [
    'mosquitto_pub',
    `--cafile ${CA_FILE}`,
    `-h ${shellQuote(PLACEHOLDER.mqttHost)} -p ${MQTT_PORT}`,
    `-i ${shellQuote(`${PLACEHOLDER.instanceId}:${tenant}:${deviceToken}`)}`,
    login,
    `-t ${shellQuote(`${PLACEHOLDER.instanceId}/${tenant}/devices/${deviceToken}/events`)}`,
    `-m ${shellQuote(eventBody(input, 'mqtt'))}`,
  ].join(CONTINUATION);
}

// httpCommand posts the same event to the HTTP ingest listener.
export function httpCommand(input: SnippetInput): string {
  const url = `http://${PLACEHOLDER.ingestHost}:${HTTP_INGEST_PORT}/${PLACEHOLDER.instanceId}/${input.tenant}/events`;
  return [
    `curl -i -X POST ${shellQuote(url)}`,
    `-H ${shellQuote('Content-Type: application/json')}`,
    `-d ${shellQuote(eventBody(input))}`,
  ].join(CONTINUATION);
}

// usableCredential reports whether a credential can authenticate an event right now:
// enabled, not expired, and of a type the platform accepts.
export function usableCredential(
  c: { credentialType: string; credentialId: string; enabled: boolean; expiresAt?: string | null },
  now: Date,
): c is { credentialType: SnippetCredentialType; credentialId: string; enabled: boolean; expiresAt?: string | null } {
  if (!c.enabled || !c.credentialId) return false;
  if (c.credentialType !== 'ACCESS_TOKEN' && c.credentialType !== 'MQTT_BASIC') return false;
  if (c.expiresAt) {
    const at = Date.parse(c.expiresAt);
    if (!Number.isNaN(at) && at <= now.getTime()) return false;
  }
  return true;
}
